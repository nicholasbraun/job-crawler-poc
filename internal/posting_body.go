package crawler

import (
	"html"
	"regexp"
	"strings"
)

// DescriptionSource is where a Job Listing's stored description came from
// (ADR-0041 / CONTEXT "Description Source"): a closed set, so a typo can never
// create a silent third state. It sits alongside SourceLane and WorkArrangement
// as a domain enum.
type DescriptionSource string

const (
	// DescriptionSourceStructuredData is a body read from the page's lone
	// structured-data JobPosting description — the cleanest crawl-lane text.
	DescriptionSourceStructuredData DescriptionSource = "structured_data"
	// DescriptionSourcePageContent is a body taken from the page's main content,
	// used when the page publishes no unambiguous structured posting. It carries
	// page chrome where the page has no semantic container (~40% more text than a
	// structured body); accepted by ADR-0041.
	DescriptionSourcePageContent DescriptionSource = "page_content"
	// DescriptionSourceATSBoard is a body supplied by an ATS board API on an ATS
	// Fetch (ADR-0022). The ATS ingest processor stamps it; the crawl-lane heal
	// must never rewrite a listing carrying it.
	DescriptionSourceATSBoard DescriptionSource = "ats_board"
	// DescriptionSourceLLMSummary marks a LEGACY, model-authored summary from
	// before ADR-0041. New code never writes it; it exists so the refetch lane can
	// find the rows it must heal from the page it already fetched.
	DescriptionSourceLLMSummary DescriptionSource = "llm_summary"
)

// DefaultDescriptionMaxChars caps a stored Posting Body at 16000 runes. It is a
// ROW-SIZE guard, not a quality knob: real postings run to ~12k characters at p99,
// so it only ever fires on a pathological parse (59k characters observed live,
// where a <body> fallback swallowed a whole site).
//
// It is deliberately NOT the extractor's prompt window (LLM_EXTRACT_MAX_CHARS):
// that dial tunes prompt latency for local models and must never decide what the
// Corpus stores (ADR-0041). The two are bounded by different knobs on purpose.
const DefaultDescriptionMaxChars = 16000

// PostingBody derives a Job Listing's Posting Body from a parsed page and reports
// the Description Source that produced it (ADR-0041). Precedence: the description of
// the page's LONE structured-data posting (LonePosting) — exactly one JobPosting node
// and no openings index, the exact complement of the Extract Gate's openings-index
// reject — reduced to plain text; otherwise the page's main content. Both are capped
// to maxChars runes.
//
// A page whose lone posting carries no usable description falls through to main
// content rather than storing an empty body. A non-positive maxChars falls back to
// DefaultDescriptionMaxChars, so a forgotten knob can never blank the Corpus. A nil
// page yields an empty body attributed to page content.
//
// Pure: no model, no network, no database.
func PostingBody(content *Content, maxChars int) (body string, source DescriptionSource) {
	if maxChars <= 0 {
		maxChars = DefaultDescriptionMaxChars
	}
	if content == nil {
		return "", DescriptionSourcePageContent
	}
	if posting, ok := LonePosting(content); ok {
		if structured := htmlToText(posting.Description); structured != "" {
			return capChars(validUTF8(structured), maxChars), DescriptionSourceStructuredData
		}
	}
	return capChars(validUTF8(content.MainContent), maxChars), DescriptionSourcePageContent
}

// validUTF8 drops bytes that are not valid UTF-8. A page served as ISO-8859-1 — or
// declaring a charset it does not honour — reaches the parser as bytes Go never
// re-encodes, so a body taken straight from it can carry them, and Postgres REJECTS
// such a write outright (SQLSTATE 22021, "invalid byte sequence for encoding UTF8").
// Observed live: a lone 0xe4 (Latin-1 "ä") failing a refetch heal.
//
// This is load-bearing on the MAIN-CONTENT branch, which is the only one carrying raw
// page bytes: before ADR-0041 the description came from the model's JSON response and
// was valid by construction, so without this a mis-encoded page fails its Save and the
// posting is lost entirely, and fails its heal on every Cycle forever. On the
// structured-data branch it is belt-and-braces — json.Unmarshal already substitutes
// U+FFFD while decoding the block (pinned by a test), so nothing invalid survives that
// far. Applied to both so the guarantee holds if either input path changes.
//
// The bytes are DROPPED rather than replaced with U+FFFD: the original character is
// unrecoverable either way, and a replacement char would only add junk lexemes to the
// weight-B search index. Recovering the text properly means honouring the response's
// charset at download time — a separate concern from bounding what is stored.
//
// Deliberately NOT applied to SourceHash's input: that hash is over raw bytes, and
// changing what it digests would invalidate every stored extraction-cache key.
func validUTF8(s string) string {
	// ToValidUTF8 returns s unchanged when it is already valid, which is the common case.
	return strings.ToValidUTF8(s, "")
}

var htmlTagRegex = regexp.MustCompile("<[^>]*>")

// htmlToText reduces a structured-data description — plain text, single-encoded HTML,
// or double-encoded HTML — to flat text. Tags are stripped to SPACES before the single
// unescape: unescaping first would turn an entity-encoded angle bracket in the text
// ("team of &lt;10 engineers") into a literal "<" the tag regex would then swallow along
// with the run of text up to the next real tag; stripping to "" instead of " " would glue
// words across block tags.
//
// The strip is then repeated after the unescape, because a JSON-LD block is raw text to
// the HTML tokenizer: goquery's .Text() does not decode entities inside <script>, and
// json.Unmarshal only undoes JSON escaping, so a site whose templating auto-escapes its
// ld+json ships "&lt;p&gt;…" and the first strip finds no tags at all. Without the second
// pass that markup would reach the stored body and the weight-B search index (the same
// trap ats.htmlDoubleEncodedToText exists for, one layer later). The second strip cannot
// eat the "&lt;10 engineers" case: a lone "<" with no closing ">" never matches the tag
// regex.
//
// Whitespace — including the NBSP that &nbsp; unescapes to, which Fields treats as space
// — is then collapsed to single spaces and trimmed, matching how the parser already
// normalizes MainContent. Mirrors ats.htmlSingleEncodedToText, which cannot be reused:
// internal/ats imports this package.
func htmlToText(s string) string {
	stripped := htmlTagRegex.ReplaceAllString(s, " ")
	unescaped := htmlTagRegex.ReplaceAllString(html.UnescapeString(stripped), " ")
	return strings.Join(strings.Fields(unescaped), " ")
}

// capChars truncates s to at most maxChars RUNES (never bytes: a byte cut could
// split a multi-byte character). Shared with SourceHash in source_hash.go, whose
// value must stay byte-identical to the extractor's historical stamp.
func capChars(s string, maxChars int) string {
	r := []rune(s)
	if len(r) <= maxChars {
		return s
	}
	return string(r[:maxChars])
}
