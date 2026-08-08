// This file is the Extract Gold Set's BOUNDARY drawing (ADR-0043, #263): the two
// gate configs whose disagreement DEFINES the Boundary Stratum, the streaming pass
// that computes that disagreement over the capture, and the human confirmation
// sheet the stratum's labels are owed. It replays the real Extract Gate and nothing
// else -- no network, no model, no crawler behaviour.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/nicholasbraun/job-crawler-poc/cmd/llmbench/bench"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/pagegate"
)

// boundaryCensusWeight is every Boundary Stratum row's weight. It is exactly 1
// because the stratum is a CENSUS: every page in the disagreement's accept half is
// taken, so each row's inclusion probability is 1 and its inverse is 1. There is no
// accept-share correction to apply -- a census describes no stream, so there is no
// population to weight toward -- and the per-drawing weight balance therefore holds
// by construction.
const boundaryCensusWeight = 1.0

// boundaryBaselineConfig is TODAY'S blanket accept: the Extract Gate as it behaved
// before ADR-0044's Positive Evidence rung. RequirePositiveEvidence is set FALSE
// explicitly rather than left at its default so this keeps meaning "the previous
// behaviour" after #264 flips that default on.
func boundaryBaselineConfig() crawler.LLMGateConfig {
	cfg := crawler.DefaultLLMGateConfig()
	cfg.RequirePositiveEvidence = false
	return cfg
}

// boundaryCandidateConfig is the tiered Positive Evidence rule (ADR-0044, #258,
// internal/pagegate/positive_evidence.go) that DEFINES this stratum. TRUE is set
// explicitly for the same reason the baseline sets FALSE.
//
// The rule lives HERE, in code, rather than in a -gate-config flag: a flag would let
// a later run silently redefine the boundary, and the committed rows would then
// claim a boundary nobody could re-derive. Changing
// internal/pagegate/positive_evidence.go invalidates the committed stratum, and
// TestCommittedBoundaryStratumIsTheDisagreementSet is what says so out loud.
func boundaryCandidateConfig() crawler.LLMGateConfig {
	cfg := crawler.DefaultLLMGateConfig()
	cfg.RequirePositiveEvidence = true
	return cfg
}

// boundaryDisagreements re-streams the capture and replays BOTH gate configs over
// each candidate's captured Content, partitioning the pages the two disagree on by
// the live extractor's own verdict. It decodes one record at a time and keeps only
// the candidate handle, so peak memory stays proportional to the disagreement set
// rather than to the whole capture.
//
// reversed collects any page the CANDIDATE extracts that the baseline skips. The
// rung is purely additive, so that set must be empty; returning it rather than
// asserting it means a future rule that stops being additive is REPORTED instead of
// silently halving the boundary.
func boundaryDisagreements(path string, scan captureScan) (accept, abstain []candidate, reversed []string, err error) {
	byLine := map[int]candidate{}
	for _, c := range scan.Candidates {
		byLine[c.Line] = c
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open capture %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	baseline, positive := boundaryBaselineConfig(), boundaryCandidateConfig()
	accept, abstain, reversed = []candidate{}, []candidate{}, []string{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), captureScanBuffer)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		cand, ok := byLine[lineNo]
		if !ok {
			continue
		}
		var rec goldRow
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			return nil, nil, nil, fmt.Errorf("capture line %d: %w", lineNo, err)
		}
		u, err := crawler.NewURL(rec.URL)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("capture line %d: url %q: %w", lineNo, rec.URL, err)
		}
		before := pagegate.ShouldExtract(u, &rec.Content, baseline)
		after := pagegate.ShouldExtract(u, &rec.Content, positive)
		switch {
		case before == after:
			continue
		case !before && after:
			reversed = append(reversed, rec.URL)
		case cand.Verdict:
			accept = append(accept, cand)
		default:
			abstain = append(abstain, cand)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, nil, nil, fmt.Errorf("read capture %q: %w", path, err)
	}
	return accept, abstain, reversed, nil
}

// withStratum rewrites each candidate's stratum to s. The boundary draw builds its
// selection by hand rather than through applyPlan -- a census has no quota to apply
// and no share to weight by -- so this is where its rows are stamped.
func withStratum(cands []candidate, s goldStratum) []candidate {
	out := make([]candidate, 0, len(cands))
	for _, c := range cands {
		c.Stratum = s
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].URL < out[j].URL })
	return out
}

// boundaryRowsOf projects the replayed capture rows of the Boundary Stratum onto
// what ScoreExtractBoundary folds, carrying each row's confirmation state.
func boundaryRowsOf(rows []captureRow) []bench.BoundaryRow {
	out := []bench.BoundaryRow{}
	for _, row := range rows {
		if row.Stratum != stratumBoundary {
			continue
		}
		out = append(out, bench.BoundaryRow{ExtractVerdictRow: row.ExtractVerdictRow, Confirmed: row.Confirmed})
	}
	return out
}

const (
	// confirmHeadRunes and confirmMidRunes are the two page-text windows the
	// confirmation sheet shows. They are wider than the labeler's worksheet: the
	// confirmer is settling a label somebody else proposed, and a posting's role body
	// often starts below the first screen of a German page.
	confirmHeadRunes = 2000
	confirmMidRunes  = 1200
	// confirmRowsPerFile is the default chunk size. The confirmation is a human pass
	// over ~190 rows; chunking it is what makes a PARTIAL pass legitimate and visible
	// rather than an abandoned one.
	confirmRowsPerFile = 20
)

// confirmChunk is one rendered chunk of the confirmation sheet: its file name, its
// Markdown body, and the row ids it covers, so the caller can write the matching
// id file the human edits down to what they actually read.
type confirmChunk struct {
	Name string
	Body []byte
	IDs  []string
}

// renderConfirmSheet renders rows as ordered Markdown chunks of at most perFile
// rows each. Ordering is by row id: stable, quotable, and independent of the
// proposed label, so the sheet's order can never group the rows by the answer.
//
// Every JSON-LD-derived field, the live extractor's verdict and the gate's own
// marks are WITHHELD -- the same discipline the labeler's worksheet keeps, for a
// sharper reason. The confirmer answers one question, "is this page one Job
// Listing?", and the number the whole stratum exists to produce is what the gate
// concluded about these very pages; showing them that conclusion would anchor the
// answer the rule is being judged against. labels.tsv still carries the verdict:
// that is the REVIEW surface, read after the label exists.
func renderConfirmSheet(rows []goldRow, perFile int) []confirmChunk {
	if perFile <= 0 {
		perFile = confirmRowsPerFile
	}
	ordered := make([]goldRow, len(rows))
	copy(ordered, rows)
	sort.Slice(ordered, func(i, j int) bool { return rowID(ordered[i].URL) < rowID(ordered[j].URL) })

	chunks := []confirmChunk{}
	total := (len(ordered) + perFile - 1) / perFile
	for i := 0; i < len(ordered); i += perFile {
		end := min(i+perFile, len(ordered))
		index := len(chunks) + 1
		chunk := confirmChunk{Name: fmt.Sprintf("confirm-%02d.md", index)}
		var b strings.Builder
		fmt.Fprintf(&b, "# Boundary Stratum confirmation %d/%d\n\n", index, total)
		b.WriteString("One question per row: **is this page ONE Job Listing?**\n\n")
		b.WriteString("- `detail` -- the page IS one Job Listing: one role's responsibilities or requirements, and an apply action.\n")
		b.WriteString("- `hub-index` -- the page LISTS openings (a board root, a search result, a location or department facet). A page listing exactly one opening is still `hub-index`.\n")
		b.WriteString("- `residue` -- neither: culture, about, benefits, blog, press, contact, a cookie or login wall, a JS shell, a 404, a salary guide, a \"post a job\" form, or a withdrawn posting with no role body.\n")
		b.WriteString("- `ambiguous` -- the page genuinely does not resolve. Say what the tension is in the note.\n\n")
		b.WriteString("The proposed label is an LLM's. Disagreeing with it is the point of this pass.\n\n")
		for _, row := range ordered[i:end] {
			c := confirmViewOf(row)
			chunk.IDs = append(chunk.IDs, c.ID)
			fmt.Fprintf(&b, "---\n\n## %s\n\n", c.ID)
			fmt.Fprintf(&b, "- url: <%s>\n", c.URL)
			fmt.Fprintf(&b, "- proposed: **%s** -- %s\n", c.Label, orNone(c.Note))
			fmt.Fprintf(&b, "- proposed by: %s\n", orNone(c.ProposedBy))
			fmt.Fprintf(&b, "- page title: %s\n", orNone(c.Title))
			fmt.Fprintf(&b, "- outbound links: %d total, %d same-host, %d job-like\n\n", c.URLsTotal, c.URLsSameHost, c.URLsJoblike)
			fmt.Fprintf(&b, "head:\n\n```\n%s\n```\n\n", orNone(c.Head))
			if c.Mid != "" {
				fmt.Fprintf(&b, "mid:\n\n```\n%s\n```\n\n", c.Mid)
			}
		}
		b.WriteString("---\n\nWhen every row above is read, write the ids you confirm to a file and run:\n\n")
		b.WriteString("```sh\n")
		fmt.Fprintf(&b, "printf '%%s\\n' %s > confirmed-%02d.txt\n", strings.Join(chunk.IDs, " "), index)
		fmt.Fprintf(&b, "go run ./cmd/llmbench goldset-apply -confirmed-by \"<your name>\" -confirm-ids confirmed-%02d.txt\n", index)
		b.WriteString("```\n\n")
		b.WriteString("A row whose label you want CHANGED does not belong in that file: edit its `label` column in\n")
		b.WriteString("`cmd/llmbench/extract-goldset/labels.tsv` first (which retracts any confirmation on it), run\n")
		b.WriteString("`goldset-apply`, then confirm it in the next pass.\n")
		chunk.Body = []byte(b.String())
		chunks = append(chunks, chunk)
	}
	return chunks
}

// confirmView is one row as the confirmer reads it: what the page itself says plus
// the label somebody proposed for it, and nothing the gate derived.
type confirmView struct {
	ID         string
	URL        string
	Label      bench.ExtractLabel
	Note       string
	ProposedBy string
	Title      string
	Head       string
	Mid        string

	URLsTotal    int
	URLsSameHost int
	URLsJoblike  int
}

// confirmViewOf builds a row's confirmation view. It reuses worksheetFor for the
// page text and link counts so the two review surfaces can never disagree about
// what a page says, and re-cuts the text windows wider.
func confirmViewOf(row goldRow) confirmView {
	ws := worksheetFor(row)
	main := []rune(flattenField(row.Content.MainContent))
	mid := ""
	if start := len(main) / 3; start > confirmHeadRunes {
		mid = string(main[start:min(start+confirmMidRunes, len(main))])
	}
	return confirmView{
		ID:           ws.ID,
		URL:          ws.URL,
		Label:        row.Label,
		Note:         row.LabelProvenance.Note,
		ProposedBy:   row.LabelProvenance.ProposedBy,
		Title:        ws.Title,
		Head:         truncateRunes(flattenField(row.Content.MainContent), confirmHeadRunes),
		Mid:          mid,
		URLsTotal:    ws.URLsTotal,
		URLsSameHost: ws.URLsSameHost,
		URLsJoblike:  ws.URLsJoblike,
	}
}

// orNone renders an empty field as an explicit marker, so a confirmer can tell a
// page that said nothing from a sheet that lost the text.
func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(none)"
	}
	return s
}

// parseConfirmIDs parses a confirmation id file: one row id per line, blank and
// '#' lines skipped. It is what makes a chunk-by-chunk human pass reviewable --
// the diff shows exactly which rows gained a confirmer.
func parseConfirmIDs(data []byte) (map[string]struct{}, error) {
	ids := map[string]struct{}{}
	for i, line := range strings.Split(string(data), "\n") {
		id := strings.TrimSpace(line)
		if id == "" || strings.HasPrefix(id, "#") {
			continue
		}
		if strings.ContainsAny(id, " \t") {
			return nil, fmt.Errorf("confirm-ids line %d: %q is not a bare row id", i+1, id)
		}
		ids[id] = struct{}{}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("confirm-ids names no row; an empty confirmation is not a confirmation")
	}
	return ids, nil
}
