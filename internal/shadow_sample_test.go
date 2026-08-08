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
// attributing to a real gate rung.
func TestShadowSampleKeepsTheRawJobListingWireShape(t *testing.T) {
	page := crawler.RawJobListing{
		URL:     crawler.URL{RawURL: "https://acme.com/careers", Hostname: "acme.com"},
		Content: crawler.Content{Title: "Careers", MainContent: "body"},
	}

	// A sample encodes to the same top-level keys the old payload had, plus Rung.
	encoded, err := json.Marshal(crawler.ShadowSample{RawJobListing: page, Rung: "positive_evidence"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &keys); err != nil {
		t.Fatalf("unmarshal into a key map: %v", err)
	}
	for _, key := range []string{"URL", "Content", "Rung"} {
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
}
