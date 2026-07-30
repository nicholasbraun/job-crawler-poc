#!/usr/bin/env bash
#
# capture-goldset.sh -- step 2 of the Extract Gold Set harvest (#116).
#
# Reads the JSONL capture file produced by the extract-decision tap
# (EXTRACT_CAPTURE_PATH, internal/extractcapture) and re-fetches each captured URL
# into a raw-HTML fixture via `llmbench capture -kind extract`, building an
# unlabeled Extract Gold Set. You then hand-label it (detail / hub-index /
# residue, set verified:true) and score the new gate against it with
# `llmbench extract -gold <dir>`.
#
# The live pipeline keeps only parsed content, so this re-fetches the raw bytes
# through the crawler's own downloader (matching User-Agent, no JS). For a
# few-hours capture window the page has almost certainly not changed; a page that
# moved between crawl and capture just relabels a fresh target.
#
# usage:
#   scripts/capture-goldset.sh <capture.jsonl> [gold-dir] [verdict] [max] [sleep]
#
#   capture.jsonl  the tap output (one {"url","verdict","ts"} per line)
#   gold-dir       output Gold Set dir (default: cmd/llmbench/extract-goldset-live)
#   verdict        which rows to fetch: all | accept | abstain (default: all)
#   max            cap on URLs fetched, 0 = all (default: 0)
#   sleep          seconds between fetches, be polite to hosts (default: 0.5)
#
# Tip: capture the two verdicts into separate dirs so you can stratify labeling
# and oversample the weak-signal positives:
#   scripts/capture-goldset.sh cap.jsonl gold-accept  accept  400
#   scripts/capture-goldset.sh cap.jsonl gold-abstain abstain 400

set -eu

cap="${1:?usage: capture-goldset.sh <capture.jsonl> [gold-dir] [verdict] [max] [sleep]}"
gold="${2:-cmd/llmbench/extract-goldset-live}"
verdict="${3:-all}"
max="${4:-0}"
gap="${5:-0.5}"

[ -f "$cap" ] || { echo "capture file not found: $cap" >&2; exit 1; }

case "$verdict" in
  all)     match='"verdict":' ;;
  accept)  match='"verdict":true' ;;
  abstain) match='"verdict":false' ;;
  *) echo "verdict must be all|accept|abstain, got: $verdict" >&2; exit 2 ;;
esac

# Build the binary once -- looping `go run` recompiles llmbench on every call.
bin="$(mktemp -d)/llmbench"
echo "building llmbench -> $bin" >&2
go build -o "$bin" ./cmd/llmbench

# Select the verdict's lines, extract the url value (field order is fixed with
# url first; SetEscapeHTML(false) keeps '&' literal so this is faithful), then
# de-duplicate while preserving first-seen order.
urls="$(grep -F "$match" "$cap" \
  | sed -n 's/.*"url":"\([^"]*\)".*/\1/p' \
  | awk '!seen[$0]++')"

if [ "$max" -gt 0 ]; then
  urls="$(printf '%s\n' "$urls" | head -n "$max")"
fi

total="$(printf '%s\n' "$urls" | grep -c . || true)"
echo "capturing $total URL(s) (verdict=$verdict) into $gold" >&2

i=0
printf '%s\n' "$urls" | while IFS= read -r url; do
  [ -n "$url" ] || continue
  i=$((i + 1))
  echo "[$i/$total] $url" >&2
  # One dead link / timeout must not abort the batch.
  "$bin" capture -kind extract -gold "$gold" "$url" || echo "  skipped (fetch failed)" >&2
  sleep "$gap"
done

echo >&2
echo "done. Next:" >&2
echo "  1. label $gold/manifest.json  (label: detail|hub-index|residue, verified: true)" >&2
echo "  2. score the gate:  go run ./cmd/llmbench extract -gold $gold" >&2
