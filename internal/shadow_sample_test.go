package crawler_test

import (
	"encoding/json"
	"testing"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

// TestShadowSampleKeepsTheRawJobListingWireShape pins the reason RawJobListing is
// EMBEDDED rather than held in a named field. The Shadow Extraction lane's payload
// is JSON on a durable Redis stream, so an entry enqueued before the rung
// attribution existed can be redelivered to a binary that has it. Embedding keeps
// URL and Content at the top level, so such an entry still decodes into a whole page
// -- with an empty Rung, which the processor reports as unknown rather than
// attributing to a real gate rung, and a zero Score that rung keeps anyone from
// reading as a Posting Score (ADR-0049).
func TestShadowSampleKeepsTheRawJobListingWireShape(t *testing.T) {
	page := crawler.RawJobListing{
		URL:     crawler.URL{RawURL: "https://acme.com/careers", Hostname: "acme.com"},
		Content: crawler.Content{Title: "Careers", MainContent: "body"},
	}

	// A sample encodes to the same top-level keys the old payload had, plus Rung and
	// Score.
	encoded, err := json.Marshal(crawler.ShadowSample{RawJobListing: page, Rung: "positive_evidence"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &keys); err != nil {
		t.Fatalf("unmarshal into a key map: %v", err)
	}
	for _, key := range []string{"URL", "Content", "Rung", "Score"} {
		if _, ok := keys[key]; !ok {
			t.Errorf("encoded sample has no top-level %q key: %s", key, encoded)
		}
	}

	// An OLD payload -- a bare RawJobListing -- still decodes into a whole page.
	old, err := json.Marshal(page)
	if err != nil {
		t.Fatalf("marshal the old payload: %v", err)
	}
	var decoded crawler.ShadowSample
	if err := json.Unmarshal(old, &decoded); err != nil {
		t.Fatalf("decoding an old payload: %v", err)
	}
	if decoded.URL.RawURL != page.URL.RawURL || decoded.Content.Title != page.Content.Title {
		t.Errorf("decoded %+v, want the page intact", decoded)
	}
	if decoded.Rung != "" {
		t.Errorf("decoded Rung = %q, want empty so the processor reports it unknown", decoded.Rung)
	}
	// The zero Score is SAFE rather than merely tolerated: such an entry can never
	// carry the Learned Veto's rung, because the rung and the field landed in one
	// change. The rung gates the read, so no presence flag has to cross the wire and
	// this zero is never mistaken for a page that scored zero (ADR-0049).
	if decoded.Score != 0 {
		t.Errorf("decoded Score = %v, want 0 for a payload written before the field existed", decoded.Score)
	}
}

// TestShadowSampleCarriesThePostingScore pins that the score the Learned Veto judged a
// page by survives the durable stream WITH the sample. It travels rather than being
// re-derived downstream for the reason the rung already travels: a later re-derivation
// could run a different binary's weights and file a verdict against a score the gate
// never computed (ADR-0049).
func TestShadowSampleCarriesThePostingScore(t *testing.T) {
	sample := crawler.ShadowSample{
		RawJobListing: crawler.RawJobListing{
			URL:     crawler.URL{RawURL: "https://acme.com/jobs/senior-engineer", Hostname: "acme.com"},
			Content: crawler.Content{Title: "Senior Engineer", MainContent: "body"},
		},
		Rung:  "learned_veto",
		Score: 0.4212,
	}

	encoded, err := json.Marshal(sample)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded crawler.ShadowSample
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.Rung != sample.Rung {
		t.Errorf("decoded Rung = %q, want %q", decoded.Rung, sample.Rung)
	}
	if decoded.Score != sample.Score {
		t.Errorf("decoded Score = %v, want %v exactly: the verdict downstream is filed against this number",
			decoded.Score, sample.Score)
	}
}
