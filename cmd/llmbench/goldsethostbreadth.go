// This file is the Extract Gold Set's HOST-BREADTH drawing (ADR-0043, ADR-0050): a
// cluster sample of the HOSTS in one closed capture window, each selected host
// represented by exactly one of its pages. It exists because the Posting Score's
// cross-validation is host-grouped and its leakage guard is keyed on host words
// (ADR-0049), so both read HOSTS rather than rows -- and the fit's binding shortage is
// hosts, not pages.
//
// It reads NOTHING the previous fit produced: not the Posting Score, not
// pagegate.VetoThreshold, not a band derived from either. That is the point. A drawing
// banded on a threshold the last refit chose selects its own training set from its own
// beliefs, which is the circular selection docs/improving-the-posting-score.md names as
// the loop's characteristic failure. Stratifying on the LIVE EXTRACTOR VERDICT is an
// outside fact about the frame and is explicitly permitted (ADR-0049), and it is the
// only thing this drawing conditions on.
//
// Like every drawing here it produces no crawler behaviour and touches no network or
// model.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/parser"
)

// hostBreadthSamplePlan is the host-breadth drawing's design: quotas over the two
// verdict cells, and nothing else. Like samplePlan and randomSamplePlan it is a package
// var rather than a flag, because the design is an argument rather than a knob.
//
// The two quotas are unequal on purpose. A `detail` page is what the fit is short of,
// and real Job Listings concentrate in the accept half -- the live extractor's
// precision there is 0.454 against human labels (ADR-0049), against far less among its
// abstains. The abstain cell still gets 400, because a fit shown only accepts learns
// what a posting looks like and nothing about what a hub or a benefits page looks like.
//
// These are quotas over HOSTS, not pages: the sampling frame each is applied to has
// already been reduced to one representative page per host (hostRepresentatives), so
// 500 accept rows are 500 distinct hosts.
var hostBreadthSamplePlan = []cellPlan{
	{stratumHostBreadth, true, 500},
	{stratumHostBreadth, false, 400},
}

// shippingRenderer names the renderer today's parser writes Content.MainContent with.
// It is read off a parser INSTANCE rather than written down, so the drawing's default
// frame follows the PARSE_STRUCTURAL_RENDERING kill switch's own default instead of a
// string that goes stale the day it flips.
func shippingRenderer() string { return parser.NewHTMLParser().RendererID() }

// framedByRenderer narrows a scan to the candidates stamped with renderer, returning the
// narrowed scan and how many it dropped. ADR-0046 is the reason it exists: a captured
// page is evidence about the bytes the gate will later see, so a window a crawl flipped
// the renderer part-way through holds rows from two parsers, and a drawing that mixed
// them would be a sample of no single thing.
//
// -since is the fence every earlier drawing used for this, and it cannot do the job
// here: it is a floor, and the renderer this window flipped TO is the one at its end. A
// drawing needs a fence on the stamp itself.
//
// A candidate carrying NO stamp is dropped too, and deliberately: an unstamped row
// predates #281, which makes its renderer unknown rather than equal to anything, and a
// drawing may not silently assume the answer.
func framedByRenderer(scan captureScan, renderer string) (captureScan, int) {
	out := scan
	out.Candidates = make([]candidate, 0, len(scan.Candidates))
	dropped := 0
	for _, c := range scan.Candidates {
		if c.Renderer != renderer {
			dropped++
			continue
		}
		out.Candidates = append(out.Candidates, c)
	}
	return out, dropped
}

// hostRepresentatives reduces candidates to at most one page per (hostname, verdict),
// keeping the page with the lowest seededHash -- the same deterministic order every
// other drawing selects in, so the representative is a fixed function of (seed, host)
// and never of a page's position in the capture.
//
// The cluster is (hostname, verdict) and not hostname alone, so a host that published
// both an accepted and an abstained page can represent itself once in each cell. That
// keeps the two cells independent, which is what lets each carry its own inclusion
// probability; pooling them would make one cell's quota silently change the other's
// weights. hostname is the key rather than eTLD+1 because hostname is exactly what the
// fit's cross-validation groups on (trainscorerfit.go's scorerSample.Host) -- sampling
// on a coarser key would leave the fold assignment seeing repeats this drawing thought
// it had spent a row to avoid.
//
// A candidate whose URL the domain cannot parse is dropped and counted. scanCapture
// already rejected those, so this is a guard against a future caller rather than a
// path the verb takes.
func hostRepresentatives(cands []candidate, seed string) (reps []candidate, unparseable int) {
	type hostCell struct {
		Host    string
		Verdict bool
	}
	groups := map[hostCell][]candidate{}
	order := []hostCell{}
	for _, c := range cands {
		u, err := crawler.NewURL(c.URL)
		if err != nil {
			unparseable++
			continue
		}
		key := hostCell{Host: u.Hostname, Verdict: c.Verdict}
		if _, seen := groups[key]; !seen {
			order = append(order, key)
		}
		groups[key] = append(groups[key], c)
	}

	// Iterate the FIRST-SEEN order rather than ranging the map, so the result is
	// reproducible; the caller sorts by URL anyway, but a selection built on map order
	// is a byte-reproducibility bug waiting for the first host that ties on hash.
	reps = make([]candidate, 0, len(order))
	for _, key := range order {
		reps = append(reps, takeByHash(groups[key], seed, 1)...)
	}
	sort.Slice(reps, func(i, j int) bool { return reps[i].URL < reps[j].URL })
	return reps, unparseable
}

// hostBreadthSelection draws the sample: min(quota, population) hosts from each verdict
// cell in deterministic hash order, each cell's rows weighted by the inverse of their
// selection probability, normalized so the drawing's weights sum to its own row count.
//
//	w_c = (N_c / n_c) x (n / N)
//
// exactly the arithmetic the Learned Veto's bands use (stratifiedSelection), and for the
// same reason: this population is ENUMERATED by the scan, so its selection probabilities
// are known exactly and there is no capped stream to reconstruct a verdict share from.
// weightsFor is deliberately not used -- it exists to undo the tap's per-verdict caps on
// a drawing whose frame is a capped stream sample, and this drawing's frame is not one.
//
// What a weighted count over these rows estimates is "the hosts in this window, one page
// each" -- never the stream of pages, in which a host publishing 900 pages counts 900
// times and here counts once. Like the Boundary Strata its rows therefore stay out of
// the weighted stream scorecard.
func hostBreadthSelection(reps []candidate, plan []cellPlan, seed string) (selection, error) {
	quotas := map[bool]int{}
	for _, p := range plan {
		if p.Stratum != stratumHostBreadth {
			return selection{}, fmt.Errorf("host-breadth plan carries cell %s/%v, which is not this drawing's stratum", p.Stratum, p.Verdict)
		}
		if _, dup := quotas[p.Verdict]; dup {
			return selection{}, fmt.Errorf("host-breadth plan: duplicate verdict cell %v", p.Verdict)
		}
		quotas[p.Verdict] = p.N
	}
	if len(quotas) != 2 {
		return selection{}, fmt.Errorf("host-breadth plan covers %d verdict cells, want both", len(quotas))
	}

	cells := map[bool][]candidate{}
	for _, c := range reps {
		cells[c.Verdict] = append(cells[c.Verdict], c)
	}

	taken := map[bool][]candidate{}
	population, sampled := 0, 0
	for _, verdict := range []bool{true, false} {
		taken[verdict] = takeByHash(cells[verdict], seed, quotas[verdict])
		population += len(cells[verdict])
		sampled += len(taken[verdict])
	}
	if sampled == 0 {
		return selection{}, fmt.Errorf("the quotas draw no row at all from a %d-host frame; there is nothing to weight", population)
	}

	sel := selection{}
	chosen := []candidate{}
	for _, verdict := range []bool{true, false} {
		n := len(taken[verdict])
		weight := 0.0
		if n > 0 {
			weight = (float64(len(cells[verdict])) / float64(n)) * (float64(sampled) / float64(population))
		}
		// An empty cell still emits its accounting line, so a cell the frame has no
		// hosts in is VISIBLE in the summary rather than absent from it.
		sel.Cells = append(sel.Cells, cellResult{
			Key:        cellKey{Stratum: stratumHostBreadth, Verdict: verdict},
			Population: len(cells[verdict]),
			Sampled:    n,
			Weight:     weight,
		})
		chosen = append(chosen, taken[verdict]...)
	}
	sort.Slice(chosen, func(i, j int) bool { return chosen[i].URL < chosen[j].URL })
	sel.Chosen = withStratum(chosen, stratumHostBreadth)
	return sel, nil
}

// hostBreadthSummary is one run's account, in the order printHostBreadthSummary reads
// it. It carries the whole arithmetic the Extract Gold Set's README records for a
// drawing, so an operator never has to reconstruct a number from a log line.
type hostBreadthSummary struct {
	Scan             captureScan
	Framed           captureScan
	Reps             []candidate
	Sel              selection
	Drawn            []goldRow
	Merged           []goldRow
	OutOfFrame       int
	Renderer         string
	OtherRenderer    int
	AlreadyCommitted int
	Unparseable      int
	Substrate        string
	Drew             bool
}

// runGoldSetSampleHostBreadth is the goldset-sample-host-breadth verb: it scans a
// capture, narrows it to the faithful frame at or after -since, reduces that frame to
// one page per (host, verdict), and either reports what the quotas would take or
// APPENDS the sample to the Extract Gold Set as the host-breadth stratum.
//
// Report mode is the default for the same reason it is the veto boundary's: a draw
// changes the fitted weights and owes a human confirmation per row, so the operator
// reads the frame's host count before spending either.
//
// Like every drawing here it is drawn ONCE. A second window's rows would carry a second
// set of inclusion probabilities into one stratum, which is exactly what makes a
// weighted estimate over it unrecoverable; a fresh window is a new drawing, declared in
// code as this one was.
//
// Exit: 2 on a usage or validation error, 1 on IO, 0 otherwise. Nothing is written
// unless every check passes.
func runGoldSetSampleHostBreadth(args []string) int {
	fs := flag.NewFlagSet("goldset-sample-host-breadth", flag.ExitOnError)
	capture := fs.String("capture", "", "extract-capture JSONL written by the EXTRACT_CAPTURE_PATH tap (required; gitignored, never committed)")
	dir := fs.String("dir", defaultGoldSetDir, "directory holding the Extract Gold Set the host-breadth stratum is appended to (read only under -draw)")
	seed := fs.String("seed", defaultHostBreadthSeed, "seed for the deterministic host representative and within-cell selection; changing it is a deliberate resample")
	since := fs.String("since", "", "RFC3339 cutoff: only capture records at or after it are drawn from (required; excludes windows parsed by a superseded parser)")
	renderer := fs.String("renderer", shippingRenderer(), "renderer stamp the frame is fenced to (ADR-0046): a drawing takes ONE renderer, and a row carrying no stamp is never assumed to match")
	draw := fs.Bool("draw", false, "append the sample to the Extract Gold Set as the host-breadth stratum; the default reports the frame's host count and writes nothing")
	_ = fs.Parse(args)

	if *capture == "" || *since == "" {
		fmt.Fprintln(os.Stderr, "usage: llmbench goldset-sample-host-breadth -capture <capture.jsonl> -since <RFC3339> [-dir d] [-seed s] [-draw]")
		return 2
	}
	cutoff, err := time.Parse(time.RFC3339, *since)
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-sample-host-breadth: -since %q: %v\n", *since, err)
		return 2
	}

	scan, err := scanCapture(*capture)
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-sample-host-breadth: %v\n", err)
		return 1
	}
	framed, outOfFrame := frameSince(scan, cutoff)
	framed, otherRenderer := framedByRenderer(framed, *renderer)
	if len(framed.Candidates) == 0 {
		fmt.Fprintf(os.Stderr, "llmbench goldset-sample-host-breadth: no page in the frame carries renderer %q (%d carry another or none). Check -since and -renderer.\n",
			*renderer, otherRenderer)
		return 2
	}
	summary := hostBreadthSummary{Scan: scan, Framed: framed, OutOfFrame: outOfFrame, Renderer: *renderer, OtherRenderer: otherRenderer}

	// Report mode never opens the substrate, so its host populations are PRE-exclusion:
	// the pages earlier drawings already hold are still counted. That is the honest
	// number for "how many hosts does this window carry", which is what the mode is read
	// for; the draw reports the post-exclusion figure beside it.
	if !*draw {
		reps, unparseable := hostRepresentatives(framed.Candidates, *seed)
		sel, err := hostBreadthSelection(reps, hostBreadthSamplePlan, *seed)
		if err != nil {
			fmt.Fprintf(os.Stderr, "llmbench goldset-sample-host-breadth: %v\n", err)
			return 2
		}
		summary.Reps, summary.Sel, summary.Unparseable = reps, sel, unparseable
		printHostBreadthSummary(os.Stdout, summary)
		return 0
	}

	// The substrate MUST already exist, for the same reason the random drawing requires
	// one: this stratum extends the committed file, and silently starting a new one
	// would strand every existing label.
	substrate, _ := goldSetPaths(*dir)
	existing, err := readGoldSet(substrate)
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-sample-host-breadth: %v (this stratum extends an existing gold set; run goldset-sample first)\n", err)
		return 1
	}
	committed := map[string]struct{}{}
	for _, row := range existing {
		committed[row.URL] = struct{}{}
		if row.Stratum == stratumHostBreadth {
			fmt.Fprintf(os.Stderr, "llmbench goldset-sample-host-breadth: the substrate already holds %s rows (e.g. %s), and this drawing is drawn ONCE.\n",
				stratumHostBreadth, row.URL)
			fmt.Fprintf(os.Stderr, "  Appending a second window's sample would pool two selection designs in one stratum, "+
				"which is exactly what makes a weighted estimate over it unrecoverable. A fresh window is a new DRAWING: "+
				"declare it in code, as every drawing before it was. Nothing was written.\n")
			return 2
		}
	}

	// The exclusion happens BEFORE the host reduction, not after. A host whose only
	// low-hash page an earlier drawing already holds must fall back to its next page
	// rather than lose its representative -- excluding after the reduction would drop
	// the host from this drawing entirely and make its inclusion probability a function
	// of what a different drawing happened to sample.
	drawable, alreadyCommitted := withoutURLs(framed.Candidates, committed)
	reps, unparseable := hostRepresentatives(drawable, *seed)
	if len(reps) == 0 {
		fmt.Fprintln(os.Stderr, "llmbench goldset-sample-host-breadth: the frame carries no host the substrate does not already hold; nothing left to draw")
		return 2
	}
	sel, err := hostBreadthSelection(reps, hostBreadthSamplePlan, *seed)
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-sample-host-breadth: %v\n", err)
		return 2
	}
	drawn, err := readSelected(*capture, sel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-sample-host-breadth: %v\n", err)
		return 1
	}
	if err := validateDrawnHostBreadthRows(sel, drawn, committed); err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-sample-host-breadth: %v\n", err)
		return 2
	}

	merged := append(append([]goldRow{}, existing...), drawn...)
	if err := writeGoldSetFiles(*dir, merged); err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-sample-host-breadth: %v\n", err)
		return 1
	}

	summary.Reps, summary.Sel, summary.Drawn, summary.Merged = reps, sel, drawn, merged
	summary.AlreadyCommitted, summary.Unparseable, summary.Substrate, summary.Drew = alreadyCommitted, unparseable, substrate, true
	printHostBreadthSummary(os.Stdout, summary)
	return 0
}

// validateDrawnHostBreadthRows refuses a draw that could corrupt the substrate: a row
// already committed (one page cannot carry two drawings' incompatible weights), a
// duplicate within the draw, a row outside this drawing's stratum, TWO rows on one
// (host, verdict) -- which would mean the cluster reduction failed and the weights
// describe a population that was not sampled -- a weight the selection cannot have
// produced, or a row that arrived carrying a label.
//
// Nothing is written until it passes, so a bad draw leaves the committed file exactly
// as it was.
func validateDrawnHostBreadthRows(sel selection, drawn []goldRow, committed map[string]struct{}) error {
	drawnWeights := map[float64]struct{}{}
	total := 0.0
	for _, c := range sel.Cells {
		if c.Sampled > 0 && c.Weight > 0 {
			drawnWeights[c.Weight] = struct{}{}
			total += c.Weight * float64(c.Sampled)
		}
	}
	if delta := total - float64(len(drawn)); delta > 1e-6 || delta < -1e-6 {
		return fmt.Errorf("the draw's weights sum to %.6f over %d rows; a drawing's weights must normalize to its own row count", total, len(drawn))
	}

	type hostCell struct {
		Host    string
		Verdict bool
	}
	seen := map[string]struct{}{}
	seenHost := map[hostCell]string{}
	for _, row := range drawn {
		if _, dup := committed[row.URL]; dup {
			return fmt.Errorf("drew %q, which the substrate already carries", row.URL)
		}
		if _, dup := seen[row.URL]; dup {
			return fmt.Errorf("drew %q twice", row.URL)
		}
		seen[row.URL] = struct{}{}
		if row.Stratum != stratumHostBreadth {
			return fmt.Errorf("drew %q in stratum %q, want %q", row.URL, row.Stratum, stratumHostBreadth)
		}
		if row.Label != "" {
			return fmt.Errorf("drew %q carrying label %q; a drawing appends UNLABELLED rows", row.URL, row.Label)
		}
		u, err := crawler.NewURL(row.URL)
		if err != nil {
			return fmt.Errorf("drew %q, whose URL the domain rejects: %w", row.URL, err)
		}
		key := hostCell{Host: u.Hostname, Verdict: row.Verdict}
		if other, dup := seenHost[key]; dup {
			return fmt.Errorf("drew both %q and %q from host %q at verdict %v; this drawing samples HOSTS, one page each, "+
				"so a second page from one host means the cluster reduction failed and the weights describe a population that was not sampled",
				other, row.URL, u.Hostname, row.Verdict)
		}
		seenHost[key] = row.URL
		if _, ok := drawnWeights[row.Weight]; !ok {
			return fmt.Errorf("drew %q with weight %g, which none of the sampled cells carries; its cell was keyed wrong", row.URL, row.Weight)
		}
	}
	return nil
}

// printHostBreadthSummary writes the account an operator needs to trust the drawing:
// what was read, what was dropped and why, how many HOSTS the frame carried, the
// realized per-cell design, and where the rows landed. It is the number set the Extract
// Gold Set's README records for this drawing.
func printHostBreadthSummary(w io.Writer, s hostBreadthSummary) {
	fmt.Fprintln(w, "extract gold set host-breadth drawing")
	fmt.Fprintf(w, "  capture lines        %d\n", s.Scan.Lines)
	fmt.Fprintf(w, "  duplicate lines      %d (deduped by url, latest ts wins)\n", s.Scan.Duplicates)
	fmt.Fprintf(w, "  dropped oversized    %d (raw line > %d bytes)\n", s.Scan.Oversized, maxCandidateBytes)
	fmt.Fprintf(w, "  dropped bad url      %d\n", s.Scan.BadURL)
	fmt.Fprintf(w, "  dropped out-of-frame %d (superseded parser, or an unparseable ts)\n", s.OutOfFrame)
	fmt.Fprintf(w, "  dropped renderer     %d (stamped with another renderer, or with none -- ADR-0046)\n", s.OtherRenderer)
	fmt.Fprintf(w, "  candidate frame      %d (renderer %s)\n", len(s.Framed.Candidates), s.Renderer)
	if s.Drew {
		fmt.Fprintf(w, "  dropped committed    %d (pages the substrate already carries)\n", s.AlreadyCommitted)
	} else {
		fmt.Fprintln(w, "                       populations below are PRE-exclusion: this mode never opens the")
		fmt.Fprintln(w, "                       gold set, so the pages earlier drawings hold are still counted")
	}
	if s.Unparseable > 0 {
		fmt.Fprintf(w, "  dropped unparseable  %d (url the domain rejects)\n", s.Unparseable)
	}
	fmt.Fprintf(w, "  host representatives %d (one page per host per verdict, lowest seeded hash)\n", len(s.Reps))
	for _, c := range s.Sel.Cells {
		fmt.Fprintf(w, "  cell verdict=%-5v   hosts %5d  quota-taken %4d  weight %.4f\n", c.Key.Verdict, c.Population, c.Sampled, c.Weight)
	}
	if !s.Drew {
		fmt.Fprintf(w, "  wrote                nothing. Re-run with -draw to append %d rows.\n", len(s.Sel.Chosen))
		return
	}
	fmt.Fprintf(w, "  drawn rows           %d\n", len(s.Drawn))
	fmt.Fprintf(w, "  drawn weight sum     %.4f (must equal the drawn rows)\n", weightSum(s.Drawn))
	fmt.Fprintf(w, "  substrate rows       %d (%d existing + %d drawn)\n", len(s.Merged), len(s.Merged)-len(s.Drawn), len(s.Drawn))
	if info, err := os.Stat(s.Substrate); err == nil {
		fmt.Fprintf(w, "  wrote                %s (%d bytes)\n", s.Substrate, info.Size())
	}
}
