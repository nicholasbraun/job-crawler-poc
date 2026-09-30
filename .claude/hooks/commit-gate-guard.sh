#!/usr/bin/env bash
# PreToolUse guard, wired in .claude/settings.json for every Bash call.
#
# The commit gate itself is the git pre-commit hook in .githooks/: git runs it for
# every spelling of a commit, in the right worktree, on the staged snapshot. This
# guard only refuses the ways an agent could switch that hook off, and tells an
# agent to switch it on in a clone where it is not enabled yet.
#
# It reads the command as text, so a commit message that merely mentions one of
# the patterns below is refused too; the message says how to get past that.
set -u

input=$(cat)
cmd=$input
if command -v jq >/dev/null 2>&1; then
	parsed=$(jq -r '.tool_input.command // empty' <<<"$input" 2>/dev/null) || parsed=""
	[ -n "$parsed" ] && cmd=$parsed
	cwd=$(jq -r '.cwd // empty' <<<"$input" 2>/dev/null) || cwd=""
else
	cwd=""
fi

refuse() {
	{
		echo "commit gate guard: $1"
		echo "If the text is only part of a commit message, pass the message with 'git commit -F <file>'."
	} >&2
	exit 2
}

grep -q -- '--no-verify' <<<"$cmd" &&
	refuse "--no-verify skips the commit gate (.githooks/pre-commit). Fix what the gate reports instead."

# A token of the git command's own words, never crossing into the next command.
tok='[^[:space:];&|]+'
commit_re="(^|[^[:alnum:]_./-])git([[:space:]]+$tok)*[[:space:]]+commit"

grep -Eq "$commit_re([[:space:]]+$tok)*[[:space:]]+-[a-zA-Z]*n[a-zA-Z]*([[:space:]]|\$)" <<<"$cmd" &&
	refuse "'git commit -n' is --no-verify and skips the commit gate. Fix what the gate reports instead."

enable='git config core.hooksPath .githooks'
inspect='git config --get core.hooksPath'
if grep -qi 'core\.hookspath' <<<"$cmd"; then
	[ "$cmd" = "$enable" ] || [ "$cmd" = "$inspect" ] ||
		refuse "core.hooksPath decides whether the commit gate runs; the only allowed commands are exactly '$enable' and '$inspect'."
	exit 0
fi

# A commit in a clone where the gate was never switched on would pass unchecked.
if grep -Eq "$commit_re([[:space:]]|\$)" <<<"$cmd"; then
	[ -n "$cwd" ] && cd "$cwd" 2>/dev/null
	if git rev-parse --show-toplevel >/dev/null 2>&1 &&
		[ -f "$(git rev-parse --show-toplevel)/.githooks/pre-commit" ] &&
		[ "$(git config --get core.hooksPath 2>/dev/null)" != ".githooks" ]; then
		echo "commit gate guard: the commit gate is not enabled in this clone. Run exactly: $enable  -- then commit again." >&2
		exit 2
	fi
fi

exit 0
