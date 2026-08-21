// This file is llmbench's Extract Gold Set substrate (ADR-0043, #254): the row
// format, the streaming IO over the extract-decision tap's capture, and the pure
// sampling / stratification / weighting / review-sheet helpers the goldset-* verbs
// drive. The boundary drawing's own machinery -- the two gate configs whose
// disagreement defines it, and the confirmation sheet it is owed -- lives beside this
// in goldsetboundary.go, because it replays the gate where nothing here does. This
// file produces no crawler behaviour and touches no network or model.
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nicholasbraun/job-crawler-poc/cmd/llmbench/bench"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

// goldRow is one Extract Gold Set row: an extract-capture record
// (internal/extractcapture: url, verdict, ts, renderer, content) extended IN PLACE
// with the label and sampling metadata ADR-0043 requires. It is deliberately a superset of
// the tap's own record rather than a new format, so an unlabeled capture file and
// a committed gold-set row read through the same decoder, and a later spec can add
// a stratum to the same file with no migration.
//
// Content is the parsed page the LIVE extractor and Extract Gate saw, serialized
// by the tap at decision time. It is never re-fetched: a page re-fetched months
// later is a different page, and that drift corrupts the label (ADR-0043).
type goldRow struct {
	URL string `json:"url"`
	// Verdict is the extractor's ORIGINAL live accept/abstain (true = it read a
	// single job posting off the page). It is evidence, never a re-run, and it is
	// half of the sampling cell — never the label.
	Verdict bool   `json:"verdict"`
	TS      string `json:"ts"`
	// Renderer names the renderer that produced Content.MainContent
	// (parser.RendererID, #281): parser.RendererStructural for a Structural Rendering,
	// "flattened-v1" for the plain Flattened Text. ABSENT on every row drawn before
	// the stamp existed -- all 457 committed rows -- which is why it is omitempty:
	// those rows carry an UNKNOWN renderer, not an empty one, and re-serializing the
	// substrate must leave them byte for byte as they are. A row with no renderer is
	// also the row that cannot be shown structurally to a labeller and has to be
	// re-fetched instead (ADR-0047).
	Renderer string             `json:"renderer,omitempty"`
	Label    bench.ExtractLabel `json:"label"`
	// Stratum is the STRUCTURAL cell the row was drawn from, not its label: you
	// cannot stratify on a label you have not produced yet, and a stratum defined
	// by the label would make the sample circular.
	Stratum goldStratum `json:"stratum"`
	// Weight is the row's inverse inclusion probability, normalized so the file's
	// weights sum to its row count. Derived from the capture's per-verdict caps
	// (weightsFor), NEVER from the file's own accept/abstain mix.
	Weight          float64        `json:"weight"`
	LabelProvenance goldProvenance `json:"label_provenance"`
	// Expected is the field-fidelity ground truth a Free Extraction is scored
	// against. Absent until #256 fills it; declared here so the row format never
	// migrates.
	Expected *goldExpected   `json:"expected,omitempty"`
	Content  crawler.Content `json:"content"`
}

// goldProvenance records who proposed a row's label and who confirmed it
// (ADR-0043). ConfirmedBy is EMPTY until a human has actually seen the row —
// the tooling never fills it on its own behalf, and never overwrites a name
// already there.
type goldProvenance struct {
	// ProposedBy names the proposer, prefixed "llm:" for a model-authored label.
	ProposedBy string `json:"proposed_by"`
	ProposedAt string `json:"proposed_at"` // RFC3339 UTC
	// ProposedLabel is the label ProposedBy actually proposed, kept when a human
	// overrides it (ADR-0048). It is what makes a confirmation pass measurable: how
	// often an independent human and the proposer reach the same answer is the direct
	// evidence for what the still-unconfirmed rows are worth. It is
	// first-writer-wins exactly as ProposedBy is, so the two always name one
	// proposer and the label that proposer gave.
	//
	// omitempty because it is ABSENT on the rows a human confirmed before it
	// existed: the set does not record which of those were relabelled during the pass
	// that confirmed them, and an empty-but-present field would be a claim that none
	// were. Re-serializing such a row must leave it byte for byte as it is
	// (TestCommittedGoldSetIsByteStableThroughTheDecoder).
	ProposedLabel bench.ExtractLabel `json:"proposed_label,omitempty"`
	// ConfirmedBy names the human who confirmed the label. A confirmation is only
	// meaningful from a person, so goldset-apply rejects an "llm:" name here.
	ConfirmedBy string `json:"confirmed_by"`
	ConfirmedAt string `json:"confirmed_at"`
	// Note is a short rationale in the labeler's own words (<= 80 chars), never
	// text copied off the page.
	Note string `json:"note"`
}

// goldExpected is the field-fidelity ground truth #256 scores a Free Extraction
// against: what a correct extraction of this page must produce. It is carried on
// every row the Free Extraction fires on -- the lone-posting stratum -- and on no
// other, since an expectation for a page the mechanism never serves is unscorable.
//
// The values are read off the page's own structured data by a reading INDEPENDENT
// of the Go traversal they score (scripts/propose-expected.sh). Proposing them by
// running the decorator would make the guard a tautology: it could then only ever
// catch a future regression, never a bug that exists today.
type goldExpected struct {
	Title           string `json:"title"`
	Location        string `json:"location"`
	WorkArrangement string `json:"work_arrangement"`
	// FreeOK accepts a Free Extraction on a page that is NOT a posting as a known,
	// argued exception instead of a fatal precision failure. It is honoured for a
	// residue row only -- a hub-index is the exact shape ADR-0042's predicate
	// rejects structurally, so excusing one would mean the predicate itself is
	// broken. Never set by tooling on its own behalf.
	FreeOK bool `json:"free_ok,omitempty"`
	// FreeOKNote is the one-line argument for that acceptance, in the reviewer's own
	// words. Required whenever FreeOK is set: an exception with no stated reason
	// cannot be reviewed or withdrawn.
	FreeOKNote string `json:"free_ok_note,omitempty"`
	// ProposedBy names who read these values off the page's structured data,
	// prefixed "script:" or "llm:" for an automated reading. ConfirmedBy is EMPTY
	// until a human has actually seen the row, exactly like goldProvenance.
	ProposedBy  string `json:"proposed_by"`
	ProposedAt  string `json:"proposed_at"` // RFC3339 UTC
	ConfirmedBy string `json:"confirmed_by"`
	ConfirmedAt string `json:"confirmed_at"`
}

// goldStratum is a row's structural sampling cell: what the page's structured
// data looks like, decided by the ADR-0042 lone-posting predicate. It is fixed at
// sampling time and frozen in the committed file.
type goldStratum string

const (
	// stratumLonePosting is exactly one JSON-LD JobPosting node, no ItemList
	// wrapping a posting, and a non-empty title -- the exact shape the Free
	// Extraction (ADR-0042) fires on. This is where the mechanism lives.
	stratumLonePosting goldStratum = "lone-posting"
	// stratumAmbiguousPosting carries structured posting data the Free Extraction
	// must refuse: two or more JobPosting nodes, an ItemList wrapping one, or a
	// lone node with no title.
	stratumAmbiguousPosting goldStratum = "ambiguous-posting"
	// stratumNoPosting carries no JobPosting node at all -- the matched hub-index
	// and residue population.
	stratumNoPosting goldStratum = "no-posting"
	// stratumRandom is the ADR-0043 random stratum (#262): pages drawn at RANDOM
	// from the live extract stream within their verdict cell, each weighted back to
	// it, so composition and precision computed over them describe production rather
	// than the sample's own mix. It is a sampling cell like the other three, but it
	// belongs to a DIFFERENT DRAWING: its weights rest on a different capture frame
	// and a different accept-rate measurement, so they are never pooled with theirs
	// (see goldDrawing).
	stratumRandom goldStratum = "random"
	// stratumBoundary is the ADR-0043 Boundary Stratum (#263): the pages where
	// TODAY'S blanket accept and the tiered Positive Evidence rule DISAGREE --
	// computed by replaying both gate configs over the captured content, never
	// guessed. It is where a false-drop hides, which is why it is the stratum ADR-0043
	// requires a human to confirm.
	//
	// It is a CENSUS of that disagreement's accept half, not a sample: inclusion
	// probability 1, weight 1, and no stream behind it to weight toward. Like the
	// random stratum it is its own DRAWING, and its rows never enter the weighted
	// stream estimates -- a census of a disagreement set describes no population.
	stratumBoundary goldStratum = "boundary"
)

// allStrata is the fixed print / iteration order for per-stratum breakdowns.
// Mirrors bench.AllExtractLabels.
var allStrata = []goldStratum{stratumLonePosting, stratumAmbiguousPosting, stratumNoPosting, stratumRandom, stratumBoundary}

// Valid reports whether s is one of the known strata.
func (s goldStratum) Valid() bool {
	return s == stratumLonePosting || s == stratumAmbiguousPosting || s == stratumNoPosting ||
		s == stratumRandom || s == stratumBoundary
}

// goldDrawing groups strata into the DRAW they came from. A drawing is the scope a
// weight normalizes over: the #254 structural strata were drawn from the July
// capture (4271 pages, accept share 0.3432) under a structural design, and the #262
// random stratum from the August faithful frame (5162 pages, accept share 0.0753)
// under a verdict-only one. Each drawing's weights sum to ITS OWN row count, so
// pooling them across drawings would be meaningless arithmetic.
//
// It is derived from Stratum and never persisted -- adding a field to the row would
// make it look like a second axis a labeler or a sheet could disagree about. It is
// also deliberately not domain language: it is this tool's normalization scope, not
// a term the crawler uses.
type goldDrawing string

const (
	// drawingStructural is the #254 draw: lone-posting / ambiguous-posting /
	// no-posting, stratified on the ADR-0042 predicate.
	drawingStructural goldDrawing = "structural"
	// drawingRandom is the #262 draw: a random sample of the stream, stratified on
	// the verdict alone.
	drawingRandom goldDrawing = "random"
	// drawingBoundary is the #263 draw: a census of the pages two gate rules
	// disagree on. Its weights are all 1 -- there is nothing to normalize toward,
	// which is exactly why it is a drawing of its own rather than more rows in
	// another one.
	drawingBoundary goldDrawing = "boundary"
)

// Drawing returns the drawing s belongs to, or "" for a stratum-less row -- a raw,
// unsampled capture line, which belongs to no draw and carries no weight.
func (s goldStratum) Drawing() goldDrawing {
	switch s {
	case stratumLonePosting, stratumAmbiguousPosting, stratumNoPosting:
		return drawingStructural
	case stratumRandom:
		return drawingRandom
	case stratumBoundary:
		return drawingBoundary
	default:
		return ""
	}
}

// cellPlan is one line of the sampling design: how many rows to draw from the
// (Stratum, Verdict) cell. N == 0 means take the whole cell.
type cellPlan struct {
	Stratum goldStratum
	Verdict bool
	N       int
}

// samplePlan is the ADR-0043 sampling design. The mechanism's own stratum is
// sampled heavily and its abstain half EXHAUSTIVELY, because that half is the
// model-override population: pages the model abstained on that nonetheless
// publish a lone structured posting, i.e. exactly where a Free Extraction can
// silently create a listing the model rejected. The no-posting cells are the
// matched hub-index / residue set. It is a package var, not a flag, because the
// design is an argument rather than a knob; a later spec adds its cell here.
//
// The plan must cover every (stratum, verdict) cell: the weights normalize over
// it, so an uncovered cell would silently drop stream mass. sampleFromCapture
// rejects a candidate that lands outside it.
// The realized populations at sampling time (2026-08-07, 4271 deduped pages) are
// in the accept/abstain quota comments. The lone-posting accept quota is set so
// the lone-posting stratum lands at 70 rows -- the review set a human must confirm
// (ADR-0043) -- once its exhaustive abstain half is counted.
var samplePlan = []cellPlan{
	{stratumLonePosting, true, 46},      // population 777
	{stratumLonePosting, false, 0},      // population 24 -- the model-override rows, all of them
	{stratumAmbiguousPosting, true, 0},  // population 0
	{stratumAmbiguousPosting, false, 0}, // population 1 -- structurally ambiguous pages barely survive the gate
	{stratumNoPosting, true, 33},        // population 1037 -- real postings publishing NO structured data
	{stratumNoPosting, false, 45},       // population 2432 -- where hub-index and residue live
}

// randomSamplePlan is the #262 random drawing's design. Its ONLY sampling cell is
// the verdict: the per-verdict caps are the only thing the capture design imposed,
// so stratifying structurally as samplePlan does would destroy the honest
// composition this stratum exists to produce -- the whole point is that the mix of
// detail / hub-index / residue in it is the stream's mix, not a design's. Like
// samplePlan it is a package var rather than a flag, because the design is an
// argument rather than a knob.
//
// The frame is the FAITHFUL capture window only: records at or after
// 2026-08-07T21:13Z, the first after `fceaf87 fix(parser): only strip site chrome on
// the body fallback`. Two parser changes landed mid-capture, and ADR-0043's premise
// is that captured content is the exact bytes the gate will later see, so content
// from a parser that no longer exists is a different page. Deduped by URL and with
// the 42 URLs the #254 drawing already committed excluded (a page cannot carry two
// incompatible weights), that frame is 5162 pages: 1762 accept, 3400 abstain.
//
// The quotas below sample the accept cell at 40/1762 = 2.27% and the abstain cell at
// 80/3400 = 2.35%, so the draw is within a percentage point of a simple random
// sample of the frame and the weights carry the CAP CORRECTION and nothing else.
// With the true stream accept rate p = 0.0753 (#261's census measurement, never the
// file's own 0.4265 mix, which overstates accepts 5.7x) weightsFor collapses to
// w_accept = 3p = 0.2259 and w_abstain = 1.5(1-p) = 1.38705, summing to 120.
var randomSamplePlan = []cellPlan{
	{stratumRandom, true, 40},  // population 1762 -- the stream's accepts
	{stratumRandom, false, 80}, // population 3400 -- the stream's abstains
}

const (
	// defaultGoldSetDir holds the committed Extract Gold Set, relative to the repo
	// root (the working directory `go run ./cmd/llmbench` is invoked from).
	defaultGoldSetDir = "cmd/llmbench/extract-goldset"
	// goldSetFile is the substrate: one goldRow per line, sorted by URL.
	goldSetFile = "goldset.jsonl"
	// labelsFile is the human review surface rendered from the substrate: one
	// compact line per row, the file a reviewer reads in a diff and edits to
	// correct a label.
	labelsFile = "labels.tsv"
	// expectedFile is the SECOND review surface (#256): one line per row carrying
	// an expected extraction, i.e. exactly the rows the Free Extraction fires on.
	// It is deliberately not four more columns on labelsFile -- that file is 150
	// lines a reviewer already worked through for #254, and widening it would
	// change parseSheet's column contract. This one focuses the #256 review on its
	// own 70 rows.
	expectedFile = "expected.tsv"
	// goldSetPerm is the mode every COMMITTED gold-set file lands with. It is named
	// because the write path stages through os.CreateTemp, which creates 0600: a
	// committed file must not silently become owner-only on the next apply (#283).
	goldSetPerm os.FileMode = 0o644
	// defaultSeed keys the deterministic within-cell selection. Changing it is a
	// deliberate resample, which is why it is a flag rather than a constant use.
	defaultSeed = "extract-goldset-v1"
	// defaultRandomSeed keys the #262 random drawing. It is a DISTINCT seed because
	// it is a distinct draw: reusing defaultSeed would correlate the two samples'
	// within-cell orders for no reason.
	defaultRandomSeed = "extract-goldset-random-v1"
	// captureScanBuffer bounds one capture line. Captured lines carry the full
	// parsed MainContent; the largest observed is ~757 KB.
	captureScanBuffer = 8 * 1024 * 1024
	// maxCandidateBytes drops a capture line whose raw JSON exceeds it. A single
	// pathological page would otherwise be a large fraction of the committed file
	// for no evidentiary gain.
	maxCandidateBytes = 512 * 1024
	// sessionGap splits the capture timeline into collection sessions. The tap
	// appends to one file across runs, and the per-verdict caps reset per process,
	// so the cap analysis must be per session.
	sessionGap = time.Hour
)

// captureHead decodes only the fields the candidate pass needs, so a 5669-line,
// 87 MB capture is stratified without ever holding a whole page in a row struct
// beyond the current line.
type captureHead struct {
	URL     string `json:"url"`
	Verdict bool   `json:"verdict"`
	TS      string `json:"ts"`
	Content struct {
		JSONLD []string `json:"JSONLD"`
	} `json:"content"`
}

// candidate is one deduped capture line reduced to what sampling decides on. The
// page content is deliberately absent: it is re-read from the capture only for the
// lines the plan selects.
type candidate struct {
	URL     string
	Verdict bool
	TS      string
	Stratum goldStratum
	// Line is the 1-based capture line the candidate won, so the emit pass picks
	// the exact same record the dedupe chose.
	Line int
}

// tsVerdict is one capture record reduced to the two fields the cap analysis
// reads, in file order.
type tsVerdict struct {
	TS      time.Time
	Verdict bool
}

// captureScan is the outcome of one streaming pass over the capture: the deduped
// candidates, the verdict timeline the weights are reconstructed from, and the
// counts an operator needs to trust the sample.
type captureScan struct {
	Candidates []candidate
	Timeline   []tsVerdict
	Lines      int
	Oversized  int
	BadURL     int
	Duplicates int
}

// scanCapture streams the extract-capture JSONL at path and reduces it to sampling
// candidates: one per URL, keeping the record with the LATEST ts (ties resolve to
// the last line in the file), because the newest parse is the closest to today's
// parser. Lines whose raw JSON exceeds maxCandidateBytes, and lines whose URL the
// domain rejects, are dropped and counted rather than failing the scan -- a
// harvest artifact must never make the whole capture unusable. A malformed line is
// an error: silently dropping it would bias the sample invisibly.
func scanCapture(path string) (captureScan, error) {
	f, err := os.Open(path)
	if err != nil {
		return captureScan{}, fmt.Errorf("open capture %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	out := captureScan{}
	byURL := map[string]candidate{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), captureScanBuffer)
	for sc.Scan() {
		out.Lines++
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		if len(line) > maxCandidateBytes {
			out.Oversized++
			continue
		}
		var head captureHead
		if err := json.Unmarshal(line, &head); err != nil {
			return captureScan{}, fmt.Errorf("capture line %d: %w", out.Lines, err)
		}
		if _, err := crawler.NewURL(head.URL); err != nil {
			out.BadURL++
			continue
		}
		if ts, err := time.Parse(time.RFC3339, head.TS); err == nil {
			out.Timeline = append(out.Timeline, tsVerdict{TS: ts, Verdict: head.Verdict})
		}
		cand := candidate{
			URL:     head.URL,
			Verdict: head.Verdict,
			TS:      head.TS,
			Stratum: stratumOf(head.Content.JSONLD),
			Line:    out.Lines,
		}
		if prev, dup := byURL[head.URL]; dup {
			out.Duplicates++
			if prev.TS > cand.TS {
				continue
			}
		}
		byURL[head.URL] = cand
	}
	if err := sc.Err(); err != nil {
		return captureScan{}, fmt.Errorf("read capture %q: %w", path, err)
	}

	out.Candidates = make([]candidate, 0, len(byURL))
	for _, c := range byURL {
		out.Candidates = append(out.Candidates, c)
	}
	sort.Slice(out.Candidates, func(i, j int) bool { return out.Candidates[i].Line < out.Candidates[j].Line })
	return out, nil
}

// frameSince narrows a scan to the capture records at or after cutoff, returning
// the narrowed scan and how many candidates it dropped. It is how the #262 drawing
// excludes the windows parsed by a superseded parser: ADR-0043's premise is that a
// captured page is the exact bytes the gate will later see, and content from a
// parser that no longer exists is a different page.
//
// Filtering AFTER the scan is sound, not a shortcut: scanCapture already resolved
// each URL to its LATEST record, so a URL captured on both sides of the cutoff keeps
// its post-cutoff parse and survives, while a URL seen only before it drops
// entirely. A candidate whose TS does not parse drops too -- a record that cannot be
// placed in time cannot be placed in the frame.
//
// Timeline is cut to match, so a caller estimating the accept share off the narrowed
// scan reads the frame's own timeline rather than the whole file's.
func frameSince(scan captureScan, cutoff time.Time) (captureScan, int) {
	out := scan
	out.Candidates = make([]candidate, 0, len(scan.Candidates))
	dropped := 0
	for _, c := range scan.Candidates {
		ts, err := time.Parse(time.RFC3339, c.TS)
		if err != nil || ts.Before(cutoff) {
			dropped++
			continue
		}
		out.Candidates = append(out.Candidates, c)
	}
	out.Timeline = make([]tsVerdict, 0, len(scan.Timeline))
	for _, r := range scan.Timeline {
		if r.TS.Before(cutoff) {
			continue
		}
		out.Timeline = append(out.Timeline, r)
	}
	return out, dropped
}

// excludingURLs drops the candidates whose URL is already in exclude, returning the
// narrowed scan and the count dropped. The #262 drawing excludes the URLs the #254
// drawing already committed: a duplicate URL would fail the substrate's
// well-formedness guard, and it would give one page two incompatible weights from
// two different drawings.
//
// The exclusion carries a mild, stated bias -- the pages a walk re-visits most often
// are the likeliest to have been drawn twice -- which is recorded in the gold set's
// README rather than corrected for.
func excludingURLs(scan captureScan, exclude map[string]struct{}) (captureScan, int) {
	out := scan
	out.Candidates = make([]candidate, 0, len(scan.Candidates))
	dropped := 0
	for _, c := range scan.Candidates {
		if _, skip := exclude[c.URL]; skip {
			dropped++
			continue
		}
		out.Candidates = append(out.Candidates, c)
	}
	return out, dropped
}

// asStratum rewrites every candidate's stratum to s, so applyPlan draws from cells
// keyed on the verdict alone. It is what makes the #262 drawing reuse the #254
// weighting machinery unchanged: the random draw's only sampling cell is the
// verdict, and collapsing the structural stratum before planning expresses that
// without a second weighting function to keep in step with the first.
func asStratum(scan captureScan, s goldStratum) captureScan {
	out := scan
	out.Candidates = make([]candidate, 0, len(scan.Candidates))
	for _, c := range scan.Candidates {
		c.Stratum = s
		out.Candidates = append(out.Candidates, c)
	}
	return out
}

// stratumOf classifies a page's structured data into its sampling cell using the
// ADR-0042 lone-posting predicate exactly: exactly one JobPosting node, no
// ItemList wrapping a posting, and a non-empty title ON THAT NODE. The page's own
// <title> is deliberately NOT a fallback -- a Free Extraction reads its title out
// of the structured data, so a title-less node is a shape the mechanism must
// refuse, and folding it into the lone-posting cell would hide the refusal.
//
// This walk deliberately duplicates crawler.PostingBody's scanJobPostings rather
// than calling it: #253 moves that read into the domain as a typed result and is
// being delivered IN PARALLEL on the same spec branch, so depending on it would
// couple two independent tickets. The stratum is frozen in the committed data, so
// collapsing this onto the domain function later is behaviour-neutral for the file
// that already exists.
func stratumOf(blocks []string) goldStratum {
	postings, itemListOfJob, title := scanPostings(blocks)
	switch {
	case postings == 0:
		return stratumNoPosting
	case postings == 1 && !itemListOfJob && strings.TrimSpace(title) != "":
		return stratumLonePosting
	default:
		return stratumAmbiguousPosting
	}
}

// scanPostings folds every JSON-LD block on a page into the three facts the
// stratum is decided on: how many JobPosting nodes the page carries, whether an
// ItemList among them wraps at least one (an openings index), and the title of the
// first posting node carrying one. An unparseable block contributes nothing and
// never fails the read -- the same fail-safe the domain walk applies.
func scanPostings(blocks []string) (postings int, itemListOfJob bool, title string) {
	for _, block := range blocks {
		var node any
		if err := json.Unmarshal([]byte(block), &node); err != nil {
			continue
		}
		p, il, t := scanPostingNode(node)
		postings += p
		itemListOfJob = itemListOfJob || il
		if title == "" {
			title = t
		}
	}
	return postings, itemListOfJob, title
}

// scanPostingNode walks a decoded JSON-LD value -- arrays, objects, and any
// @graph / itemListElement / item nesting -- mirroring the domain walk rule for
// rule.
func scanPostingNode(v any) (postings int, itemListOfJob bool, title string) {
	switch node := v.(type) {
	case []any:
		for _, item := range node {
			p, il, t := scanPostingNode(item)
			postings += p
			itemListOfJob = itemListOfJob || il
			if title == "" {
				title = t
			}
		}
	case map[string]any:
		self := 0
		if isPostingType(node["@type"], "jobposting") {
			self = 1
			if s, ok := node["title"].(string); ok {
				title = s
			}
		}
		descendants := 0
		for _, val := range node {
			p, il, t := scanPostingNode(val)
			descendants += p
			itemListOfJob = itemListOfJob || il
			if title == "" {
				title = t
			}
		}
		postings = self + descendants
		if isPostingType(node["@type"], "itemlist") && descendants > 0 {
			itemListOfJob = true
		}
	}
	return postings, itemListOfJob, title
}

// isPostingType reports whether a JSON-LD @type value (a string, or an array of
// strings) names the given schema.org type, matched case-insensitively as a
// substring so a bare "JobPosting" and a "https://schema.org/JobPosting" both hit.
func isPostingType(t any, want string) bool {
	switch tv := t.(type) {
	case string:
		return strings.Contains(strings.ToLower(tv), want)
	case []any:
		for _, item := range tv {
			if s, ok := item.(string); ok && strings.Contains(strings.ToLower(s), want) {
				return true
			}
		}
	}
	return false
}

// cellKey identifies one sampling cell.
type cellKey struct {
	Stratum goldStratum
	Verdict bool
}

// cellResult reports what the plan actually drew from one cell, so an operator can
// see the realized design rather than the intended one.
type cellResult struct {
	Key        cellKey
	Population int
	Sampled    int
	Weight     float64
}

// selection is the outcome of applying samplePlan to a scan: the chosen
// candidates (sorted by URL), the per-cell accounting, and the accept share the
// weights were built on.
type selection struct {
	Chosen      []candidate
	Cells       []cellResult
	AcceptShare float64
	// AcceptShareEstimated is false when the caller supplied the share explicitly.
	AcceptShareEstimated bool
}

// applyPlan draws the sample: it buckets candidates into (stratum, verdict) cells,
// takes min(quota, population) from each by deterministic hash order, and attaches
// each cell's row weight. acceptShare is the live extract stream's accept share
// (see liveAcceptShare); pass a value in (0,1) to override the estimate.
// A candidate landing outside samplePlan is an error: the weights normalize over
// the plan, so an uncovered cell would silently drop stream mass.
func applyPlan(scan captureScan, plan []cellPlan, seed string, acceptShare float64) (selection, error) {
	covered := map[cellKey]int{}
	for _, p := range plan {
		key := cellKey{p.Stratum, p.Verdict}
		if _, dup := covered[key]; dup {
			return selection{}, fmt.Errorf("sample plan: duplicate cell %s/%v", p.Stratum, p.Verdict)
		}
		covered[key] = p.N
	}

	buckets := map[cellKey][]candidate{}
	for _, c := range scan.Candidates {
		key := cellKey{c.Stratum, c.Verdict}
		if _, ok := covered[key]; !ok {
			return selection{}, fmt.Errorf("sample plan does not cover cell %s/%v (candidate %s)", c.Stratum, c.Verdict, c.URL)
		}
		buckets[key] = append(buckets[key], c)
	}

	sel := selection{AcceptShare: acceptShare}
	if acceptShare <= 0 || acceptShare >= 1 {
		estimated, ok := liveAcceptShare(scan.Timeline)
		if !ok {
			return selection{}, fmt.Errorf("cannot estimate the live accept share from the capture timeline: pass -accept-share explicitly")
		}
		sel.AcceptShare, sel.AcceptShareEstimated = estimated, true
	}

	chosen := []candidate{}
	counts := map[cellKey]int{}
	pops := map[cellKey]int{}
	for _, p := range plan {
		key := cellKey{p.Stratum, p.Verdict}
		cell := buckets[key]
		pops[key] = len(cell)
		take := takeByHash(cell, seed, p.N)
		counts[key] = len(take)
		chosen = append(chosen, take...)
	}

	weights, err := weightsFor(plan, pops, counts, sel.AcceptShare)
	if err != nil {
		return selection{}, err
	}
	for _, p := range plan {
		key := cellKey{p.Stratum, p.Verdict}
		sel.Cells = append(sel.Cells, cellResult{Key: key, Population: pops[key], Sampled: counts[key], Weight: weights[key]})
	}

	sort.Slice(chosen, func(i, j int) bool { return chosen[i].URL < chosen[j].URL })
	sel.Chosen = chosen
	return sel, nil
}

// takeByHash returns the first n candidates in ascending sha256(seed + "\n" + url)
// order, or the whole cell when n <= 0 or n exceeds the population. Hashing rather
// than an RNG means the same capture and seed always yield a byte-identical file,
// while a new seed is a genuine, unbiased resample.
func takeByHash(cell []candidate, seed string, n int) []candidate {
	ordered := make([]candidate, len(cell))
	copy(ordered, cell)
	sort.Slice(ordered, func(i, j int) bool {
		hi, hj := seededHash(seed, ordered[i].URL), seededHash(seed, ordered[j].URL)
		if hi == hj {
			return ordered[i].URL < ordered[j].URL
		}
		return hi < hj
	})
	if n <= 0 || n > len(ordered) {
		return ordered
	}
	return ordered[:n]
}

// seededHash is the deterministic sort key: hex sha256 over seed and url.
func seededHash(seed, url string) string {
	sum := sha256.Sum256([]byte(seed + "\n" + url))
	return hex.EncodeToString(sum[:])
}

// rowID is a row's short stable handle: the first 12 hex digits of sha256(url).
// It is what a labeler quotes when proposing a label, so a 120-character URL never
// has to be retyped (and cannot be mistyped) on the way into the file.
func rowID(url string) string {
	sum := sha256.Sum256([]byte(url))
	return hex.EncodeToString(sum[:])[:12]
}

// liveAcceptShare estimates the extract stream's accept share BEFORE the tap's
// per-verdict caps truncated it (ADR-0043: "the caps are the sampling design", so
// the file's own accept/abstain mix is design, not stream, and must never be used
// as a weight).
//
// Records are grouped into capture sessions -- a ts gap above sessionGap starts a
// new one, since the tap appends across process restarts and its caps reset with
// each. Within a session every record after min(last accept, last abstain) is
// dropped: from that point one verdict had hit its cap and the mix stopped
// describing the stream. The estimate is the accept share of what remains, pooled
// over sessions. ok is false when no session retains a record, which is the honest
// answer for a capture whose caps bound immediately.
func liveAcceptShare(seq []tsVerdict) (share float64, ok bool) {
	accepts, total := 0, 0
	for _, session := range splitSessions(seq) {
		lastAccept, lastAbstain := -1, -1
		for i, r := range session {
			if r.Verdict {
				lastAccept = i
			} else {
				lastAbstain = i
			}
		}
		cutoff := lastAccept
		if lastAbstain < cutoff {
			cutoff = lastAbstain
		}
		for i := 0; i <= cutoff; i++ {
			total++
			if session[i].Verdict {
				accepts++
			}
		}
	}
	if total == 0 {
		return 0, false
	}
	return float64(accepts) / float64(total), true
}

// splitSessions cuts the capture timeline into collection sessions at every gap
// wider than sessionGap. The sequence is taken in file order; the tap appends
// monotonically, so a record older than its predecessor is treated as part of the
// same session rather than starting a new one.
func splitSessions(seq []tsVerdict) [][]tsVerdict {
	sessions := [][]tsVerdict{}
	current := []tsVerdict{}
	for i, r := range seq {
		if i > 0 && r.TS.Sub(seq[i-1].TS) > sessionGap {
			sessions = append(sessions, current)
			current = []tsVerdict{}
		}
		current = append(current, r)
	}
	if len(current) > 0 {
		sessions = append(sessions, current)
	}
	return sessions
}

// weightsFor returns each cell's per-row sampling weight: the inverse of its
// inclusion probability, normalized so the file's weights sum to its row count.
//
// A cell's share of the live extract stream is its verdict's cap-corrected share
// (acceptShare, reconstructed by liveAcceptShare) times the cell's share of that
// verdict's captured population -- so the verdict mix comes from the cap analysis
// and only the WITHIN-verdict structural split comes from the capture, which the
// caps do not distort. The row weight is that stream share divided by the cell's
// share of the sample, which is what makes an exhaustively-sampled rare cell count
// for less than a thinly-sampled common one.
func weightsFor(plan []cellPlan, pops, counts map[cellKey]int, acceptShare float64) (map[cellKey]float64, error) {
	if acceptShare <= 0 || acceptShare >= 1 {
		return nil, fmt.Errorf("accept share must be in (0,1), got %g", acceptShare)
	}

	popByVerdict := map[bool]int{}
	sampled := 0
	for _, p := range plan {
		key := cellKey{p.Stratum, p.Verdict}
		popByVerdict[p.Verdict] += pops[key]
		sampled += counts[key]
	}
	if sampled == 0 {
		return nil, fmt.Errorf("sample is empty: nothing to weight")
	}

	weights := map[cellKey]float64{}
	for _, p := range plan {
		key := cellKey{p.Stratum, p.Verdict}
		n := counts[key]
		if n == 0 {
			continue
		}
		if popByVerdict[p.Verdict] == 0 {
			continue
		}
		verdictShare := 1 - acceptShare
		if p.Verdict {
			verdictShare = acceptShare
		}
		streamShare := verdictShare * float64(pops[key]) / float64(popByVerdict[p.Verdict])
		weights[key] = streamShare / (float64(n) / float64(sampled))
	}
	return weights, nil
}

// readGoldSet reads the committed Extract Gold Set at path into memory. The file
// is a few thousand rows at most, so it is read whole -- unlike the capture it was
// drawn from, which is only ever streamed.
func readGoldSet(path string) ([]goldRow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open gold set %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	rows := []goldRow{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), captureScanBuffer)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		if len(sc.Bytes()) == 0 {
			continue
		}
		var row goldRow
		if err := json.Unmarshal(sc.Bytes(), &row); err != nil {
			return nil, fmt.Errorf("gold set %q line %d: %w", path, lineNo, err)
		}
		rows = append(rows, row)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read gold set %q: %w", path, err)
	}
	return rows, nil
}

// goldSetWrite returns the staged write that lays the substrate down at path: rows
// sorted by URL, one JSON object per line with a trailing newline, HTML escaping off
// to match the tap so a captured URL's "&" stays literal and the file diffs as the
// operator reads it. Encoding streams straight into the staging file, so a row the
// encoder refuses aborts the write before anything is renamed.
//
// The sort happens here rather than inside the closure: the closure runs after the
// group's other files are staged, and a caller mutating rows in between would
// otherwise reorder this file and not the review sheets rendered from the same rows.
func goldSetWrite(path string, rows []goldRow) fileWrite {
	ordered := make([]goldRow, len(rows))
	copy(ordered, rows)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].URL < ordered[j].URL })

	return fileWrite{Path: path, Perm: goldSetPerm, Write: func(w io.Writer) error {
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		for _, row := range ordered {
			if err := enc.Encode(row); err != nil {
				return fmt.Errorf("encode %q: %w", row.URL, err)
			}
		}
		return nil
	}}
}

// writeGoldSet writes rows to path as the substrate, atomically: the content is
// staged beside path and renamed over it, so a crash, a full disk or a row the
// encoder refuses leaves the committed file exactly as it was rather than truncated
// (#283).
func writeGoldSet(path string, rows []goldRow) error { return atomicWrite(goldSetWrite(path, rows)) }

// sheetHeader names the review sheet's columns. It is written as a leading
// comment line so the file is self-describing in a diff.
const sheetHeader = "#id\turl\tstratum\tverdict\tlabel\tproposed_by\tconfirmed_by\tnote\ttitle"

// sheetTitleRunes caps the informational title column so one row stays one
// readable line.
const sheetTitleRunes = 80

// sheetRow is one parsed line of labels.tsv. It is the review surface, not the
// substrate: id keys the merge, stratum and verdict are echoed so a stale sheet is
// detected rather than silently applied, and title is informational only.
type sheetRow struct {
	ID          string
	URL         string
	Stratum     goldStratum
	Verdict     bool
	Label       bench.ExtractLabel
	ProposedBy  string
	ConfirmedBy string
	Note        string
}

// renderSheet renders rows as the review sheet: a header comment plus one
// tab-separated line per row, sorted by URL. Every field has tabs and newlines
// collapsed to spaces on write, so a page title carrying either can never split or
// widen a line.
func renderSheet(rows []goldRow) []byte {
	ordered := make([]goldRow, len(rows))
	copy(ordered, rows)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].URL < ordered[j].URL })

	var b strings.Builder
	b.WriteString(sheetHeader)
	b.WriteString("\n")
	for _, row := range ordered {
		fields := []string{
			rowID(row.URL),
			row.URL,
			string(row.Stratum),
			strconv.FormatBool(row.Verdict),
			string(row.Label),
			row.LabelProvenance.ProposedBy,
			row.LabelProvenance.ConfirmedBy,
			row.LabelProvenance.Note,
			truncateRunes(flattenField(row.Content.Title), sheetTitleRunes),
		}
		for i, f := range fields {
			if i > 0 {
				b.WriteString("\t")
			}
			b.WriteString(flattenField(f))
		}
		b.WriteString("\n")
	}
	return []byte(b.String())
}

// parseSheet parses a rendered review sheet back into rows, skipping the header
// and any blank line. A line with the wrong column count, an unparseable verdict,
// or an unknown non-empty label is an error: a half-applied sheet is worse than a
// rejected one.
func parseSheet(data []byte) ([]sheetRow, error) {
	rows := []sheetRow{}
	for i, line := range strings.Split(string(data), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 9 {
			return nil, fmt.Errorf("sheet line %d: got %d columns, want 9", i+1, len(fields))
		}
		verdict, err := strconv.ParseBool(fields[3])
		if err != nil {
			return nil, fmt.Errorf("sheet line %d: verdict %q: %w", i+1, fields[3], err)
		}
		label := bench.ExtractLabel(fields[4])
		if label != "" && !label.Valid() {
			return nil, fmt.Errorf("sheet line %d: unknown label %q", i+1, label)
		}
		rows = append(rows, sheetRow{
			ID:          fields[0],
			URL:         fields[1],
			Stratum:     goldStratum(fields[2]),
			Verdict:     verdict,
			Label:       label,
			ProposedBy:  fields[5],
			ConfirmedBy: fields[6],
			Note:        fields[7],
		})
	}
	return rows, nil
}

// expectedSheetHeader names the expected-extraction sheet's columns, written as a
// leading comment line so the file is self-describing in a diff.
const expectedSheetHeader = "#id\turl\tlabel\ttitle\tlocation\twork_arrangement\tfree_ok\tfree_ok_note\tproposed_by\tconfirmed_by"

// expectedSheetColumns is the exact column count parseExpectedSheet requires. A
// line of any other width is a sheet edited against a different renderer, which is
// rejected rather than half-applied.
const expectedSheetColumns = 10

// expectedSheetRow is one parsed line of expected.tsv: the review surface for the
// field-fidelity ground truth, not the substrate. id keys the merge and label is
// echoed so a sheet edited against an older sample is detected rather than
// silently applied.
type expectedSheetRow struct {
	ID              string
	URL             string
	Label           bench.ExtractLabel
	Title           string
	Location        string
	WorkArrangement string
	FreeOK          bool
	FreeOKNote      string
	ProposedBy      string
	ConfirmedBy     string
}

// renderExpectedSheet renders the rows that carry an expected extraction as the
// #256 review sheet, sorted by URL, every field flattened so a page title carrying
// a tab or a newline can never split or widen a line.
func renderExpectedSheet(rows []goldRow) []byte {
	ordered := make([]goldRow, len(rows))
	copy(ordered, rows)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].URL < ordered[j].URL })

	var b strings.Builder
	b.WriteString(expectedSheetHeader)
	b.WriteString("\n")
	for _, row := range ordered {
		if row.Expected == nil {
			continue
		}
		fields := []string{
			rowID(row.URL),
			row.URL,
			string(row.Label),
			row.Expected.Title,
			row.Expected.Location,
			row.Expected.WorkArrangement,
			strconv.FormatBool(row.Expected.FreeOK),
			row.Expected.FreeOKNote,
			row.Expected.ProposedBy,
			row.Expected.ConfirmedBy,
		}
		for i, f := range fields {
			if i > 0 {
				b.WriteString("\t")
			}
			b.WriteString(flattenField(f))
		}
		b.WriteString("\n")
	}
	return []byte(b.String())
}

// parseExpectedSheet parses a rendered expected-extraction sheet back into rows,
// skipping the header and any blank line. A wrong column count, an unparseable
// free_ok, an unknown non-empty label, a work_arrangement outside the domain's
// enum, or a free_ok with no stated reason is an error: a half-applied sheet is
// worse than a rejected one, and an unreviewable exception is worse than none.
func parseExpectedSheet(data []byte) ([]expectedSheetRow, error) {
	rows := []expectedSheetRow{}
	for i, line := range strings.Split(string(data), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != expectedSheetColumns {
			return nil, fmt.Errorf("expected sheet line %d: got %d columns, want %d", i+1, len(fields), expectedSheetColumns)
		}
		label := bench.ExtractLabel(fields[2])
		if label != "" && !label.Valid() {
			return nil, fmt.Errorf("expected sheet line %d: unknown label %q", i+1, label)
		}
		arrangement := fields[5]
		if string(crawler.NormalizeWorkArrangement(arrangement)) != arrangement {
			return nil, fmt.Errorf("expected sheet line %d: work_arrangement %q is not one of the domain's values", i+1, arrangement)
		}
		freeOK, err := strconv.ParseBool(fields[6])
		if err != nil {
			return nil, fmt.Errorf("expected sheet line %d: free_ok %q: %w", i+1, fields[6], err)
		}
		if freeOK && strings.TrimSpace(fields[7]) == "" {
			return nil, fmt.Errorf("expected sheet line %d: free_ok with no stated reason; an exception nobody can read is one nobody can withdraw", i+1)
		}
		rows = append(rows, expectedSheetRow{
			ID:              fields[0],
			URL:             fields[1],
			Label:           label,
			Title:           fields[3],
			Location:        fields[4],
			WorkArrangement: arrangement,
			FreeOK:          freeOK,
			FreeOKNote:      fields[7],
			ProposedBy:      fields[8],
			ConfirmedBy:     fields[9],
		})
	}
	return rows, nil
}

// flattenField collapses every whitespace run -- tabs and newlines included -- to
// a single space so a field can never break the sheet's line/column structure.
func flattenField(s string) string { return strings.Join(strings.Fields(s), " ") }

// truncateRunes caps s at n RUNES (never bytes: a byte cut could split a
// multi-byte character, and captured pages are routinely German).
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// worksheetRow is the labeler's view of a page: what the page itself says, with
// every structured-data-derived field DELIBERATELY WITHHELD. If a labeler could
// see that a page publishes a lone JobPosting, the labels would agree with the
// structured data by construction -- exactly the circularity ADR-0043 exists to
// end. It is a working artifact, never committed: it regenerates from the
// substrate.
//
// The live extractor's VERDICT is withheld for the same reason (#262). The random
// stratum's headline number is the precision of that verdict, so a labeler who could
// see it would agree with it by construction. labels.tsv still shows the verdict:
// that is the REVIEW surface, read after the label already exists.
type worksheetRow struct {
	ID    string `json:"id"`
	URL   string `json:"url"`
	Title string `json:"title"`
	// Head and Mid are two windows into the page's main content: the opening,
	// where a posting states its role, and a middle slice, which separates a real
	// posting body from a page whose first screen is navigation.
	Head string `json:"head"`
	Mid  string `json:"mid"`
	// URLsTotal / URLsSameHost / URLsJoblike count outbound links. A dense set of
	// same-host job-like links is the hub signal a labeler judges on -- a link
	// count, never structured data.
	URLsTotal    int `json:"urls_total"`
	URLsSameHost int `json:"urls_same_host"`
	URLsJoblike  int `json:"urls_joblike"`
}

const (
	worksheetHeadRunes = 1000
	worksheetMidRunes  = 600
)

// jobLikeSegments are the path tokens that make a link look like a job link. They
// are a HUB signal for the labeler's link counts only, and deliberately have no
// relationship to the gate's own posting-path predicate -- nothing here may feed a
// production decision.
var jobLikeSegments = []string{"job", "career", "karriere", "stelle", "position", "vacanc"}

// worksheetFor renders one row's labeling view. hostname is the row URL's
// hostname; a row whose URL does not parse yields zero same-host counts rather
// than an error, since the worksheet is a reading aid.
func worksheetFor(row goldRow) worksheetRow {
	hostname := ""
	if u, err := crawler.NewURL(row.URL); err == nil {
		hostname = u.Hostname
	}

	out := worksheetRow{
		ID:    rowID(row.URL),
		URL:   row.URL,
		Title: flattenField(row.Content.Title),
		Head:  truncateRunes(flattenField(row.Content.MainContent), worksheetHeadRunes),
	}

	main := []rune(flattenField(row.Content.MainContent))
	if start := len(main) / 3; start > worksheetHeadRunes {
		end := start + worksheetMidRunes
		if end > len(main) {
			end = len(main)
		}
		out.Mid = string(main[start:end])
	}

	for _, raw := range row.Content.URLs {
		out.URLsTotal++
		u, err := crawler.NewURL(raw)
		if err != nil || hostname == "" || u.Hostname != hostname {
			continue
		}
		out.URLsSameHost++
		lower := strings.ToLower(u.RawURL)
		for _, seg := range jobLikeSegments {
			if strings.Contains(lower, seg) {
				out.URLsJoblike++
				break
			}
		}
	}
	return out
}

// extractLabelQuestion is the ONE question every Extract Gold Set label answers. It
// is shared by the confirmation sheet and the labelling tool so the two surfaces can
// never put different questions to the same human (#286).
const extractLabelQuestion = "is this page ONE Job Listing?"

// labelRubricEntry is one label as a labeller reads it: the label itself, the
// keystroke goldset-ui binds to it, and the definition both review surfaces state.
// Key is ignored by the Markdown sheet; it lives here so "d is detail" is written
// down once.
type labelRubricEntry struct {
	Label bench.ExtractLabel `json:"label"`
	Key   string             `json:"key"`
	Text  string             `json:"text"`
}

// extractLabelRubric is the labelling rubric, in the fixed order both surfaces show
// it. It is the wording renderConfirmSheet has always printed, lifted out of that
// function so goldset-ui cannot state a definition the sheet does not (#286).
var extractLabelRubric = []labelRubricEntry{
	{bench.ExtractDetail, "d", "the page IS one Job Listing: one role's responsibilities or requirements, and an apply action."},
	{bench.ExtractHubIndex, "h", "the page LISTS openings (a board root, a search result, a location or department facet). A page listing exactly one opening is still `hub-index`."},
	{bench.ExtractResidue, "r", "neither: culture, about, benefits, blog, press, contact, a cookie or login wall, a JS shell, a 404, a salary guide, a \"post a job\" form, or a withdrawn posting with no role body."},
	{bench.ExtractAmbiguous, "a", "the page genuinely does not resolve. Say what the tension is in the note."},
}

// goldSetPaths returns the substrate and review-sheet paths under dir.
func goldSetPaths(dir string) (substrate, sheet string) {
	return filepath.Join(dir, goldSetFile), filepath.Join(dir, labelsFile)
}

// expectedSheetPath returns the expected-extraction review sheet's path under dir.
func expectedSheetPath(dir string) string { return filepath.Join(dir, expectedFile) }

// sheetWrite returns the staged write for the labels review sheet rendered from rows.
func sheetWrite(path string, rows []goldRow) fileWrite {
	return fileWrite{Path: path, Perm: goldSetPerm, Write: writeBytes(renderSheet(rows))}
}

// expectedWrite returns the staged write for the expected-extraction review sheet
// rendered from rows.
func expectedWrite(path string, rows []goldRow) fileWrite {
	return fileWrite{Path: path, Perm: goldSetPerm, Write: writeBytes(renderExpectedSheet(rows))}
}

// writeGoldSetFiles writes the substrate and BOTH review sheets under dir from one
// merged slice, staging all three before any of them is renamed into place. The three
// are rendered from the same rows and a reader is entitled to assume they agree --
// ADR-0048's guard asserts the substrate and labels.tsv name the same confirmers -- so
// a failure must leave all three on their previous version rather than two on the new
// one (#283).
func writeGoldSetFiles(dir string, rows []goldRow) error {
	substrate, sheet := goldSetPaths(dir)
	return atomicWriteAll(
		goldSetWrite(substrate, rows),
		sheetWrite(sheet, rows),
		expectedWrite(expectedSheetPath(dir), rows),
	)
}
