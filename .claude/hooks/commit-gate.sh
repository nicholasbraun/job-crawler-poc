#!/usr/bin/env bash
# PreToolUse gate for `git commit`, wired in .claude/settings.json.
#
# Runs the cheap CI gates -- gofmt, go build, golangci-lint -- on the tree being
# committed and blocks the commit (exit 2, stderr goes to the agent) when one
# fails. `go test -race` is deliberately not here: it needs Docker and takes
# minutes, so it stays with the implementer and CI.
#
# Each run appends one line to <git-common-dir>/commit-gate.log, which is how to
# confirm the gate fired for a given agent or worktree.
set -u

input=$(cat)
cmd=$(jq -r '.tool_input.command // empty' <<<"$input" 2>/dev/null) || exit 0
cwd=$(jq -r '.cwd // empty' <<<"$input" 2>/dev/null) || exit 0

# The `if` filter in settings only narrows to git commands; pick out commits here.
# Global options before the subcommand (`git -C <dir> commit`, `git -c k=v commit`)
# still count.
grep -Eq '(^|[^[:alnum:]_./-])git([[:space:]]+-[cC][[:space:]]+[^[:space:]]+)*[[:space:]]+commit([[:space:]]|$)' <<<"$cmd" || exit 0

# Gate the tree the commit lands in, which is not always the hook's cwd: an agent
# working in a worktree may reach it with `git -C <dir>` or a leading `cd <dir> &&`.
dir=$(sed -nE 's/.*git[[:space:]]+-C[[:space:]]+"?([^"[:space:]]+)"?[[:space:]]+(-[cC][[:space:]]+[^[:space:]]+[[:space:]]+)*commit.*/\1/p' <<<"$cmd" | head -n 1)
if [ -z "$dir" ]; then
	dir=$(sed -nE 's/^[[:space:]]*cd[[:space:]]+"?([^"[:space:];&]+)"?[[:space:]]*(&&|;).*/\1/p' <<<"$cmd" | head -n 1)
fi
[ -n "$cwd" ] && cd "$cwd" 2>/dev/null
if [ -n "$dir" ]; then
	cd "$dir" 2>/dev/null || true
fi
root=$(git rev-parse --show-toplevel 2>/dev/null) || exit 0
cd "$root" || exit 0

log() {
	local common
	common=$(git rev-parse --git-common-dir 2>/dev/null) || return 0
	printf '%s %s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$1" "$root" >>"$common/commit-gate.log" 2>/dev/null || true
}

# A commit with no Go change cannot break these gates. `git diff HEAD` sees staged
# and unstaged changes to tracked files; an untracked file only enters the commit
# through a `git add` in the same command, so that case never skips.
if ! grep -Eq 'git([[:space:]]+-[cC][[:space:]]+[^[:space:]]+)*[[:space:]]+add([[:space:]]|$)' <<<"$cmd" &&
	git diff --quiet HEAD -- '*.go' go.mod go.sum .golangci.yml 2>/dev/null; then
	log "skip(no-go-change)"
	exit 0
fi

block() {
	log "block($1)"
	{
		echo "commit gate: $1 failed in $root -- fix it, then commit again."
		echo "These are the same checks CI runs; do not work around the gate."
		echo
		printf '%s\n' "$2" | head -n 80
	} >&2
	exit 2
}

# gofmt over tracked files only: `gofmt -l .` would also walk other agents'
# half-written trees under .claude/worktrees.
unformatted=$(git ls-files -z -- '*.go' | while IFS= read -r -d '' f; do
	[ -f "$f" ] && printf '%s\0' "$f"
done | xargs -0 gofmt -l 2>&1)
[ -z "$unformatted" ] || block "gofmt -l" "Not gofmt-formatted (run gofmt -w on these):
$unformatted"

out=$(go build ./... 2>&1) || block "go build ./..." "$out"

command -v golangci-lint >/dev/null 2>&1 ||
	block "golangci-lint" "golangci-lint is not installed (brew install golangci-lint); the gate will not pass without it."
out=$(golangci-lint run ./... 2>&1) || block "golangci-lint run ./..." "$out"

log "pass"
exit 0
