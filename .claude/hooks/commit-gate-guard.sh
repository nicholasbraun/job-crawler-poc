#!/usr/bin/env bash
# PreToolUse guard, wired in .claude/settings.json for every Bash call.
#
# The commit gate itself is the git hook .githooks/pre-commit. This guard covers
# what git cannot: it refuses the usual ways an agent might switch that hook off
# (--no-verify, `commit -n`, changing core.hooksPath), refuses a commit where the
# gate is not in effect, and turns every `git push` into a prompt for the human.
#
# It reads the command's words, not its effect, so it is a backstop against the
# obvious spellings and not a boundary: a determined command can get past it.
# It looks only at the words of git invocations, so other commands that merely
# mention these patterns (grep, a quoted commit message) pass.
set -u

input=$(cat)
case "$input" in
*git*) ;;
*) exit 0 ;;
esac
command -v jq >/dev/null 2>&1 || exit 0
cmd=$(jq -r '.tool_input.command // empty' <<<"$input" 2>/dev/null) || exit 0
[ -n "$cmd" ] || exit 0
cwd=$(jq -r '.cwd // empty' <<<"$input" 2>/dev/null) || cwd=""

# Split the command into simple commands and words (quotes and backslashes
# honoured), and report one line per finding:
#   REFUSE noverify | REFUSE hookspath | ASK push | COMMIT <cd dir>\t<-C dirs...>
verdicts=$(CMD="$cmd" awk '
function endtok() { if (has) t[++nt] = tok; tok = ""; has = 0 }
function endcmd() { if (nt > 0) analyze(); nt = 0 }
function analyze(   k, a, sub_, dirs, j, ch, rest, m, lc) {
	k = 1
	while (k <= nt && (t[k] ~ /^[A-Za-z_][A-Za-z0-9_]*=/ || (t[k] in wrap) || (k > 1 && t[k - 1] == "timeout"))) k++
	if (k > nt) return
	if (t[k] == "cd") { if (k < nt) cddir = t[k + 1]; return }
	if (t[k] != "git" && t[k] !~ /\/git$/) return
	k++
	dirs = ""
	while (k <= nt && t[k] ~ /^-/) {
		a = t[k]
		if (a == "-C") { dirs = dirs "\t" t[k + 1]; k += 2; continue }
		if (a == "-c") { if (tolower(t[k + 1]) ~ /^core\.hookspath(=|$)/) print "REFUSE hookspath"; k += 2; continue }
		if (tolower(a) ~ /^--config-env=core\.hookspath/) print "REFUSE hookspath"
		if (a == "--git-dir" || a == "--work-tree" || a == "--namespace" || a == "--config-env") { k += 2; continue }
		k++
	}
	if (k > nt) return
	sub_ = t[k]; k++

	if (sub_ == "push") { print "ASK push"; return }

	if (sub_ == "config") {
		rest = ""; m = 0
		for (; k <= nt; k++) {
			lc = tolower(t[k])
			if (lc == "--local") continue
			if (lc == "core.hookspath" || lc ~ /^core\.hookspath=/) { m = 1; lc = "KEY" }
			else if ((lc == "--remove-section" || lc == "remove-section" || lc == "--rename-section" || lc == "rename-section") && tolower(t[k + 1]) == "core") m = 2
			else lc = t[k]
			rest = rest " " lc
		}
		if (m == 2) print "REFUSE hookspath"
		else if (m == 1 && rest != " KEY" && rest != " --get KEY" && rest != " get KEY" && rest != " KEY .githooks" && rest != " set KEY .githooks") print "REFUSE hookspath"
		return
	}

	if (sub_ == "merge" || sub_ == "pull") {
		for (; k <= nt; k++) if (t[k] ~ /^--no-veri/) print "REFUSE noverify"
		if (sub_ == "merge") print "COMMIT " cddir dirs
		return
	}

	if (sub_ == "commit") {
		for (; k <= nt; k++) {
			a = t[k]
			if (a == "--") break
			if (a ~ /^--no-veri/) { print "REFUSE noverify"; continue }
			if (a ~ /^--/) { if (a !~ /=/ && (a in longarg)) k++; continue }
			if (a ~ /^-[A-Za-z]/) {
				# a cluster of short options: n is --no-verify; m F C c t take the
				# rest of the cluster or the next word as their value; u and S take
				# the rest of the cluster
				for (j = 2; j <= length(a); j++) {
					ch = substr(a, j, 1)
					if (ch == "n") { print "REFUSE noverify"; break }
					if (index("mFCct", ch)) { if (j == length(a)) k++; break }
					if (index("uS", ch)) break
				}
			}
		}
		print "COMMIT " cddir dirs
	}
}
BEGIN {
	split("{ ! if then else elif do while until time command exec env nohup sudo timeout", w, " "); for (i in w) wrap[w[i]] = 1
	split("--message --file --author --date --reuse-message --reedit-message --fixup --squash --template --cleanup --pathspec-from-file --trailer", l, " "); for (i in l) longarg[l[i]] = 1
	s = ENVIRON["CMD"]; n = length(s); tok = ""; has = 0; nt = 0; q = ""; cddir = ""
	for (i = 1; i <= n; i++) {
		c = substr(s, i, 1)
		if (q == "\047") { if (c == "\047") q = ""; else tok = tok c; continue }
		if (q == "\"") {
			if (c == "\"") { q = ""; continue }
			if (c == "\\" && i < n) { i++; tok = tok substr(s, i, 1); continue }
			tok = tok c; continue
		}
		if (c == "\047" || c == "\"") { q = c; has = 1; continue }
		if (c == "\\" && i < n) { i++; c = substr(s, i, 1); if (c != "\n") { tok = tok c; has = 1 }; continue }
		if (c == " " || c == "\t") { endtok(); continue }
		if (c == "\n" || c == ";" || c == "&" || c == "|" || c == "(" || c == ")" || c == "`") { endtok(); endcmd(); continue }
		if (c == "$" && substr(s, i + 1, 1) == "(") { endtok(); endcmd(); i++; continue }
		tok = tok c; has = 1
	}
	endtok(); endcmd()
}') || exit 0
[ -n "$verdicts" ] || exit 0

refuse() {
	echo "commit gate guard: $1" >&2
	exit 2
}

grep -q '^REFUSE noverify$' <<<"$verdicts" &&
	refuse "this skips the commit gate (.githooks/pre-commit). Fix what the gate reports instead of bypassing it."
grep -q '^REFUSE hookspath$' <<<"$verdicts" &&
	refuse "core.hooksPath decides whether the commit gate runs. It may only be read, or set with: git config core.hooksPath .githooks"

# A commit or merge where the gate is not in effect would pass unchecked.
while IFS= read -r line; do
	case "$line" in
	"COMMIT "*) ;;
	*) continue ;;
	esac
	dir=${cwd:-.}
	unknown=""
	IFS=$'\t' read -r -a parts <<<"${line#COMMIT }"
	for p in "${parts[@]:-}"; do
		[ -n "$p" ] || continue
		case "$p" in
		*'$'*) unknown=1 ;;
		"~") p=$HOME ;;
		"~/"*) p="$HOME/${p#\~/}" ;;
		esac
		case "$p" in
		/*) dir=$p ;;
		*) dir="$dir/$p" ;;
		esac
	done
	[ -z "$unknown" ] || continue
	top=$(git -C "$dir" rev-parse --show-toplevel 2>/dev/null) || continue
	hooks=$(git -C "$dir" config --get core.hooksPath 2>/dev/null || true)
	if [ -f "$top/.githooks/pre-commit" ]; then
		[ "$hooks" = ".githooks" ] ||
			refuse "the commit gate is not enabled in $top. Run 'git config core.hooksPath .githooks' there, then commit again."
	elif [ "$hooks" = ".githooks" ]; then
		refuse "the checkout at $top predates the commit gate (it has no .githooks/pre-commit), so this commit would go unchecked. Merge or rebase the default branch first."
	fi
done <<<"$verdicts"

if grep -q '^ASK push$' <<<"$verdicts"; then
	jq -n '{hookSpecificOutput: {hookEventName: "PreToolUse", permissionDecision: "ask", permissionDecisionReason: "git push publishes commits; this repo asks before every push."}}'
fi
exit 0
