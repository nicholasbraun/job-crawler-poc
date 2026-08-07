#!/usr/bin/env bash
#
# propose-expected.sh -- proposes the Extract Gold Set's expected extractions
# (#256): for every `lone-posting` row, the title, location and working mode a
# correct Free Extraction (ADR-0042) must produce.
#
# WHY IT EXISTS AS A SCRIPT. The #256 fidelity check scores the Go domain
# traversal (internal/structured_posting.go) against these values. Generating them
# by RUNNING that traversal would make the check a tautology: it could then only
# ever catch a future regression, never a bug that exists today. So this is a
# second, INDEPENDENT reading of the same JSON-LD bytes, written in jq + python3.
# Any disagreement between the two readings is a finding to investigate, never
# something to overwrite from the Go side.
#
# WHAT IT DOES NOT DO. It proposes; a human confirms. Every row it writes carries
# an empty confirmed_by, and `goldset-apply` refuses to stamp a machine name there
# (ADR-0043).
#
# usage:
#   scripts/propose-expected.sh [goldset-dir]
#
#   goldset-dir  directory holding goldset.jsonl (default: cmd/llmbench/extract-goldset)
#
# writes:
#   <dir>/expected.tsv   the review surface; `llmbench goldset-apply` folds it into
#                        the substrate and re-renders it canonically.
#
# then:
#   go run ./cmd/llmbench goldset-apply -expected-proposed-by "script:propose-expected.sh (…)"
#   go run ./cmd/llmbench score-free

set -eu

dir="${1:-cmd/llmbench/extract-goldset}"
substrate="$dir/goldset.jsonl"
out="$dir/expected.tsv"

command -v jq >/dev/null || { echo "propose-expected.sh: jq is required" >&2; exit 1; }
command -v python3 >/dev/null || { echo "propose-expected.sh: python3 is required" >&2; exit 1; }
[ -f "$substrate" ] || { echo "propose-expected.sh: no substrate at $substrate" >&2; exit 1; }

# Pass 1 (jq): walk each row's JSON-LD to the single JobPosting node and emit its
# RAW declared values as compact JSON -- deliberately not @tsv, which would escape
# an embedded tab or newline into a literal \t / \n that survives into the value.
#
# The traversal mirrors the domain's rules: @type matched case-insensitively as a
# substring (so a bare "JobPosting" and a schema.org URL both hit); a value read
# through `txt` whether the site published a string, a named node, or an array of
# either; jobLocation taken as a Place or an ARRAY of them, the first that composes
# to something non-empty winning; an address published as a bare string taken
# verbatim. Entity decoding is NOT done here -- jq cannot -- and is pass 2's job.
jq -c '
  def txt:
    if type == "string" then .
    elif type == "object" then (if has("name") then (.name | txt) else "" end)
    elif type == "array" then ([.[] | txt] | map(select(test("\\S") // false)) | (.[0] // ""))
    else "" end;
  def hastoken($want):
    if type == "string" then (ascii_downcase | contains($want))
    elif type == "array" then any(.[]; type == "string" and (ascii_downcase | contains($want)))
    else false end;
  def address:
    if type == "array" then ([.[] | address] | map(select(.parts != [] or (.str | test("\\S") // false))) | (.[0] // {parts: [], str: ""}))
    elif type == "object" then {parts: ([.addressLocality, .addressRegion, .addressCountry] | map(txt) | map(select(test("\\S") // false))), str: ""}
    elif type == "string" then {parts: [], str: .}
    else {parts: [], str: ""} end;
  def place:
    if type == "array" then ([.[] | place] | map(select(.parts != [] or (.str | test("\\S") // false))) | (.[0] // {parts: [], str: ""}))
    elif type == "object" then (.address | address)
    else {parts: [], str: ""} end;
  select(.stratum == "lone-posting")
  | . as $row
  | [ .content.JSONLD[]? | (fromjson? // empty) | .. | objects
      | select(."@type"? | hastoken("jobposting")) ] as $postings
  | ($postings[0] // {}) as $p
  | {url: $row.url,
     label: $row.label,
     note: $row.label_provenance.note,
     nodes: ($postings | length),
     title: ($p.title | txt),
     location: ($p.jobLocation | place),
     remote: ($p.jobLocationType | hastoken("telecommute"))}
' "$substrate" |
# Pass 2 (python3): apply the entity decode and whitespace collapse jq cannot --
# the same strip / unescape / strip / collapse the domain's htmlToText applies, and
# the reason a "T&#252;bingen" or a "People &amp; Culture Manager" reads correctly
# here. Then compose the address parts and write the review sheet.
python3 -c '
import hashlib, html, json, re, sys

TAG = re.compile(r"<[^>]*>")

def to_text(s):
    # internal/posting_body.go htmlToText: strip tags to spaces, unescape once,
    # strip again (a JSON-LD block is raw text to the tokenizer, so auto-escaped
    # markup only appears after the unescape), then collapse whitespace. Python
    # str.split() treats NBSP as whitespace exactly as Go strings.Fields does.
    return " ".join(TAG.sub(" ", html.unescape(TAG.sub(" ", s))).split())

rows = []
for line in sys.stdin:
    if not line.strip():
        continue
    r = json.loads(line)
    if r["nodes"] != 1:
        sys.exit("propose-expected.sh: %s carries %d JobPosting nodes, want exactly 1" % (r["url"], r["nodes"]))
    loc = r["location"]
    parts = [p for p in (to_text(x) for x in loc["parts"]) if p]
    location = ", ".join(parts) if parts else to_text(loc["str"])
    free_ok = r["label"] != "detail"
    rows.append([
        hashlib.sha256(r["url"].encode()).hexdigest()[:12],
        r["url"],
        r["label"],
        to_text(r["title"]),
        location,
        "remote" if r["remote"] else "unspecified",
        "true" if free_ok else "false",
        (r["note"] or "") if free_ok else "",
        "",
        "",
    ])

rows.sort(key=lambda row: row[1])
out = sys.argv[1]
with open(out, "w", encoding="utf-8") as f:
    f.write("#id\turl\tlabel\ttitle\tlocation\twork_arrangement\tfree_ok\tfree_ok_note\tproposed_by\tconfirmed_by\n")
    for row in rows:
        f.write("\t".join(" ".join(c.split()) for c in row) + "\n")
print("wrote %s (%d rows)" % (out, len(rows)))
' "$out"
