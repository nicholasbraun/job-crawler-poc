# Commenting Guidelines

Based on "A Philosophy of Software Design" by John Ousterhout (Chapters 12–16).

## Core Principle

**Comments should describe things that aren't obvious from the code.** If someone reading the code for the first time could write the comment just by looking at the code next to it, the comment is worthless.

## Comment Categories

There are four categories of comments, in order of importance:

1. **Interface comments** — immediately before a type, function, or method declaration. Describe the abstraction: what it does, not how.
2. **Data structure / variable comments** — next to field or variable declarations. Describe what the variable _represents_, not how it's manipulated.
3. **Implementation comments** — inside function bodies. Describe _what_ and _why_, never _how_.
4. **Cross-module comments** — describe dependencies that cross package boundaries. Place these in a `designNotes` file or at the most obvious central point (e.g., the type declaration that all dependent code must touch).

Every exported type should have an interface comment. Every exported method should have an interface comment. Every struct field should have a comment. Implementation comments are often unnecessary for short, simple functions.

## What Makes a Bad Comment

### Repeating the code

```go
// BAD: Add a URL to the frontier
func (f *Frontier) AddURL(ctx context.Context, url URL) error {
```

The comment just restates the function name. A reader gains nothing.

### Using the same words as the name

```go
// BAD: The horizontal padding of each line in the text.
var textHorizontalPadding = 4
```

This rephrases the variable name into a sentence. No new information.

### Same level of detail as the code

Line-by-line comments that mirror what each line does are almost never useful.

**Test:** Could someone who has never seen the code write this comment just by looking at the code next to it? If yes, delete or rewrite the comment.

## What Makes a Good Comment

### Lower-level comments add precision

For variable and field declarations, fill in missing details:

- What are the units?
- Are boundary conditions inclusive or exclusive?
- What does a nil/zero value imply?
- Who is responsible for closing/freeing a resource?
- What invariants hold? (e.g., "this slice always contains at least one element")

```go
// GOOD:
// Position in the buffer of the first object that hasn't
// been returned to the client.
var offset uint32
```

Focus on **nouns, not verbs** — describe what the variable represents, not how it gets modified.

### Higher-level comments provide intuition

For implementation comments inside functions, describe the code's overall intent and purpose, not its mechanics:

```go
// BAD (too low-level, partially repeats code):
// If there is a LOADING readRpc using the same session
// as PKHash pointed to by assignPos...

// GOOD (captures intent):
// Try to append the current key hash onto an existing
// RPC to the desired server that hasn't been sent yet.
```

Ask yourself:

- What is this code trying to do?
- What is the simplest thing I can say that explains everything here?
- What is the most important thing about this code?

### Explain _why_, not _how_

When code exists for non-obvious reasons (bug fixes, subtle invariants, performance), document the _why_:

```go
// Compact the slice when len drops below half of cap to prevent
// unbounded memory growth from long-running crawls. See issue #42.
if cap(q.urls) > 64 && len(q.urls) < cap(q.urls)/2 {
```

### "How we got here" comments

Document the conditions under which a block of code executes, especially for unusual or error-handling paths:

```go
// Some key hashes couldn't be looked up in this request
// (server crashed, not enough space in response, or keys
// not stored on this server). Mark unprocessed hashes
// for reassignment to new RPCs.
```

## Interface Comments

An interface comment for a method should include:

1. **One or two sentences** describing the behavior as perceived by callers (the abstraction).
2. **Each parameter and return value** described precisely, including constraints and dependencies between arguments.
3. **Side effects** — any consequence that affects future behavior of the system.
4. **Errors/exceptions** that can be returned.
5. **Preconditions** that must be satisfied before calling.

```go
// Copy copies a range of bytes from the buffer to dest.
//
// offset is the index of the first byte to copy.
// length is the number of bytes to copy.
// dest must have room for at least length bytes.
//
// Returns the actual number of bytes copied, which may be less
// than length if the requested range extends past the end of
// the buffer. Returns 0 if there is no overlap between the
// requested range and the buffer.
func (b *Buffer) Copy(offset, length uint32, dest []byte) uint32
```

### Interface vs. implementation

Keep interface comments free of implementation details. If you can't describe a method without exposing its internals, that's a red flag — the method is probably shallow.

### Class/type-level comments

Describe the overall abstraction the type provides, not its internal mechanics:

```go
// Package inmem implements an in-memory URL frontier with per-domain
// queues, configurable cooldowns, and in-flight tracking. The frontier
// blocks on Next until a URL is available or all work is done.
package inmem
```

## Go-Specific Conventions

Follow `godoc` conventions:

- Comments on exported symbols start with the symbol name: `// NewFrontier creates a Frontier with the given options.`
- Package comments go in `doc.go` or at the top of the main file: `// Package inmem ...`
- Use `//` line comments, not `/* */` block comments for godoc.
- Every exported type, function, method, and constant should have a comment.

## Cross-Module Documentation

When a design decision affects multiple packages (e.g., the retry → frontier → orchestrator shutdown sequence), document it in one canonical place and reference it elsewhere:

```go
// See "Graceful Shutdown" in docs/designNotes.md
```

This avoids duplication while keeping the information discoverable.

## Maintaining Comments

- **Keep comments close to the code they describe.** The farther away, the more likely they'll become stale.
- **Don't duplicate.** Document each decision once, in the most natural place.
- **Put important info in the code, not the commit message.** Commit logs aren't where developers look.
- **Check diffs before committing.** Scan for comments that need updating alongside the code change.
- **Higher-level comments are easier to maintain** because they don't reflect low-level details that change frequently.

## Red Flags

| Red Flag                                    | What It Means                                                                  |
| ------------------------------------------- | ------------------------------------------------------------------------------ |
| Comment repeats the code                    | The comment adds no value. Rewrite using different words that add information. |
| Implementation details in interface comment | The abstraction is leaking. Remove implementation details or redesign.         |
| Hard to describe simply                     | The underlying design may need refactoring.                                    |
| Hard to pick a name                         | The entity may not have a clear purpose. Consider restructuring.               |
| Long comment needed for a variable          | The variable decomposition may be wrong.                                       |
