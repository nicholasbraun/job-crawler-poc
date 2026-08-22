package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/catalog"
	"github.com/nicholasbraun/job-crawler-poc/internal/pagegate"
)

// committedGoldSet is the Extract Gold Set as the tests reach it: cmd/llmbench is the
// working directory of every test in this package.
func committedGoldSet() string { return filepath.Join("extract-goldset", goldSetFile) }

// committedWeights is the generated table pagegate compiles in, from the same
// working directory.
func committedWeights() string {
	return filepath.Join("..", "..", "internal", "pagegate", "posting_score_weights_gen.go")
}

// TestTrainScorerReproducesTheCommittedWeights is the regenerability guard (ADR-0049):
// the artifact is a pure function of the committed Gold Set, the trainer's code and its
// flag defaults, so retraining must reproduce it byte for byte.
//
// It runs with -report=false, which costs one fit rather than the fifty the
// cross-validated curves take. That the report cannot move a byte is held by this same
// test, because the committed file was produced with the report ON.
func TestTrainScorerReproducesTheCommittedWeights(t *testing.T) {
	out := filepath.Join(t.TempDir(), "posting_score_weights_gen.go")
	if code := runTrainScorer([]string{"-in", committedGoldSet(), "-out", out, "-report=false"}); code != 0 {
		t.Fatalf("train-scorer exited %d, want 0", code)
	}

	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read regenerated weights: %v", err)
	}
	want, err := os.ReadFile(committedWeights())
	if err != nil {
		t.Fatalf("read committed weights: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf(`retraining did not reproduce %s (%d bytes regenerated, %d committed).

A weight may only move by a Gold Set change or a trainer change, and both belong in this
diff. Regenerate with "go generate ./internal/pagegate" and commit the result. Do NOT
relax this test to a tolerance comparison: the guard is what makes the shipped rule
provably the output of the labelled data.`, committedWeights(), len(got), len(want))
	}
}

// TestTrainScorerLeavesTheGoldSetUntouched holds the one thing the trainer must never
// do. Labels are human-owned (ADR-0043): the trainer reads them and has no business
// writing one, least of all to move a number it reports.
func TestTrainScorerLeavesTheGoldSetUntouched(t *testing.T) {
	before := sha256OfFile(t, committedGoldSet())
	out := filepath.Join(t.TempDir(), "weights.go")
	if code := runTrainScorer([]string{"-in", committedGoldSet(), "-out", out, "-report=false"}); code != 0 {
		t.Fatalf("train-scorer exited %d, want 0", code)
	}
	if after := sha256OfFile(t, committedGoldSet()); after != before {
		t.Errorf("the Extract Gold Set changed during a training run (%s -> %s); labels are human-owned", before, after)
	}
}

// TestTrainScorerRefusesAWiringError covers the flag guards. Each is refused BEFORE
// anything is read or written, so a mistyped knob can never leave a half-considered
// artifact on disk for the next build to compile in.
func TestTrainScorerRefusesAWiringError(t *testing.T) {
	for name, args := range map[string][]string{
		"no such gold set": {"-in", filepath.Join("extract-goldset", "no-such-file.jsonl")},
		"one fold":         {"-folds", "1"},
		"min-df zero":      {"-min-df", "0"},
		"negative vocab":   {"-vocab", "-1"},
		"empty seed":       {"-seed", ""},
		"empty in":         {"-in", ""},
		"empty out":        {"-out", ""},
	} {
		t.Run(name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "weights.go")
			full := append([]string{"-out", out, "-report=false"}, args...)
			if code := runTrainScorer(full); code != 2 {
				t.Errorf("exit code %d, want 2", code)
			}
			if _, err := os.Stat(out); err == nil {
				t.Errorf("%s was written despite the wiring error", out)
			}
		})
	}
}

// TestCommittedScoreVocabularyHoldsNoGoldSetHostWord is the leakage guard ADR-0049
// requires. There are 457 rows but far fewer hosts, so a blind fit will learn one
// sampled employer's boilerplate and cross-validation will reward it.
//
// It parses the SHIPPED bytes rather than re-running the fit, which makes it a guard on
// the artifact itself rather than on the trainer's intentions: a trainer that stopped
// applying the exclusion, or a hand-edit of the generated file, both fail here.
func TestCommittedScoreVocabularyHoldsNoGoldSetHostWord(t *testing.T) {
	samples, _, err := scorerSamples(committedGoldSet())
	if err != nil {
		t.Fatalf("read gold set: %v", err)
	}
	banned := hostTokens(samples)

	entries := parseCommittedWeights(t)
	if len(entries) == 0 {
		t.Fatal("scoreWeights parsed as empty; a rename would turn this guard into a silent pass")
	}
	words := 0
	offenders := []string{}
	for _, name := range entries {
		word, ok := vocabularyWordOf(name)
		if !ok {
			continue
		}
		words++
		if _, bad := banned[word]; bad {
			offenders = append(offenders, name)
		}
	}
	if words == 0 {
		t.Fatal("the committed table holds no Score Vocabulary entry at all; the guard has nothing to guard")
	}
	if len(offenders) > 0 {
		slices.Sort(offenders)
		t.Errorf("%d Score Vocabulary entries are a word of a Gold Set host: %s", len(offenders), strings.Join(offenders, ", "))
	}
	t.Logf("%d Score Vocabulary entries checked against %d host words over %d hosts", words, len(banned), hostCount(samples))
}

// parseCommittedWeights returns the keys of the committed scoreWeights literal, read
// through go/parser so the test sees exactly what the compiler sees.
func parseCommittedWeights(t *testing.T) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), committedWeights(), nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", committedWeights(), err)
	}
	keys := []string{}
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok || len(spec.Names) != 1 || spec.Names[0].Name != "scoreWeights" || len(spec.Values) != 1 {
			return true
		}
		lit, ok := spec.Values[0].(*ast.CompositeLit)
		if !ok {
			return true
		}
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			basic, ok := kv.Key.(*ast.BasicLit)
			if !ok || basic.Kind != token.STRING {
				continue
			}
			key, err := strconv.Unquote(basic.Value)
			if err != nil {
				t.Fatalf("unquote %s: %v", basic.Value, err)
			}
			keys = append(keys, key)
		}
		return false
	})
	return keys
}

// TestCommittedThresholdLosesNoDetailRowThePositiveEvidenceRungAccepts is the ticket's
// strongest guard: it re-derives the shipped operating point through pagegate.Score and
// pagegate.VetoThreshold -- the gate's own arithmetic over the gate's own compiled
// table -- so the trainer's internal scoring cannot silently disagree with what ships.
//
// ADR-0049 pins the threshold to the ledger rather than to recall: the deepest cut that
// loses none of the detail rows rung 8 accepts today, the SAME rows by name. A cut at
// recall parity would swap one set of postings for another and turn
// TestExtractGoldSetFalseDropGuard red for every substitution.
func TestCommittedThresholdLosesNoDetailRowThePositiveEvidenceRungAccepts(t *testing.T) {
	accepts, _ := replayPositiveEvidenceAccepts(t)
	if len(accepts) == 0 {
		t.Fatal("the Positive Evidence rung accepts nothing; the population was computed wrong")
	}

	census := vetoPoint{Threshold: pagegate.VetoThreshold}
	scorable := []scoredAccept{}
	for _, a := range accepts {
		if !a.Label.Scored() {
			continue
		}
		scorable = append(scorable, a)
		if a.Label.Positive() && a.Score < pagegate.VetoThreshold {
			t.Errorf("%s is a detail row the rung accepts and the Learned Veto would drop (score %.6f < VetoThreshold %.6f)",
				a.URL, a.Score, pagegate.VetoThreshold)
		}
	}
	census = measureVeto(scorable, census)
	before := censusOf(scorable)
	t.Logf("rung-8 accepts: %d scorable, %d detail, precision %.4f -> vetoed %d (%.2f%%), %d hub-index + %d residue removed, %d survive at precision %.4f, detail lost %d",
		before.Scorable, before.Detail, before.Precision,
		census.Vetoed, 100*census.VetoShare, census.HubIndexRemoved, census.ResidueRemoved,
		census.Survivors, census.Precision, census.DetailLost)
}

// TestThePositiveEvidenceRungSpendsTheWholeExtractBill holds the claim ADR-0049's
// population rests on: no gold-set row takes the ATS exemption, so every extract call
// on this population is rung 8's and a rule that prunes rung 8's accepts is the only
// rule with a bill attached. The day that changes, "restricted to the rows Positive
// Evidence accepts" means something else.
func TestThePositiveEvidenceRungSpendsTheWholeExtractBill(t *testing.T) {
	_, census := replayPositiveEvidenceAccepts(t)
	if census.ATSExempt != 0 {
		t.Errorf("%d gold-set rows are ATS postings exempted at rung 2; ADR-0049's population is no longer the whole extract bill", census.ATSExempt)
	}
	t.Logf("%d rows: %d rung-8 accepts, %d ats-exempt, %d ambiguous set aside, %d unlabelled",
		census.Rows, census.Accepts, census.ATSExempt, census.Ambiguous, census.Skipped)
}

// replayPositiveEvidenceAccepts replays the shipped Extract Gate over the committed
// Gold Set and returns the pages rung 8 accepts, each scored through pagegate.Score --
// the COMPILED table, not the trainer's own model. It reads the gate through
// boundaryCandidateConfig for the same reason the boundary draw does: the population
// must mean the same thing before and after the Positive Evidence default flips.
func replayPositiveEvidenceAccepts(t *testing.T) ([]scoredAccept, sampleCensus) {
	t.Helper()
	rows, err := readGoldSet(committedGoldSet())
	if err != nil {
		t.Fatalf("read gold set: %v", err)
	}
	cfg := boundaryCandidateConfig()
	census := sampleCensus{Rows: len(rows)}
	accepts := []scoredAccept{}
	for _, row := range rows {
		u, err := crawler.NewURL(row.URL)
		if err != nil {
			t.Fatalf("url %q: %v", row.URL, err)
		}
		exempt := catalog.Classify(u) == catalog.RoleJobListing
		if exempt {
			census.ATSExempt++
		}
		switch {
		case !row.Label.Valid():
			census.Skipped++
		case !row.Label.Scored():
			census.Ambiguous++
		}
		extract, rung := pagegate.ExtractDecision(u, &row.Content, cfg)
		if !extract || rung != pagegate.RungNone || exempt {
			continue
		}
		census.Accepts++
		accepts = append(accepts, scoredAccept{URL: row.URL, Label: row.Label, Score: pagegate.Score(u, &row.Content)})
	}
	slices.SortFunc(accepts, func(a, b scoredAccept) int {
		if c := cmpFloat(a.Score, b.Score); c != 0 {
			return c
		}
		return strings.Compare(a.URL, b.URL)
	})
	return accepts, census
}

// sha256OfFile is the before/after fingerprint the gold-set immutability check reads.
func sha256OfFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
