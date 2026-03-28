---
name: code-review
description: "Use this skill whenever the user asks you to review code, check changes, audit a PR, look for bugs, or evaluate code quality. Also trigger when the user says 'review this', 'check my code', 'anything wrong here?', 'can you spot issues?', 'audit this', or 'what do you think of these changes?'. This skill performs a structured code review covering correctness, security, design, error handling, and edge cases. IMPORTANT: This skill is READ-ONLY. Do NOT modify any files. Only use reading tools: view, glob, grep, bash (for git diff, git log, git show only)."
---

# Code Review Skill

Perform structured code reviews that surface real issues with specific,
actionable feedback. Read-only — never modify files.

## Allowed Tools

You may ONLY use these tools during a review:

- `view` — read files and directories
- `grep` / `glob` — search for patterns across the codebase
- `bash` — ONLY for read-only git commands:
  - `git diff`, `git diff --cached`, `git diff main..HEAD`
  - `git log`, `git show`
  - `git status`
  - `go vet ./...`, `go build ./...` (analysis only, no modifications)
  - `go test ./...` (running tests to verify correctness)

Do NOT use: `str_replace`, `create_file`, `bash` with write commands (`git commit`,
`git add`, `rm`, `mv`, `cp`, `sed`, etc.)

If you find issues, describe the fix — do not apply it.

## Workflow

### 1. Understand the Scope

Determine what to review:

- If the user points to specific files → review those files
- If the user says "review my changes" → run `git diff` or `git diff --cached`
- If the user says "review the last commit" → run `git show HEAD`
- If the user says "review this branch" → run `git diff main..HEAD`
- If unclear → run `git status` and ask the user what to review

### 2. Read Context

Before reviewing changed code, understand the surrounding context:

- Read the full file(s) containing changes, not just the diff
- Check for AGENTS.md or CLAUDE.md for project conventions
- Look at related interfaces, types, and tests to understand contracts
- Check imports to understand dependencies

### 3. Review

Evaluate the code against each category below. Only report findings that
are genuine issues — do not pad the review with nitpicks or style
preferences unless they violate project conventions.

### 4. Present Findings

Organize findings by severity, not by category. Use this format:

```
## Review: <brief scope description>

### Critical
Issues that will cause bugs, data loss, security vulnerabilities,
or crashes in production.

### Important
Issues that affect correctness, maintainability, or reliability
but won't cause immediate failures.

### Minor
Suggestions for improvement that are not urgent.

### Looks Good
Briefly note what's well-designed — good reviews aren't only negative.
```

If there are no findings in a severity level, omit that section.
If everything looks good, say so — don't invent issues.

## Review Categories

### Correctness & Logic

- Does the code do what it claims to do?
- Are there off-by-one errors, nil pointer risks, or wrong comparisons?
- Are boolean conditions correct? Watch for inverted logic.
- Are return values checked? Especially errors.
- Could any operation panic? (nil map access, index out of bounds, nil pointer dereference)
- Are goroutines properly synchronized? Look for races on shared state.
- For loops: are loop variables captured correctly in closures?

### Error Handling

- Are all errors checked? Look for unchecked error returns.
- Are errors wrapped with context? (`fmt.Errorf("doing X: %w", err)`)
- Is there a mix of `%w` and `%v` for errors? `%v` discards the error chain.
- Are sentinel errors compared with `errors.Is()` not `==`?
- On error, is acquired state cleaned up? (mutex unlocks, file closes, rollbacks)
- Are errors logged AND returned? That's usually a bug — pick one.
- Could an error path leave the system in an inconsistent state?

### Security

- Is user/external input validated before use?
- SQL: are queries parameterized? Look for string concatenation in queries.
- HTTP: are responses from external services validated (status code, content type)?
- Are secrets hardcoded? (API keys, passwords, tokens in source)
- Is HTML from untrusted sources sanitized before storage or display?
- Are file paths validated to prevent path traversal?
- Is crypto usage correct? (constant-time comparison for secrets, proper random sources)
- Prompt injection: is untrusted content injected into LLM prompts without separation?

### Concurrency

- Are shared variables protected by a mutex or channel?
- Is the mutex held during blocking operations? (network calls, channel sends, sleeps)
- Can a goroutine leak? (blocked on a channel that nobody will close)
- Is `sync.WaitGroup.Add()` called before `go func()`?
- Are channel operations properly selected with `ctx.Done()`?
- Could there be a deadlock? (lock ordering, nested locks, unbuffered channels)

### Design & Architecture

- Does the code follow the project's package conventions? (check AGENTS.md)
- Are interfaces defined by the consumer, not the implementation?
- Is there unnecessary coupling between packages?
- Are responsibilities clearly separated? (single responsibility principle)
- Is there duplication that should be extracted?
- Are public APIs minimal? Could anything be unexported?
- Does a function do too many things? Look for functions over 50 lines.

### Edge Cases

- What happens with empty input? (empty string, nil slice, zero value struct)
- What happens with very large input? (huge HTML pages, thousands of URLs)
- What happens when external services are unavailable? (database, HTTP, LLM APIs)
- What happens on context cancellation mid-operation?
- Are map lookups guarded against missing keys?
- For numeric operations: overflow, underflow, division by zero?

### Testing

- Are the right things tested? (behavior, not implementation details)
- Are edge cases covered? (empty input, error paths, boundary values)
- Are tests isolated? (no shared state between test cases)
- Do tests use external test packages? (`package foo_test` not `package foo`)
- Are mocks/spies defined inline? (no generated mocks per project conventions)
- Is `t.Helper()` used on test helper functions?
- Are concurrent tests using `synctest.Test` or `t.Parallel()` correctly?

### Performance (only when it matters)

Only flag performance issues that would matter at the project's actual scale.
Do NOT flag:

- Micro-optimizations (preallocating small slices, string builder vs concat for 3 strings)
- Theoretical concerns that don't apply to the current data volume

DO flag:

- O(n²) or worse algorithms operating on unbounded input
- Unbounded memory growth (slices that grow without limit, goroutine leaks)
- Missing `defer rows.Close()` or `defer resp.Body.Close()` (resource leaks)
- Database queries inside loops (N+1 query patterns)
- Holding locks during I/O operations
- Regexp compilation inside hot loops (compile once, reuse)

## Tone

- Be direct and specific. "The mutex is held during the HTTP call on line 47"
  not "consider whether the locking strategy is optimal."
- Suggest the fix, don't just name the problem. "Unlock before the select block
  and re-lock after" not "this could be improved."
- Acknowledge good decisions. If the code handles something well, say so briefly.
- Don't apologize for findings. Don't say "this might be intentional but..."
  — state the issue and the author can decide.
- If you're unsure whether something is a bug or intentional, say so explicitly
  and explain both interpretations.
