package bench_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/nicholasbraun/job-crawler-poc/cmd/llmbench/bench"
)

// guardRow builds one gold-set row as the guard reads it. Weight is absent on
// purpose: the guard is unweighted, and a test that had to supply weights would be
// testing something else.
func guardRow(url string, label bench.ExtractLabel, extract, baseline, confirmed bool) bench.FalseDropRow {
	return bench.FalseDropRow{URL: url, Label: label, Extract: extract, Baseline: baseline, Confirmed: confirmed}
}

// argued builds a ledger entry with a reason, which is the only shape a reviewable
// exception has.
func argued(url string, rung bench.FalseDropRung, confirmed bool) bench.KnownFalseDrop {
	return bench.KnownFalseDrop{URL: url, Rung: rung, Confirmed: confirmed, Reason: "read and written up"}
}

// TestAuditFalseDropsPassesWhenTheLedgerNamesEveryDrop is the green case: the two
// pages the rule drops are both named, under the right rung and at the right
// standing, so nothing is fatal -- and both are still REPORTED, because a tolerated
// drop is still a Job Listing nobody extracts.
func TestAuditFalseDropsPassesWhenTheLedgerNamesEveryDrop(t *testing.T) {
	audit := bench.AuditFalseDrops(
		[]bench.FalseDropRow{
			guardRow("https://a.test/posting", bench.ExtractDetail, false, true, false),
			guardRow("https://b.test/posting", bench.ExtractDetail, false, false, true),
			guardRow("https://c.test/posting", bench.ExtractDetail, true, true, false),
			guardRow("https://d.test/hub", bench.ExtractHubIndex, false, true, false),
		},
		[]bench.KnownFalseDrop{
			argued("https://a.test/posting", bench.RungPositiveEvidence, false),
			argued("https://b.test/posting", bench.RungReject, true),
		},
	)

	if audit.Failed() {
		t.Errorf("Failed() = true, want false; audit %+v", audit)
	}
	wantDrops := []bench.FalseDrop{
		{URL: "https://a.test/posting", Rung: bench.RungPositiveEvidence, Confirmed: false},
		{URL: "https://b.test/posting", Rung: bench.RungReject, Confirmed: true},
	}
	if !reflect.DeepEqual(audit.Drops, wantDrops) {
		t.Errorf("Drops = %+v, want %+v (input order, both rungs)", audit.Drops, wantDrops)
	}
	if audit.Confirmed != 1 || audit.Unconfirmed != 1 {
		t.Errorf("confirmed %d / unconfirmed %d, want 1 / 1", audit.Confirmed, audit.Unconfirmed)
	}
}

// TestAuditFalseDropsFailsOnADropTheLedgerDoesNotName is the hard zero, and the
// acceptance criterion that the guard NAMES the offending URL: a reader of a CI log
// must be able to open the page without re-running anything.
func TestAuditFalseDropsFailsOnADropTheLedgerDoesNotName(t *testing.T) {
	audit := bench.AuditFalseDrops(
		[]bench.FalseDropRow{
			guardRow("https://named.test/posting", bench.ExtractDetail, false, true, false),
			guardRow("https://new.test/posting", bench.ExtractDetail, false, true, false),
		},
		[]bench.KnownFalseDrop{argued("https://named.test/posting", bench.RungPositiveEvidence, false)},
	)

	if !audit.Failed() {
		t.Fatal("Failed() = false, want true: a drop the ledger does not name must fail the build")
	}
	if len(audit.Unrecorded) != 1 || audit.Unrecorded[0].URL != "https://new.test/posting" {
		t.Fatalf("Unrecorded = %+v, want exactly the unnamed page", audit.Unrecorded)
	}
	if audit.Unrecorded[0].Rung != bench.RungPositiveEvidence {
		t.Errorf("Unrecorded rung = %q, want %q: only the rule drops it", audit.Unrecorded[0].Rung, bench.RungPositiveEvidence)
	}
	if got := audit.Unrecorded[0].String(); !strings.Contains(got, "https://new.test/posting") {
		t.Errorf("String() = %q, must name the URL", got)
	}
}

// TestAuditFalseDropsFailsWhenALedgerEntryIsRecovered keeps the ratchet falling
// only: a page the rule now keeps must be struck from the ledger rather than left
// standing as unearned headroom for the next drop.
func TestAuditFalseDropsFailsWhenALedgerEntryIsRecovered(t *testing.T) {
	audit := bench.AuditFalseDrops(
		[]bench.FalseDropRow{guardRow("https://a.test/posting", bench.ExtractDetail, true, true, false)},
		[]bench.KnownFalseDrop{argued("https://a.test/posting", bench.RungPositiveEvidence, false)},
	)

	if !audit.Failed() {
		t.Fatal("Failed() = false, want true: a recovered page left in the ledger must fail")
	}
	if !reflect.DeepEqual(audit.Recovered, []string{"https://a.test/posting"}) {
		t.Errorf("Recovered = %v, want the recovered page", audit.Recovered)
	}
	if len(audit.Drops) != 0 {
		t.Errorf("Drops = %+v, want none: the rule keeps the page", audit.Drops)
	}
}

// TestAuditFalseDropsFailsWhenADropChangesRung is what makes the rung attribution a
// checked fact rather than a claim in an ADR. A page recorded as a reject-rung drop
// that becomes a Positive Evidence drop -- or the reverse, a reject rung newly
// swallowing a page the rule used to own -- fails.
func TestAuditFalseDropsFailsWhenADropChangesRung(t *testing.T) {
	for name, tt := range map[string]struct {
		baseline bool
		recorded bench.FalseDropRung
		want     bench.FalseDropRung
	}{
		"a reject-rung entry is now the rule's":   {baseline: true, recorded: bench.RungReject, want: bench.RungPositiveEvidence},
		"the rule's entry is now a reject rung's": {baseline: false, recorded: bench.RungPositiveEvidence, want: bench.RungReject},
	} {
		t.Run(name, func(t *testing.T) {
			audit := bench.AuditFalseDrops(
				[]bench.FalseDropRow{guardRow("https://a.test/posting", bench.ExtractDetail, false, tt.baseline, false)},
				[]bench.KnownFalseDrop{argued("https://a.test/posting", tt.recorded, false)},
			)
			if !audit.Failed() {
				t.Fatal("Failed() = false, want true: the ledger attributes the drop to the wrong rung")
			}
			if len(audit.Misattributed) != 1 || audit.Misattributed[0].Rung != tt.want {
				t.Errorf("Misattributed = %+v, want one drop on the %q rung", audit.Misattributed, tt.want)
			}
			if len(audit.Unrecorded) != 0 {
				t.Errorf("Unrecorded = %+v, want none: the ledger does name the page", audit.Unrecorded)
			}
		})
	}
}

// TestAuditFalseDropsFailsWhenALabelGainsAConfirmation is how the guard arms
// itself. ADR-0043 permits a hard zero to be argued only on human-confirmed labels,
// so an exception written against an unconfirmed label loses its footing the moment
// a human reads that page -- and must be re-argued rather than inherited.
func TestAuditFalseDropsFailsWhenALabelGainsAConfirmation(t *testing.T) {
	audit := bench.AuditFalseDrops(
		[]bench.FalseDropRow{guardRow("https://a.test/posting", bench.ExtractDetail, false, true, true)},
		[]bench.KnownFalseDrop{argued("https://a.test/posting", bench.RungPositiveEvidence, false)},
	)

	if !audit.Failed() {
		t.Fatal("Failed() = false, want true: the label behind the exception has been confirmed since it was written")
	}
	if len(audit.Restanding) != 1 || audit.Restanding[0].URL != "https://a.test/posting" {
		t.Errorf("Restanding = %+v, want the newly confirmed page", audit.Restanding)
	}
	if audit.Confirmed != 1 {
		t.Errorf("Confirmed = %d, want 1", audit.Confirmed)
	}
}

// TestAuditFalseDropsFailsOnAMalformedLedger covers the two faults in the ledger
// itself: an exception with no argument cannot be reviewed or withdrawn, and a URL
// named twice leaves it unclear which argument is in force.
func TestAuditFalseDropsFailsOnAMalformedLedger(t *testing.T) {
	rows := []bench.FalseDropRow{guardRow("https://a.test/posting", bench.ExtractDetail, false, true, false)}

	unargued := bench.AuditFalseDrops(rows, []bench.KnownFalseDrop{
		{URL: "https://a.test/posting", Rung: bench.RungPositiveEvidence},
	})
	if !unargued.Failed() {
		t.Fatal("Failed() = false, want true: an exception with no stated reason")
	}
	if !reflect.DeepEqual(unargued.Unargued, []string{"https://a.test/posting"}) {
		t.Errorf("Unargued = %v, want the unargued entry", unargued.Unargued)
	}

	duplicated := bench.AuditFalseDrops(rows, []bench.KnownFalseDrop{
		argued("https://a.test/posting", bench.RungPositiveEvidence, false),
		argued("https://a.test/posting", bench.RungReject, false),
	})
	if !duplicated.Failed() {
		t.Fatal("Failed() = false, want true: the ledger names one URL twice")
	}
	if !reflect.DeepEqual(duplicated.Duplicated, []string{"https://a.test/posting"}) {
		t.Errorf("Duplicated = %v, want the duplicated entry", duplicated.Duplicated)
	}
}

// TestAuditFalseDropsNeverCountsAnAmbiguousRowAsADrop is ExtractLabel.Scored()'s
// contract, asserted where it decides an exit code: a page a review could not settle
// must never redden a build in either direction, because that would let the
// labeller's shrug pick the answer.
func TestAuditFalseDropsNeverCountsAnAmbiguousRowAsADrop(t *testing.T) {
	for name, extract := range map[string]bool{"skipped": false, "extracted": true} {
		t.Run(name, func(t *testing.T) {
			audit := bench.AuditFalseDrops(
				[]bench.FalseDropRow{guardRow("https://a.test/unclear", bench.ExtractAmbiguous, extract, true, false)},
				nil,
			)
			if audit.Failed() {
				t.Errorf("Failed() = true, want false; audit %+v", audit)
			}
			if len(audit.Drops) != 0 {
				t.Errorf("Drops = %+v, want none: an ambiguous page is never a false-drop", audit.Drops)
			}
		})
	}
}

// TestAuditFalseDropsIgnoresTheNonPostingsTheRungIsFor guards against the opposite
// mistake: a hub or a residue page the rule sheds is the rung WORKING, and counting
// it would invert the guard into one that forbids the gate from gating.
func TestAuditFalseDropsIgnoresTheNonPostingsTheRungIsFor(t *testing.T) {
	audit := bench.AuditFalseDrops(
		[]bench.FalseDropRow{
			guardRow("https://a.test/careers", bench.ExtractHubIndex, false, true, false),
			guardRow("https://b.test/about-us", bench.ExtractResidue, false, true, false),
		},
		nil,
	)
	if audit.Failed() {
		t.Errorf("Failed() = true, want false; audit %+v", audit)
	}
	if len(audit.Drops) != 0 {
		t.Errorf("Drops = %+v, want none: shedding a non-posting is the rung working", audit.Drops)
	}
}

// TestAuditFalseDropsInitializesEverySlice keeps an empty result readable and
// diffable, mirroring ScoreExtract's convention so tests can use
// reflect.DeepEqual.
func TestAuditFalseDropsInitializesEverySlice(t *testing.T) {
	audit := bench.AuditFalseDrops(nil, nil)
	if audit.Drops == nil || audit.Unrecorded == nil || audit.Recovered == nil ||
		audit.Misattributed == nil || audit.Restanding == nil || audit.Unargued == nil || audit.Duplicated == nil {
		t.Errorf("a slice came back nil: %+v", audit)
	}
	if audit.Failed() {
		t.Error("Failed() = true over no rows and no ledger, want false")
	}
}
