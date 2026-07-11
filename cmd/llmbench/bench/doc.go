// Package bench holds the pure scoring layer of the career-page classifier
// benchmark (ADR-0008): the Gold-Set manifest loader and the Scorer that folds
// per-fixture verdict rows into a deterministic report. It runs no parser,
// network, or LLM itself -- cmd/llmbench's main wiring drives the real pipeline
// to PRODUCE the rows, and this package scores them. Keeping the Scorer pure
// makes it table-testable from synthetic rows and is the one stable seam later
// tickets (LLM verdicts, repeat votes, review queue) extend without reshaping.
package bench
