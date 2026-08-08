// This file is the bench package's FALSE-DROP GUARD (ADR-0020, ADR-0044, #264):
// the fatal half of the Extract Gate's scoring, separated from the descriptive
// half so the two can never be confused for one another. ScoreExtract reports
// which pages a config drops; this decides whether that is allowed to fail a
// build.
//
// It reads the WHOLE labelled gold set, both strata. That is deliberate and
// differs from every weighted number in this package: a stream estimate is only
// meaningful over the random stratum, but a false-drop anywhere is a real Job
// Listing the extractor never sees, and a page's stratum has no bearing on that.
//
// WHAT THIS FILE MAY NEVER ASSERT. "The rule keeps the pages the extractor
// accepts today" is a one-line assertion somebody will be tempted to write here
// -- every Boundary Stratum row carries verdict=accept, so a row's Verdict field
// makes it trivial. It is exactly the objective ADR-0044 considered and REJECTED:
// roughly 43% of what the pipeline saves today is not a single posting, so
// preserving today's output preserves the false positives. Recall on true
// postings is the guard; nothing here may consult the live extractor's verdict.
//
// PURE -- no parser, network, model, or file IO. cmd/llmbench replays the real
// gate under both configs to PRODUCE the rows, and this folds them.
package bench

import "fmt"

// FalseDropRung attributes a false-drop to the part of the Extract Gate that
// caused it. It is DERIVED, never stored: the two configs the guard replays differ
// in exactly one field, so a drop the pre-Positive-Evidence baseline also makes
// belongs to a reject rung -- and reject-rung tuning is out of #257's scope.
type FalseDropRung string

const (
	// RungReject is a drop the blanket-accept baseline makes too: a reject rung
	// resolved the page before Positive Evidence was ever consulted.
	RungReject FalseDropRung = "reject"
	// RungPositiveEvidence is a drop only the Positive Evidence rule makes -- the
	// population #257 owns.
	RungPositiveEvidence FalseDropRung = "positive-evidence"
)

// FalseDropRow is one gold-set row as the guard reads it: the same page decided
// twice, under the rule and under the baseline it replaces, plus whether a HUMAN
// signed its label off. Confirmed is load-bearing rather than decorative --
// ADR-0043 forbids acting on an unconfirmed boundary label, so an exception's
// standing changes the moment a human reads the page.
type FalseDropRow struct {
	URL       string
	Label     ExtractLabel
	Extract   bool // the candidate rule's decision
	Baseline  bool // the pre-Positive-Evidence blanket accept's decision
	Confirmed bool
}

// KnownFalseDrop is one page the rule is KNOWN to drop, together with the
// argument for tolerating it. Every field is part of the argument: the rung says
// whose failure it is, Confirmed pins the standing the argument was made at, and
// Reason is a human's one-line case in their own words. An exception nobody can
// read is one nobody can withdraw, so an empty Reason is a fault the audit
// reports.
type KnownFalseDrop struct {
	URL       string
	Rung      FalseDropRung
	Confirmed bool // the label's confirmation state WHEN THE ENTRY WAS WRITTEN
	Reason    string
}

// FalseDrop is one drop as the audit reports it.
type FalseDrop struct {
	URL       string
	Rung      FalseDropRung
	Confirmed bool
}

// String renders a drop for a failure message: the URL first, because that is what
// a reader of a CI log needs to open.
func (d FalseDrop) String() string {
	return fmt.Sprintf("%s (%s rung, label %s)", d.URL, d.Rung, confirmedWord(d.Confirmed))
}

// confirmedWord renders a confirmation state in the words a human uses about it.
func confirmedWord(confirmed bool) string {
	if confirmed {
		return "human-confirmed"
	}
	return "LLM-proposed, unconfirmed"
}

// FalseDropAudit is the guard's verdict. The five fatal slices ARE the guard;
// everything else is descriptive. Note what is absent: there is no tolerance, no
// budget and no count anywhere in the fatal conditions. The ledger names specific
// pages, and the guard is a hard zero on every page it does not name.
type FalseDropAudit struct {
	// Drops is every false-drop the rule makes, in input order, whether or not the
	// ledger names it -- the descriptive record a reader needs to see the whole
	// picture.
	Drops []FalseDrop
	// Unrecorded is a drop the ledger does not name. This is the hard zero: a page
	// the rule newly drops fails the build until somebody reads it and either widens
	// the rule or writes the exception down.
	Unrecorded []FalseDrop
	// Recovered is a ledger entry the rule no longer drops. Fatal so the ratchet can
	// only fall: a recovery must be recorded by deleting the entry, never pocketed
	// silently.
	Recovered []string
	// Misattributed is a drop the ledger names under the OTHER rung. It turns
	// ADR-0044's prose claim -- that two of these drops are not this rung's -- into a
	// machine-checked fact, and catches a reject rung that has silently moved.
	Misattributed []FalseDrop
	// Restanding is a drop whose label's confirmation state has changed since the
	// entry was written. It is how the guard ARMS ITSELF: ADR-0043 permits a hard
	// zero to be argued only on human-confirmed labels, so the moment a human
	// confirms one of these pages the exception must be re-argued at that standard,
	// with no code change needed to trigger it.
	Restanding []FalseDrop
	// Unargued is a ledger entry carrying no Reason, and Duplicated a URL the ledger
	// names more than once. Both are faults in the ledger itself rather than
	// findings about the gate, and both are fatal: a malformed ledger cannot be
	// reviewed.
	Unargued   []string
	Duplicated []string
	// Confirmed and Unconfirmed split Drops by whether a human signed the label off,
	// so a reader never reads a drop count without seeing how much of it rests on a
	// model's opinion.
	Confirmed   int
	Unconfirmed int
}

// Failed reports whether the guard must fail the build.
func (a FalseDropAudit) Failed() bool {
	return len(a.Unrecorded) > 0 || len(a.Recovered) > 0 || len(a.Misattributed) > 0 ||
		len(a.Restanding) > 0 || len(a.Unargued) > 0 || len(a.Duplicated) > 0
}

// AuditFalseDrops folds gold-set rows and the exception ledger into a verdict.
// PURE -- no parser, network, or LLM.
//
// A false-drop is a row whose label is Scored() and Positive() -- i.e. detail --
// that the rule skips. An ambiguous row can never be one: that is
// ExtractLabel.Scored()'s existing contract, and it is what stops a labeller's
// shrug deciding whether a build goes red.
//
// Rung attribution is exact rather than heuristic BECAUSE of how the rows are
// produced: the two configs behind Extract and Baseline differ in exactly one
// field, so a page the baseline drops too was resolved by a reject rung and a page
// only the rule drops was resolved by Positive Evidence. A caller that hands in a
// Baseline from some other config gets an attribution that means something else.
//
// Slices are initialized non-nil and preserve input order (ledger order for
// Recovered, Unargued and Duplicated), mirroring ScoreExtract's convention so
// tests can use reflect.DeepEqual.
func AuditFalseDrops(rows []FalseDropRow, ledger []KnownFalseDrop) FalseDropAudit {
	audit := FalseDropAudit{
		Drops:         []FalseDrop{},
		Unrecorded:    []FalseDrop{},
		Recovered:     []string{},
		Misattributed: []FalseDrop{},
		Restanding:    []FalseDrop{},
		Unargued:      []string{},
		Duplicated:    []string{},
	}

	known := map[string]KnownFalseDrop{}
	for _, entry := range ledger {
		if _, dup := known[entry.URL]; dup {
			audit.Duplicated = append(audit.Duplicated, entry.URL)
			continue
		}
		known[entry.URL] = entry
		if entry.Reason == "" {
			audit.Unargued = append(audit.Unargued, entry.URL)
		}
	}

	matched := map[string]struct{}{}
	for _, row := range rows {
		if !row.Label.Scored() || !row.Label.Positive() || row.Extract {
			continue
		}
		drop := FalseDrop{URL: row.URL, Confirmed: row.Confirmed, Rung: RungPositiveEvidence}
		if !row.Baseline {
			drop.Rung = RungReject
		}
		audit.Drops = append(audit.Drops, drop)
		if drop.Confirmed {
			audit.Confirmed++
		} else {
			audit.Unconfirmed++
		}

		entry, ok := known[row.URL]
		if !ok {
			audit.Unrecorded = append(audit.Unrecorded, drop)
			continue
		}
		matched[row.URL] = struct{}{}
		if entry.Rung != drop.Rung {
			audit.Misattributed = append(audit.Misattributed, drop)
		}
		if entry.Confirmed != drop.Confirmed {
			audit.Restanding = append(audit.Restanding, drop)
		}
	}

	// Ledger order, and each URL reported once however many times the ledger names
	// it: a duplicate is already reported as its own fault, and repeating it here
	// would read as two separate recoveries.
	reported := map[string]struct{}{}
	for _, entry := range ledger {
		if _, ok := matched[entry.URL]; ok {
			continue
		}
		if _, ok := reported[entry.URL]; ok {
			continue
		}
		reported[entry.URL] = struct{}{}
		audit.Recovered = append(audit.Recovered, entry.URL)
	}

	return audit
}
