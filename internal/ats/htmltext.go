package ats

import (
	"html"
	"regexp"
	"strings"
)

var htmlTagRegex = regexp.MustCompile("<[^>]*>")

// htmlSingleEncodedToText reduces single-encoded HTML — literal markup whose
// text-level specials are entity-encoded exactly once, e.g. Lever's description,
// lists[].content and additional sections — to plain text. Tags are stripped
// BEFORE the single unescape: unescaping first would turn an entity-encoded angle
// bracket in the text (e.g. "team of &lt;10 engineers") into a literal "<" that the
// tag regex would then swallow together with the run of text up to the next real
// tag, silently dropping it. The order is therefore: strip tags to spaces (so words
// do not glue across block tags), unescape the text-level entities once
// (&amp; -> &, &lt; -> <, &nbsp; -> a space), then collapse runs of whitespace —
// including the NBSP that Fields treats as space — to single spaces.
func htmlSingleEncodedToText(s string) string {
	stripped := htmlTagRegex.ReplaceAllString(s, " ")
	text := html.UnescapeString(stripped)
	return strings.Join(strings.Fields(text), " ")
}

// htmlDoubleEncodedToText reduces double-encoded HTML — an entity-encoded HTML
// document, e.g. Greenhouse's content field — to plain text. The outer layer is
// unescaped first to recover real HTML whose text nodes are themselves still
// entity-encoded (&amp;nbsp;, &amp;amp;), then the single-encoded reduction strips
// the tags and unescapes the residual text-node entities. Applying the single-
// encoded reduction directly would leave every real tag entity-encoded (&lt;p&gt;),
// so nothing would be stripped.
func htmlDoubleEncodedToText(s string) string {
	return htmlSingleEncodedToText(html.UnescapeString(s))
}
