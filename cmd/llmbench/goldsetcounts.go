// This file is the Extract Gold Set's DERIVED COUNTS (ADR-0049, #304): the table of
// constants the committed record determines, the one snapshot every one of them is
// read off, and the syntax-aware rewriter goldset-refit edits them with.
//
// The counts live in a _test.go file on purpose -- they are the tests' own recorded
// standards, and a count that moved has to land in a human's diff. That is why this
// tool reads and writes that file AS DATA, through go/parser, and never links against
// the constants: a non-test build cannot see them, and a regex over three `const`
// blocks under long doc comments is a way to edit a comment by accident.
package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strconv"

	"github.com/nicholasbraun/job-crawler-poc/cmd/llmbench/bench"
)

// defaultCountsPath is the source file carrying the counts the committed record
// determines, relative to the repo root -- the same base every other llmbench default
// path is relative to.
const defaultCountsPath = "cmd/llmbench/goldset_test.go"

// recordSnapshot is the applied record as the derived counts read it: the rows
// themselves, plus ONE replay of today's shipping Positive Evidence rule over them
// (boundaryCandidateConfig, which pins both extract kill switches -- ADR-0049) and the
// boundary scorecard folded from it. Computed once, so seventeen constants cost one read
// and one replay and every one of them is read off the SAME state.
type recordSnapshot struct {
	Rows     []goldRow
	Replayed []captureRow
	Boundary bench.BoundaryScorecard
}

// snapshotRecord reads the record under dir and replays the rule the boundary
// constants are counted under. It is the counts' only view of the Gold Set, so a
// constant can never be read off a different state from its neighbour.
func snapshotRecord(dir string) (recordSnapshot, error) {
	substrate, _ := goldSetPaths(dir)
	rows, err := readGoldSet(substrate)
	if err != nil {
		return recordSnapshot{}, err
	}
	replayed, _, err := replayCaptured(substrate, boundaryCandidateConfig())
	if err != nil {
		return recordSnapshot{}, err
	}
	return recordSnapshot{
		Rows:     rows,
		Replayed: replayed,
		Boundary: bench.ScoreExtractBoundary(boundaryRowsOf(replayed)),
	}, nil
}

// countDirection says which way a HUMAN's signature moves a count, so the tool can
// refuse the direction ADR-0048 reserves to the person who withdraws a confirmation.
type countDirection int

const (
	// countPinned is a census: a move either way is drift to acknowledge, never a
	// refusal. A drawing gaining rows and an ambiguity resolving are both legitimate,
	// and both must be seen in a diff.
	countPinned countDirection = iota
	// countMayFall is a PENDING count. It falls as confirmations land; a rise means a
	// signature vanished.
	countMayFall
	// countMayRise is a CONFIRMED count. It rises as confirmations land; a fall means
	// a signature vanished.
	countMayRise
)

// derivedCount is one constant in the counts file whose value the record DETERMINES.
// Value must mirror the assertion that READS the constant, not merely resemble it:
// the verb rewriting a correct constant into a wrong one is the failure mode this
// table has to be argued against, and TestDerivedCountsMatchTheCommittedConstants is
// what holds the two arithmetics together.
type derivedCount struct {
	Name string
	// Why is one clause, printed beside a change so a moved number says what moved.
	Why       string
	Direction countDirection
	Value     func(s recordSnapshot) int
}

// derivedCounts is every count goldset-refit recomputes, in the order the counts file
// declares them, so the printed transcript reads down the file.
//
// Three of them are worth reading twice:
//
//   - pendingBoundaryConfirmations has TWO readers with slightly different
//     definitions. TestCommittedBoundaryStrataConfirmation counts every Boundary
//     Stratum row with no confirmer; TestCommittedBoundaryFalseDropsUnderTheCandidateRule
//     reads the scorecard's Unconfirmed, which replayCaptured computes over LABELLED
//     rows only. The first dominates the second and both are ceilings, so taking the
//     first keeps both green. Do not take the smaller.
//   - pendingVetoBoundaryConfirmations is its twin on the OTHER Boundary Stratum, and
//     the two are deliberately never pooled: one number over both strata would let a
//     confirmation landing on ADR-0044's drawing hide a signature vanishing from
//     ADR-0049's. Every Boundary Stratum owes one of these, which
//     TestCommittedBoundaryStrataConfirmation asserts as a census rather than trusting
//     this table to be complete.
//   - randomStreamAcceptRate is the one number in that file that LOOKS derived and is
//     not: it is #261's census measurement of the live extract stream, and the
//     committed weights are checked against it. It is never rewritten, and being a
//     float literal it falls outside the completeness guard structurally as well.
var derivedCounts = []derivedCount{
	{
		Name: "pendingHumanConfirmations", Direction: countMayFall,
		Why:   "lone-posting rows still awaiting a human confirmation",
		Value: func(s recordSnapshot) int { return pendingIn(s, stratumLonePosting) },
	},
	{
		Name: "pendingExpectedConfirmations", Direction: countMayFall,
		Why: "expected extractions still awaiting a human confirmation",
		Value: func(s recordSnapshot) int {
			n := 0
			for _, row := range s.Rows {
				if row.Expected != nil && row.Expected.ConfirmedBy == "" {
					n++
				}
			}
			return n
		},
	},
	{
		Name: "structuralStratumRows", Direction: countPinned,
		Why:   "rows in the #254 structural drawing",
		Value: func(s recordSnapshot) int { return rowsInDrawing(s, drawingStructural) },
	},
	{
		Name: "randomStratumRows", Direction: countPinned,
		Why:   "rows in the #262 random drawing",
		Value: func(s recordSnapshot) int { return rowsInDrawing(s, drawingRandom) },
	},
	{
		Name: "boundaryStratumRows", Direction: countPinned,
		Why:   "rows in the #263 boundary drawing",
		Value: func(s recordSnapshot) int { return rowsInDrawing(s, drawingBoundary) },
	},
	{
		Name: "vetoBoundaryStratumRows", Direction: countPinned,
		Why:   "rows in the Learned Veto's own boundary drawing (ADR-0049)",
		Value: func(s recordSnapshot) int { return rowsInDrawing(s, drawingVetoBoundary) },
	},
	{
		Name: "randomSpotChecks", Direction: countMayRise,
		Why: "random-stratum rows a human has read and confirmed",
		Value: func(s recordSnapshot) int {
			n := 0
			for _, row := range s.Rows {
				if row.Stratum == stratumRandom && row.LabelProvenance.ConfirmedBy != "" {
					n++
				}
			}
			return n
		},
	},
	{
		Name: "pendingBoundaryConfirmations", Direction: countMayFall,
		Why:   "Boundary Stratum rows still awaiting a human confirmation",
		Value: func(s recordSnapshot) int { return pendingIn(s, stratumBoundary) },
	},
	{
		Name: "pendingVetoBoundaryConfirmations", Direction: countMayFall,
		Why:   "Learned Veto boundary rows still awaiting a human confirmation",
		Value: func(s recordSnapshot) int { return pendingIn(s, stratumVetoBoundary) },
	},
	{
		Name: "ambiguousRows", Direction: countPinned,
		Why: "rows a reading could not settle",
		Value: func(s recordSnapshot) int {
			n := 0
			for _, row := range s.Rows {
				if row.Label == bench.ExtractAmbiguous {
					n++
				}
			}
			return n
		},
	},
	{
		Name: "boundaryDetailRows", Direction: countPinned,
		Why:   "Boundary Stratum rows labelled detail",
		Value: func(s recordSnapshot) int { return s.Boundary.Counts[bench.ExtractDetail] },
	},
	{
		Name: "boundaryRowsRecovered", Direction: countPinned,
		Why:   "Boundary Stratum rows the Positive Evidence rung extracts",
		Value: func(s recordSnapshot) int { return s.Boundary.Extracted },
	},
	{
		Name: "boundaryDetailRecovered", Direction: countPinned,
		Why:   "Job Listings the widening got back",
		Value: func(s recordSnapshot) int { return boundaryRecovered(s, bench.ExtractDetail) },
	},
	{
		Name: "boundaryNonPostingsRecovered", Direction: countPinned,
		Why: "hub-index and residue rows the widening now extracts",
		Value: func(s recordSnapshot) int {
			return boundaryRecovered(s, bench.ExtractHubIndex) + boundaryRecovered(s, bench.ExtractResidue)
		},
	},
	{
		Name: "boundaryAmbiguousRecovered", Direction: countPinned,
		Why:   "undecidable rows the widening now extracts",
		Value: func(s recordSnapshot) int { return boundaryRecovered(s, bench.ExtractAmbiguous) },
	},
	{
		Name: "boundaryFalseDropsRemaining", Direction: countPinned,
		Why:   "real Job Listings the Positive Evidence rung still drops on the boundary",
		Value: func(s recordSnapshot) int { return len(s.Boundary.FalseDrops) },
	},
	{
		Name: "boundaryAmbiguousSkipped", Direction: countPinned,
		Why:   "ambiguous Boundary Stratum rows the rung drops",
		Value: func(s recordSnapshot) int { return s.Boundary.AmbiguousSkipped },
	},
}

// refitNonDerivedCounts names the integer constants in the counts file that are NOT
// determined by the record, with the reason. It is empty today and exists so
// TestEveryDerivedCountInTheCountsFileIsOwned forces a decision when a new one is
// added, rather than letting the refit silently leave it behind.
var refitNonDerivedCounts = map[string]string{}

// rowsInDrawing counts the rows of one drawing, exactly as
// TestCommittedGoldSetIsWellFormed partitions them.
func rowsInDrawing(s recordSnapshot, d goldDrawing) int {
	n := 0
	for _, row := range s.Rows {
		if row.Stratum.Drawing() == d {
			n++
		}
	}
	return n
}

// pendingIn counts one stratum's rows that carry no human confirmer, which is what
// every pending ratchet in the counts file means.
func pendingIn(s recordSnapshot, stratum goldStratum) int {
	n := 0
	for _, row := range s.Rows {
		if row.Stratum == stratum && row.LabelProvenance.ConfirmedBy == "" {
			n++
		}
	}
	return n
}

// boundaryRecovered counts the Boundary Stratum rows of one label the replayed rule
// extracts -- the per-label split TestCommittedBoundaryRecoveryLedger pins, which the
// scorecard's own totals do not carry.
func boundaryRecovered(s recordSnapshot, label bench.ExtractLabel) int {
	n := 0
	for _, row := range s.Replayed {
		if row.Stratum == stratumBoundary && row.Extract && row.Label == label {
			n++
		}
	}
	return n
}

// derivedCountNames is the table's names in file order, which is the order the
// transcript prints and the order a refusal lists.
func derivedCountNames() []string {
	names := make([]string, 0, len(derivedCounts))
	for _, c := range derivedCounts {
		names = append(names, c.Name)
	}
	return names
}

// countLiteral is one constant's integer literal located in a source file: the byte
// range of the VALUE token alone, so a rewrite replaces the number and nothing else.
type countLiteral struct {
	Name  string
	Value int
	// Start and End bound the literal in the file's bytes, [Start,End).
	Start, End int
}

// constDecl is one top-level constant as this file reads source: its name, and the
// bare integer literal it is bound to where it is bound to one.
type constDecl struct {
	Name string
	// Literal locates the integer literal and carries its value. IsInt is false when
	// the constant is bound to anything else -- a float, an expression, an identifier,
	// or nothing at all where iota repeats the previous line -- and then Literal is the
	// zero value.
	Literal countLiteral
	IsInt   bool
}

// readConstDecls parses path with go/parser and returns its bytes beside every
// TOP-LEVEL constant it declares, in declaration order. The two travel together
// because a splice is only valid against the bytes its offsets were taken from.
//
// Only file scope counts: a `const` inside a function body is a local alias, not one of
// the record's recorded standards.
func readConstDecls(path string) ([]byte, []constDecl, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read counts %q: %w", path, err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return nil, nil, fmt.Errorf("parse counts %q: %w", path, err)
	}

	decls := []constDecl{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				out := constDecl{Name: name.Name}
				if i < len(vs.Values) {
					if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.INT {
						n, err := strconv.Atoi(lit.Value)
						if err != nil {
							return nil, nil, fmt.Errorf("counts %q: %s = %s: %w", path, name.Name, lit.Value, err)
						}
						out.IsInt = true
						out.Literal = countLiteral{
							Name:  name.Name,
							Value: n,
							Start: fset.Position(lit.Pos()).Offset,
							End:   fset.Position(lit.End()).Offset,
						}
					}
				}
				decls = append(decls, out)
			}
		}
	}
	return src, decls, nil
}

// readCountLiterals is readConstDecls narrowed to the constants in names, and it FAILS
// when one of them is missing, declared twice, or bound to anything but a bare integer
// literal -- because a count this verb silently skipped is a Gold Set change the diff
// does not show, which is the one thing the hard-coded counts exist to prevent.
func readCountLiterals(path string, names []string) ([]byte, map[string]countLiteral, error) {
	src, decls, err := readConstDecls(path)
	if err != nil {
		return nil, nil, err
	}

	wanted := make(map[string]struct{}, len(names))
	for _, n := range names {
		wanted[n] = struct{}{}
	}
	lits := make(map[string]countLiteral, len(names))
	for _, d := range decls {
		if _, ok := wanted[d.Name]; !ok {
			continue
		}
		if _, dup := lits[d.Name]; dup {
			return nil, nil, fmt.Errorf("counts %q: %s is declared twice; a rewrite could not say which one the record determines", path, d.Name)
		}
		if !d.IsInt {
			return nil, nil, fmt.Errorf("counts %q: %s is not a bare integer literal; goldset-refit rewrites the number and nothing else, so anything else here would be silently left behind", path, d.Name)
		}
		lits[d.Name] = d.Literal
	}

	missing := []string{}
	for _, n := range names {
		if _, ok := lits[n]; !ok {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return nil, nil, fmt.Errorf("counts %q does not declare %v; the record determines them, so a refit that could not find one would leave a Gold Set change out of the diff", path, missing)
	}
	return src, lits, nil
}

// countChange is one derived count as this pass read it: the value the record
// determines beside the value the counts file records. EVERY count produces one,
// moved or not -- the verb prints all of them, because a count that held still is
// evidence too, and computing the moved ones separately would be a second definition
// of the same arithmetic.
type countChange struct {
	Name, Why string
	Direction countDirection
	Old, New  int
}

// moved reports whether the record disagrees with the file, i.e. whether this count is
// one of the rewrites that will land in the diff.
func (c countChange) moved() bool { return c.Old != c.New }

// regression reports whether the move means a HUMAN SIGNATURE VANISHED -- a pending
// count rising, or a confirmed count falling. goldset-refit refuses those outright
// (ADR-0048: a ratchet may only be moved that way by the person who withdraws the
// confirmation). A pinned census is never a regression: it moving either way is drift
// to acknowledge in the diff, which is what pinning it is for.
func (c countChange) regression() bool {
	switch c.Direction {
	case countMayFall:
		return c.New > c.Old
	case countMayRise:
		return c.New < c.Old
	default:
		return false
	}
}

// countChanges reads every derived count off the snapshot and pairs it with the value
// the counts file records, in the table's file order.
func countChanges(s recordSnapshot, lits map[string]countLiteral) []countChange {
	out := make([]countChange, 0, len(derivedCounts))
	for _, c := range derivedCounts {
		out = append(out, countChange{
			Name:      c.Name,
			Why:       c.Why,
			Direction: c.Direction,
			Old:       lits[c.Name].Value,
			New:       c.Value(s),
		})
	}
	return out
}

// rewriteCounts splices the moved values into src and returns the new source. The
// splices are applied RIGHT TO LEFT so an earlier literal's offsets stay valid, and
// the result goes through go/format so a malformed splice fails HERE rather than at
// the next build.
func rewriteCounts(src []byte, lits map[string]countLiteral, changes []countChange) ([]byte, error) {
	moved := []countChange{}
	for _, c := range changes {
		if c.moved() {
			moved = append(moved, c)
		}
	}
	slices.SortFunc(moved, func(a, b countChange) int { return lits[b.Name].Start - lits[a.Name].Start })

	out := bytes.Clone(src)
	for _, c := range moved {
		lit, ok := lits[c.Name]
		if !ok {
			return nil, fmt.Errorf("rewrite %s: no literal was located for it", c.Name)
		}
		next := make([]byte, 0, len(out)+len(strconv.Itoa(c.New)))
		next = append(next, out[:lit.Start]...)
		next = append(next, strconv.Itoa(c.New)...)
		next = append(next, out[lit.End:]...)
		out = next
	}
	formatted, err := format.Source(out)
	if err != nil {
		return nil, fmt.Errorf("the rewritten counts do not parse: %w", err)
	}
	return formatted, nil
}
