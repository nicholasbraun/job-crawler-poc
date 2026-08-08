package main

import (
	"path/filepath"
	"testing"

	"github.com/nicholasbraun/job-crawler-poc/cmd/llmbench/bench"
)

// extractGoldSetFalseDrops is the LEDGER OF NAMED, ARGUED EXCEPTIONS the false-drop
// guard runs against (#264). It is NOT a budget.
//
// The distinction is the whole design. A budget says "up to N drops are tolerated",
// and a new drop inside N passes silently -- which is what #257 rejects. This names
// thirteen specific pages, each with the argument for tolerating it and the rung
// that causes it. Any other page the rule drops fails the build by name. The hard
// zero is therefore in force on every page on the web except these thirteen, each of
// which ADR-0044 writes up individually.
//
// It may only SHRINK. A page the rule stops dropping must be struck from this list
// (the guard fails until it is), so the exception set is a ratchet that falls and
// never rises.
//
// TWO OF THE THIRTEEN ARE NOT THIS RUNG'S. hiring.cafe and jobs.blooloop.com are
// dropped identically by the pre-Positive-Evidence blanket accept: a reject rung
// resolves them first. #257 lists reject-rung tuning as out of scope, so they are
// recorded under RungReject and the guard checks that attribution every run.
//
// ELEVEN REST ON LABELS NO HUMAN HAS CONFIRMED. ADR-0043 permits a hard zero to be
// ARGUED only on human-confirmed labels, because the Boundary Stratum is exactly
// where an LLM labeller is least reliable. Each entry therefore records the
// confirmation state it was written at, and the guard fails the moment that state
// changes -- so a confirmation pass re-opens each exception rather than inheriting
// it. When the boundary is fully confirmed, the guard's hard zero covers it
// automatically.
var extractGoldSetFalseDrops = []bench.KnownFalseDrop{
	{
		URL:       "https://hiring.cafe/job/softwareentwickler-m-w-d-devops-level-4-anu-befr-24-monate-1-pos-1jbf2uisealzprc5",
		Rung:      bench.RungReject,
		Confirmed: true,
		Reason: "an 8-link \"similar jobs\" rail saturates ExtractJobLinkSaturationCount (5), which drops the page " +
			"before Positive Evidence is consulted; crawl_definition.go already records 5 as under-evidenced and the " +
			"first reject signal to raise, and #257 puts reject-rung tuning out of scope",
	},
	{
		URL:       "https://jobs.blooloop.com/jobs/554707184-chief-of-development-at-the-american-lgbtq-museum",
		Rung:      bench.RungReject,
		Confirmed: false,
		Reason: "a sector Aggregator's posting page behind a saturated rail of sibling postings, dropped identically " +
			"by the blanket accept; the same reject rung as hiring.cafe, and out of #257's scope for the same reason",
	},
	{
		URL:       "http://www.observermedia.com/co-events-intern",
		Rung:      bench.RungPositiveEvidence,
		Confirmed: false,
		Reason: "a bare role slug on a company domain: prose with almost no section headings, JSON-LD that is " +
			"WebSite/Organization only, and a title that is just the role name -- nothing in URL, structure or " +
			"vocabulary separates it from an \"about our team\" page",
	},
	{
		URL:       "https://observermedia.com/freelance-reporter-philanthropy-and-wealth",
		Rung:      bench.RungPositiveEvidence,
		Confirmed: false,
		Reason:    "the second of three observermedia.com bare role slugs; same shape, same absence of any mark",
	},
	{
		URL:       "https://www.observermedia.com/freelance-editorial-research-analyst",
		Rung:      bench.RungPositiveEvidence,
		Confirmed: false,
		Reason:    "the third of three observermedia.com bare role slugs; same shape, same absence of any mark",
	},
	{
		URL:       "https://www.biochemie.med.fau.de/ta-am-lehrstuhl-fuer-biochemie-und-pathobiochemie",
		Rung:      bench.RungPositiveEvidence,
		Confirmed: false,
		Reason: "recoverable only by dropping the strong vocabulary threshold to two or three groups, which " +
			"re-admits massinc.org/about-us/career-opportunities and ifes.uni-hannover.de/eev/hiwi -- rows a blind " +
			"human re-read had just taken OUT of detail (ADR-0044)",
	},
	{
		URL:       "https://www.biochemie.med.fau.de/wissenschaftlichen-mitarbeiter-in-postdoktorand",
		Rung:      bench.RungPositiveEvidence,
		Confirmed: false,
		Reason:    "the second biochemie.med.fau.de posting; same shape, and recoverable only by the same rejected threshold move",
	},
	{
		URL:       "https://abc7.com/12393058",
		Rung:      bench.RungPositiveEvidence,
		Confirmed: false,
		Reason: "a bare numeric id on a newsroom domain: no job word anywhere in the URL, and recoverable only by " +
			"the vocabulary threshold move ADR-0044 measured and rejected",
	},
	{
		URL:       "https://theygsgroup.com/2026/07/28/content-licensing-sales-associate",
		Rung:      bench.RungPositiveEvidence,
		Confirmed: false,
		Reason: "a dated editorial path, which is evidence of a blog post rather than a posting; reading /YYYY/MM/DD/ " +
			"as a posting shape was measured at +2 postings for +3 leaks and a higher stream call rate, and rejected",
	},
	{
		URL:       "https://ecomat-bremen.de/professur-w-m-d-fuer-das-fachgebiet-software-systems-engineering-schwerpunkt-raumfahrt",
		Rung:      bench.RungPositiveEvidence,
		Confirmed: false,
		Reason: "its (w/m/d) role designation sits in the URL slug rather than the Title, and reading a gender " +
			"designation out of the slug was measured at +1 posting for +3 leaks (1:3) and rejected",
	},
	{
		URL:       "https://jobicco.tu-braunschweig.de/de/1763",
		Rung:      bench.RungPositiveEvidence,
		Confirmed: false,
		Reason: "an 846-character body whose only posting marker is the heading \"Beschreibung des Jobs\"; reaching it " +
			"needs the host SUBSTRING test, which was measured at +8 postings for +40 leaks and rejected outright",
	},
	{
		URL:       "https://duckwallfruit.com/2024/04/10/tecnico-en-refrigeracion-de-amoniaco?lang=es",
		Rung:      bench.RungPositiveEvidence,
		Confirmed: false,
		Reason: "a SPANISH-language posting: every text mark this rung reads is English or German, and a Spanish " +
			"phrase set cannot be validated on one row, so it is a follow-up rather than a guess",
	},
	{
		URL:       "https://www.liepin.com/a/78183959.shtml",
		Rung:      bench.RungPositiveEvidence,
		Confirmed: false,
		Reason: "a CHINESE-language posting, where the text marks cannot reach at all for the same reason; separately " +
			"liepin.com is a large Chinese Aggregator and probably belongs on the Aggregator denylist, a different mechanism",
	},
}

// guardRows replays the Extract Gate over the committed gold set TWICE -- once under
// the Positive Evidence rule, once under the blanket accept it replaces -- and zips
// the two into the rows the guard folds. The second replay is what makes rung
// attribution a measurement rather than a claim: the two configs differ in exactly
// one field, so a page both drop was resolved by a reject rung.
//
// It reads the WHOLE labelled file, both strata. Unlike every weighted number in
// this package that is deliberate: a stream estimate is only meaningful over the
// random stratum, but a false-drop anywhere is a Job Listing the extractor never
// sees.
func guardRows(t *testing.T) []bench.FalseDropRow {
	t.Helper()
	path := filepath.Join("extract-goldset", goldSetFile)

	rule, _, err := replayCaptured(path, boundaryCandidateConfig())
	if err != nil {
		t.Fatalf("replayCaptured under the Positive Evidence rule: %v", err)
	}
	baseline, _, err := replayCaptured(path, boundaryBaselineConfig())
	if err != nil {
		t.Fatalf("replayCaptured under the blanket accept: %v", err)
	}
	if len(rule) != len(baseline) {
		t.Fatalf("the two replays scored %d and %d rows of the same file; the guard cannot attribute a drop it cannot pair", len(rule), len(baseline))
	}

	rows := make([]bench.FalseDropRow, 0, len(rule))
	for i, row := range rule {
		if row.URL != baseline[i].URL {
			t.Fatalf("row %d is %q under the rule and %q under the baseline; the replays are not aligned", i, row.URL, baseline[i].URL)
		}
		rows = append(rows, bench.FalseDropRow{
			URL:       row.URL,
			Label:     row.Label,
			Extract:   row.Extract,
			Baseline:  baseline[i].Extract,
			Confirmed: row.Confirmed,
		})
	}
	return rows
}

// TestExtractGoldSetFalseDropGuard is the merge gate (#264): a real Job Listing the
// Extract Gate throws away fails the build, by name. It runs under the standard
// `go test` command with no network, no model and no Docker, so CI enforces it on
// every change rather than relying on somebody remembering a benchmark verb.
//
// It scores the RULE (boundaryCandidateConfig, which sets RequirePositiveEvidence
// explicitly) rather than whatever the shipped default happens to be, so the guard
// says the same thing before and after the default flips and a bisect across that
// commit stays clean.
//
// A false-drop is the one irrecoverable failure in this design: no listing means the
// Collection Cycle never seeds the page as visited, so the walk re-reaches and
// re-drops it every Cycle, forever, and nothing in production moves. A false ACCEPT
// self-heals through the refetch lane, which is why precision and cost are reported
// next door with no threshold at all.
func TestExtractGoldSetFalseDropGuard(t *testing.T) {
	audit := bench.AuditFalseDrops(guardRows(t), extractGoldSetFalseDrops)

	for _, drop := range audit.Unrecorded {
		t.Errorf("FALSE-DROP %s -- the Extract Gate drops a real Job Listing the ledger does not name. "+
			"Read the page: if it IS one Job Listing, widen the rung or fix the reject rung that swallowed it. "+
			"Do not add it to extractGoldSetFalseDrops without an argument for why it cannot be recovered.", drop)
	}
	for _, url := range audit.Recovered {
		t.Errorf("RECOVERED %s -- the rule no longer drops this page, so its ledger entry is stale. "+
			"Delete the entry: the exception list may only shrink, and a recovery left standing is unearned "+
			"headroom for the next drop.", url)
	}
	for _, drop := range audit.Misattributed {
		t.Errorf("MISATTRIBUTED %s -- the ledger records the other rung. A drop that has moved between the reject "+
			"rungs and Positive Evidence means one of them has changed; find out which before re-filing the entry.", drop)
	}
	for _, drop := range audit.Restanding {
		t.Errorf("RESTANDING %s -- this label's human-confirmation state has changed since the exception was "+
			"written, so the argument no longer stands where it was made (ADR-0043). Re-read the page and re-argue "+
			"the entry, or recover it.", drop)
	}
	for _, url := range audit.Unargued {
		t.Errorf("UNARGUED %s -- a ledger entry with no Reason. An exception nobody can read is one nobody can "+
			"withdraw; state in one line why the page cannot be recovered.", url)
	}
	for _, url := range audit.Duplicated {
		t.Errorf("DUPLICATED %s -- the ledger names this page more than once, so which argument is in force is "+
			"unclear. Keep one entry.", url)
	}

	// The standing of the whole ledger under -v, so a reader can see what is tolerated
	// without re-deriving it. Each drop is marked with whether the ledger names it,
	// because an unnamed drop printed beside the tolerated ones would read as tolerated
	// too -- and it is the opposite: it is what just failed the build.
	named := map[string]struct{}{}
	for _, entry := range extractGoldSetFalseDrops {
		named[entry.URL] = struct{}{}
	}
	t.Logf("false-drops: %d (%d human-confirmed, %d LLM-proposed and unconfirmed); ledger entries: %d",
		len(audit.Drops), audit.Confirmed, audit.Unconfirmed, len(extractGoldSetFalseDrops))
	for _, drop := range audit.Drops {
		standing := "TOLERATED"
		if _, ok := named[drop.URL]; !ok {
			standing = "UNRECORDED"
		}
		t.Logf("  %-10s %s", standing, drop)
	}
}

// TestExtractGoldSetMetricsAreReportedWithoutThresholds reports what the Positive
// Evidence rule costs and buys, and asserts NOTHING about any of it.
//
// That is ADR-0020's contract, not laziness. Precision, recall, extract-call rate
// and projected spend are weighted estimates whose value depends on the stream's
// composition, so a threshold on any of them would encode one week's mix of the web
// as a correctness property. The single fatal condition over this gold set is the
// false-drop guard above.
//
// The stream figures come from the RANDOM stratum alone and the boundary block from
// the Boundary Stratum alone, never pooled: one is a weighted sample of the stream
// and the other a census of a disagreement set, so mixing them would bias both.
// TestBoundaryStratumNeverEntersTheStreamEstimate proves that separation as a
// property; this test consumes it.
func TestExtractGoldSetMetricsAreReportedWithoutThresholds(t *testing.T) {
	rows, _, err := replayCaptured(filepath.Join("extract-goldset", goldSetFile), boundaryCandidateConfig())
	if err != nil {
		t.Fatalf("replayCaptured: %v", err)
	}

	stream := bench.ScoreExtractStream(verdictRowsIn(rows, stratumRandom))
	t.Logf("stream-weighted estimates (random stratum, n=%d, effective n=%.1f)", stream.Rows, stream.EffectiveN)
	for _, label := range bench.AllExtractLabels {
		t.Logf("  composition %-10s %.4f (n=%d)", label, stream.Composition[label], stream.Counts[label])
	}
	t.Logf("  extract-call-rate %.4f (share of today's calls this config still makes)", stream.ExtractCallRate)
	t.Logf("  precision         %.4f (of what it extracts, the share that is a real posting)", stream.Precision)
	t.Logf("  recall            %.4f (of today's real postings, the share it keeps)", stream.Recall)
	t.Logf("  projected spend   %.4fx today's extract bill (%s)", stream.ExtractCallRate, projectedSpendBasis)

	boundary := bench.ScoreExtractBoundary(boundaryRowsOf(rows))
	t.Logf("boundary stratum (n=%d, unweighted census of a disagreement set -- estimates nothing)", boundary.Rows)
	t.Logf("  extracted %d / skipped %d, false-drops %d, ambiguous-skipped %d, confirmed %d of %d",
		boundary.Extracted, boundary.Skipped, len(boundary.FalseDrops), boundary.AmbiguousSkipped,
		boundary.Confirmed, boundary.Rows)
}
