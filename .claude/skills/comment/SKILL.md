---
name: comment
description: Review and improve code comments. Finds missing comments on exported symbols, stale or misleading comments, comments that repeat the code, and implementation details leaking into interface comments. Based on "A Philosophy of Software Design" by John Ousterhout.
---

# Comment Review Skill

Review comments across the codebase (or a specific path) and produce a report of suggested changes. Do not modify any files until the user approves.

## Steps

1. **Load guidelines.** Read `.claude/skills/comment/commenting-guidelines.md`. These are the rules for judging comment quality.

2. **Determine scope.** If the user provided a path argument (`$ARGUMENTS`), review only that path (file or directory, recursive). If no argument was provided, review all `.go` files under the project root, excluding vendored dependencies and generated files.

3. **Scan each file.** For every file in scope, check for the following issues. Use judgment — skip obvious cases like simple constructors (`NewFoo`), trivial getters, or empty `doc.go` package comments that are already adequate.

   **Missing comments:**
   - Exported types without an interface comment
   - Exported methods/functions without an interface comment
   - Exported struct fields without a comment
   - Non-trivial unexported fields that hold important invariants
   - Complex code blocks (>~15 lines) inside functions with no higher-level explanation

   **Bad comments:**
   - Comment repeats the code (could be written by reading only the code next to it)
   - Comment uses the same words as the symbol name without adding information
   - Comment describes _how_ instead of _what/why_ in implementation context
   - Implementation details in an interface comment (leaking abstraction)
   - Comment describes how a variable is manipulated instead of what it represents
   - Imprecise variable comments (missing units, boundary conditions, nil semantics, invariants)

   **Stale comments:**
   - Comment contradicts the current code behavior
   - Comment references things that no longer exist (removed parameters, old function names, dead code)
   - Comment describes a TODO or intent that has already been implemented

4. **Produce a report.** Group findings by file. For each finding, include:
   - **File and line range**
   - **Category** (missing / repeats code / stale / leaking implementation / imprecise / wrong level of detail)
   - **Current comment** (if any, quote it)
   - **Suggested comment** (your proposed replacement or addition)
   - **Reasoning** (one sentence explaining why the current state is a problem)

   At the end, include a summary with total counts per category.

5. **Wait for approval.** Present the report to the user. Do not edit any files. The user will either:
   - Approve all changes
   - Cherry-pick specific suggestions
   - Ask for revisions to specific suggestions
   - Reject suggestions they disagree with

6. **Apply approved changes.** Only after the user confirms, apply the approved suggestions to the files.

## Judgment Guidelines

- **Don't be noisy.** A report full of trivial suggestions trains the user to ignore it. Only flag things that would meaningfully help a future reader.
- **Prefer fewer, better comments** over commenting everything. A missing comment on a clear two-line helper is fine. A missing comment on a 30-line method with subtle behavior is not.
- **Respect existing style.** Match the project's conventions from `AGENTS.md` (godoc style, `//` comments, package comments in `doc.go`, etc.).
- **Interface comments are highest priority.** These define the abstractions that the rest of the codebase depends on.
