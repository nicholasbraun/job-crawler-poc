// This file is the Capture Fidelity rule (ADR-0047, #287): how much of an Extract
// Gold Set row's captured text a fresh fetch of its URL still carries, and therefore
// whether a re-fetch may be shown to a labeller as a view of that row at all. It is
// PURE -- no clock, no network, no model, no file -- so the judgement that decides
// what a labeller is allowed to read is testable on its own.
//
// goldFidelityInput's shape is the enforcement of ADR-0047's hardest constraint:
// the rule reads NOTHING derived from structured data. It cannot read a JSON-LD
// JobPosting node count because it is never handed one -- and that count would be
// the sharpest possible signal here, which is exactly why it is forbidden: the row's
// stratum IS that count (ADR-0043), so a fidelity state derived from it would leak
// the answer the gold set exists to test the mechanism against.
//
// The impure half -- the fetch, its politeness, and the cache of re-fetched bytes --
// lives beside this in goldsetrefetch.go.
package main

import (
	"fmt"
	"net/http"
	"strings"
	"unicode"
)

// goldFidelity is how much of a row's captured text a fresh fetch of its URL still
// carries (ADR-0047). The zero value is the ABSENCE of a measurement, not a fourth
// state: a robots.txt that refuses the re-fetch leaves a row unmeasured, which is
// neither "gone" nor "drifted" -- we did not look.
type goldFidelity string

const (
	// fidelitySame means the page on screen IS the page the label is about.
	fidelitySame goldFidelity = "same"
	// fidelityDrifted means the live page has changed since capture: an aid to read
	// with, never evidence.
	fidelityDrifted goldFidelity = "drifted"
	// fidelityGone means the live page is not the captured page. Never rendered.
	fidelityGone goldFidelity = "gone"
)

const (
	// goldFidelitySameAt is the 3-gram retention at or above which the live page IS
	// the page the label is about (ADR-0047). Inclusive.
	goldFidelitySameAt = 0.90
	// goldFidelityGoneBelow is the retention under which the captured text counts as
	// essentially absent. The asymmetry with goldFidelitySameAt is deliberate, because
	// the errors are not symmetric: calling a live page gone costs an AID -- the row is
	// still labellable from its captured text -- while calling a gone page drifted
	// costs a LABEL, since a closed posting's withdrawal page is `residue` in the
	// rubric and argues confidently for the wrong answer (ADR-0047). A page that kept
	// under a third of its 3-grams has lost its body; what is left is furniture, and
	// furniture cannot support a label.
	goldFidelityGoneBelow = 0.30
	// goldFidelityGramSize is the n of the n-gram. Three words is short enough to
	// survive boilerplate churn and long enough that agreement is not chance.
	goldFidelityGramSize = 3
	// goldFidelityMaxWords bounds the comparison on a pathological page, in words per
	// side. Far above any real posting, and finite.
	goldFidelityMaxWords = 200000
)

// goldFidelityInput is EVERYTHING the fidelity rule may read: the HTTP status, the
// two titles and the two texts. Nothing derived from structured data appears here,
// and that is the point (ADR-0047) -- do not add a JSON-LD, Embeds or ElementIDs
// field to this struct for any reason.
//
// Both texts are Flattened Text (crawler.FlattenedText, ADR-0046), so a row captured
// as a Structural Rendering and one captured flat compare identically.
type goldFidelityInput struct {
	// Status is the live response's HTTP status. 0 means the host never answered.
	Status int
	// CapturedTitle and LiveTitle are the page titles either side of the re-fetch.
	// Either may be empty; a title only testifies when both sides carry one.
	CapturedTitle string
	LiveTitle     string
	// CapturedText is the row's stored content as Flattened Text; LiveText is the
	// re-parsed re-fetch, flattened the same way.
	CapturedText string
	LiveText     string
}

// goldFidelityReport is one row's measurement, and the whole of what the screen and
// the rendering (#288) are allowed to decide from.
type goldFidelityReport struct {
	// State is the measured fidelity; empty means the row was not measured.
	State goldFidelity `json:"state,omitempty"`
	// Status is the live HTTP status, absent when there was no response to read one
	// from.
	Status int `json:"status,omitempty"`
	// Retention is the share of the captured text's 3-grams still present live, in
	// [0,1]. It is 0 on a row that was never measured.
	Retention float64 `json:"retention"`
	// Grams is how many distinct captured 3-grams the comparison ran over, so a
	// retention computed off a two-sentence capture can be read as the thin evidence
	// it is.
	Grams int `json:"grams"`
	// TitleSame reports that both sides carried a title and they match.
	TitleSame bool `json:"title_same"`
	// LiveView reports whether a re-fetch of this row may be shown to the labeller at
	// all. False for gone -- ADR-0047's dangerous case -- and false for anything not
	// measured. #288 serves the rendering; nothing may serve one without asking here.
	LiveView bool `json:"live_view"`
	// Reason is the one sentence the screen shows the labeller, in their language,
	// about what the aid in front of them is worth.
	Reason string `json:"reason"`
	// At is when the re-fetch behind this report was taken, RFC3339 UTC. Empty on a
	// report no fetch stands behind.
	At string `json:"at,omitempty"`
}

// sealed derives LiveView from State, so no caller can hand out a live view the
// measurement did not admit. Every report leaves goldFidelityOf through here.
func (r goldFidelityReport) sealed() goldFidelityReport {
	r.LiveView = r.State == fidelitySame || r.State == fidelityDrifted
	return r
}

// goldFidelitySameByConstruction is the report for a row that carries its own
// Structural Rendering. No re-fetch is taken, so there is no live view to admit: the
// page shown IS the captured page, which is the strongest `same` there is (ADR-0046).
// At stays empty, because no fetch stands behind it.
//
// It is deliberately built without sealed(): sealed may only ever WITHHOLD a live
// view, and false here is the truth rather than a refusal -- the rendering on screen is
// the captured content, not a live one.
func goldFidelitySameByConstruction() goldFidelityReport {
	return goldFidelityReport{
		State: fidelitySame, Retention: 1, LiveView: false,
		Reason: "this row carries its own Structural Rendering, so the page on screen IS the page that was captured; nothing is re-fetched (ADR-0046)",
	}
}

// goldFidelityOf measures one row's Capture Fidelity (ADR-0047). It is total: every
// input produces a state, and a caller never has to interpret a failure.
func goldFidelityOf(in goldFidelityInput) goldFidelityReport {
	rep := goldFidelityReport{Status: in.Status}

	if in.Status != http.StatusOK {
		rep.State = fidelityGone
		if in.Status == 0 {
			rep.Reason = "the live page did not answer, so it is no longer the page that was captured"
		} else {
			rep.Reason = fmt.Sprintf("the live page answered %d, so it is no longer the page that was captured", in.Status)
		}
		return rep.sealed()
	}

	captured := goldFidelityGrams(in.CapturedText)
	live := goldFidelityGrams(in.LiveText)
	rep.Grams = len(captured)
	titleSame, titlesComparable := goldFidelityTitleMatch(in.CapturedTitle, in.LiveTitle)
	rep.TitleSame = titleSame

	// A capture of under three words has no 3-grams, so retention is undefined rather
	// than zero. It is never `gone`: with nothing captured there is nothing to have
	// gone missing.
	if len(captured) == 0 {
		if len(live) == 0 && (!titlesComparable || titleSame) {
			// ADR-0047's JS-shell case: the crawler's downloader runs no JavaScript, so a
			// JS shell renders empty in both, and "a JS shell" is `residue` in the rubric.
			// The aid agrees with the capture instead of contradicting it.
			rep.State = fidelitySame
			rep.Retention = 1
			rep.Reason = "the captured page carried no text and the live page carries none either -- the aid agrees with the capture (ADR-0047)"
			return rep.sealed()
		}
		rep.State = fidelityDrifted
		rep.Reason = "the captured page carried too little text to measure against, so the live page is a hint and nothing more"
		return rep.sealed()
	}

	rep.Retention = goldFidelityRetention(captured, live)
	pct := goldFidelityPercent(rep.Retention)
	switch {
	case rep.Retention < goldFidelityGoneBelow:
		rep.State = fidelityGone
		rep.Reason = fmt.Sprintf("the live page answered 200 but carries only %s of the captured text: the page that was captured is gone", pct)
	case rep.Retention >= goldFidelitySameAt && (!titlesComparable || titleSame):
		rep.State = fidelitySame
		rep.Reason = fmt.Sprintf("the live page still carries %s of the captured text", pct)
	case rep.Retention >= goldFidelitySameAt:
		// The title only ever WITHDRAWS a claim: `same` promises the page on screen is
		// the page the label is about, and a changed title is the page saying otherwise.
		rep.State = fidelityDrifted
		rep.Reason = fmt.Sprintf("the text is %s retained but the page title changed since capture", pct)
	default:
		rep.State = fidelityDrifted
		rep.Reason = fmt.Sprintf("the live page carries %s of the captured text: it has changed since capture", pct)
	}
	return rep.sealed()
}

// goldFidelityPercent renders a retention for the one sentence a labeller reads.
func goldFidelityPercent(r float64) string { return fmt.Sprintf("%.0f%%", r*100) }

// goldFidelityGrams reduces text to the SET of its word 3-grams. Set semantics, so a
// repeated boilerplate line cannot dominate the score, and word grams rather than
// length because a re-ordered page is the same page (ADR-0047).
func goldFidelityGrams(text string) map[string]struct{} {
	words := goldFidelityWords(text)
	grams := map[string]struct{}{}
	for i := 0; i+goldFidelityGramSize <= len(words); i++ {
		grams[strings.Join(words[i:i+goldFidelityGramSize], " ")] = struct{}{}
	}
	return grams
}

// goldFidelityWords lowercases text and reduces each field to its letters and digits,
// dropping what that leaves empty, so "Berlin," and "berlin" are one word and
// punctuation churn is not drift. Capped at goldFidelityMaxWords words.
func goldFidelityWords(text string) []string {
	fields := strings.Fields(strings.ToLower(text))
	words := make([]string, 0, len(fields))
	for _, field := range fields {
		word := strings.Map(func(r rune) rune {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				return r
			}
			return -1
		}, field)
		if word == "" {
			continue
		}
		words = append(words, word)
		if len(words) == goldFidelityMaxWords {
			break
		}
	}
	return words
}

// goldFidelityRetention is the share of captured 3-grams still present in live. An
// empty captured set has no retention to report and answers 0; callers decide that
// case before they get here.
func goldFidelityRetention(captured, live map[string]struct{}) float64 {
	if len(captured) == 0 {
		return 0
	}
	kept := 0
	for gram := range captured {
		if _, ok := live[gram]; ok {
			kept++
		}
	}
	return float64(kept) / float64(len(captured))
}

// goldFidelityTitleMatch compares two page titles after flattening and lowercasing.
// comparable is false when EITHER side is empty: a page that never carried a title
// cannot testify either way, and retention alone decides.
func goldFidelityTitleMatch(captured, live string) (same, comparable bool) {
	c := strings.ToLower(flattenField(captured))
	l := strings.ToLower(flattenField(live))
	if c == "" || l == "" {
		return false, false
	}
	return c == l, true
}
