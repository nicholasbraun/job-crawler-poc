---
name: git-commit
description: "Use this skill whenever the user asks you to commit, stage, or create a git commit message. Also trigger when the user says 'commit this', 'save my changes', 'write a commit message', 'stage and commit', or asks you to review staged changes and commit them. This skill enforces Conventional Commits format and project-specific conventions from AGENTS.md."
---

# Git Commit Skill

Create well-structured git commits following the Conventional Commits specification
and project-specific conventions.

## Workflow

1. **Check staged changes** — run `git diff --cached --stat` to see what's staged.
   If nothing is staged, run `git status --short` and ask the user what to stage,
   or suggest `git add -A` if all changes look intentional.

2. **Review the diff** — run `git diff --cached` to understand what changed.
   Read the diff carefully before writing any commit message.

3. **Check for AGENTS.md** — look for `AGENTS.md` or `CLAUDE.md` in the repo root.
   If present, read the commit message section for project-specific conventions
   (types, scopes, formatting rules). Project conventions override the defaults below.

4. **Write the commit message** — follow the format rules below.

5. **Show the message to the user** — always show the proposed commit message
   and ask for confirmation before running `git commit`.

6. **Commit** — run `git commit -m "<message>"` only after user approval.

## Commit Message Format

```
type(scope): description

[optional body]
```

### Rules

- **type**: one of the types listed below (lowercase, required)
- **scope**: the area of the codebase affected (lowercase, optional, in parentheses)
- **description**: short summary (lowercase, imperative mood, no period at the end)
- **body**: optional, separated by a blank line, explains _why_ not _what_

### Types

| Type       | When to use                                             |
| ---------- | ------------------------------------------------------- |
| `feat`     | A new feature or capability                             |
| `fix`      | A bug fix                                               |
| `refactor` | Code change that neither fixes a bug nor adds a feature |
| `test`     | Adding or updating tests                                |
| `docs`     | Documentation only changes                              |
| `chore`    | Build process, dependencies, tooling, CI changes        |
| `perf`     | Performance improvement                                 |
| `style`    | Formatting, whitespace, missing semicolons (no logic)   |
| `ci`       | CI/CD configuration changes                             |

### Scope Examples

Derive the scope from the package or area changed:

- `feat(frontier): add max depth limiting`
- `fix(retry): handle context cancellation between retries`
- `test(sqlite): add job repository round-trip test`
- `refactor(orchestrator): extract URL processing into separate method`
- `docs(readme): add architecture diagram`
- `chore(deps): upgrade goquery to v1.12`

If the change spans many packages, omit the scope:

- `refactor: rename Base to Hostname on URL type`

### Description Guidelines

- Use imperative mood: "add" not "added" or "adds"
- Lowercase first letter
- No period at the end
- Keep under 72 characters (type + scope + description combined)
- Describe _what_ the commit does, not _how_

### Body Guidelines

- Separate from the description with a blank line
- Use when the _why_ isn't obvious from the description
- Wrap lines at 72 characters
- Can include bullet points with `-`

### Examples

**Simple:**

```
feat(parser): add fallback to article tag for main content
```

**With scope:**

```
fix(frontier): unlock mutex before returning ErrMaxDomainLimit
```

**With body:**

```
refactor(orchestrator): move depth check before frontier.AddURL

URLs at max depth were being added to the frontier and then
immediately skipped on Next(). Checking depth before AddURL
keeps the frontier lean.
```

**Multi-package:**

```
refactor: rename Job to JobListing across all packages
```

**Breaking change:**

```
feat(frontier)!: add MarkDone for in-flight tracking

BREAKING CHANGE: Frontier interface now requires MarkDone method.
All implementations must be updated.
```

## What NOT To Do

- Do NOT commit without showing the message to the user first
- Do NOT use past tense ("added feature") — use imperative ("add feature")
- Do NOT capitalize the description ("Add feature") — use lowercase ("add feature")
- Do NOT end the description with a period
- Do NOT write vague messages like "fix stuff", "update code", "wip"
- Do NOT combine unrelated changes in one commit — suggest splitting if the
  diff contains multiple unrelated changes
- Do NOT stage files the user didn't intend to commit — always verify with
  `git status` first
- Do NOT add 'Co-Authored by Claude..' in the commit body
