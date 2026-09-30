---
paths:
  - "cmd/llmbench/**"
  - "internal/pagegate/**"
---

# Gate benchmarks (cmd/llmbench)

A change to `internal/pagegate` is scored offline with these verbs before it ships
(see "Gate changes are measured, not argued" in `CLAUDE.md`). The full verb list is
in the package comment of `cmd/llmbench/main.go`.

```bash
go run ./cmd/llmbench bench -llm=false          # career-page Gate over the Gold Set, gate-only
go run ./cmd/llmbench bench                     # ...same, but confirms uncertain fixtures with
                                                #    the real classifier (needs LLM_* env)
go run ./cmd/llmbench extract                   # Extract Gate over the reject-rung fixtures
go run ./cmd/llmbench score-capture -in <labeled.jsonl>   # Extract Gate over the Extract Gold Set
go run ./cmd/llmbench score-capture -in <labeled.jsonl> -gate-config <veto.json>
                                                #    ...the same scorecard with the Learned Veto on (ADR-0049);
                                                #    veto.json is {"LearnedVeto": true}
go run ./cmd/llmbench score-rendering            # A/B: Flattened Text vs Structural Rendering at one prompt budget
go run ./cmd/llmbench goldset-sample-veto-boundary -capture <capture.jsonl> -since <RFC3339>
                                                #    veto depth over a capture frame (no labels), plus the
                                                #    sampling plan preview; -draw appends a stratified sample of the
                                                #    drop set in bands (-near-band, then a quota per band:
                                                #    -accepted-rows/-near-rows/-deep-rows)
go run ./cmd/llmbench goldset-sample-host-breadth -capture <capture.jsonl> -since <RFC3339>
                                                #    host clusters in a capture frame and the sampling plan preview
                                                #    (ADR-0050; reads no Posting Score, so no circular selection);
                                                #    -draw appends one page per host, stratified on the live verdict
go run ./cmd/llmbench train-scorer               # refit the Posting Score over the Extract Gold Set and rewrite
                                                #    pagegate's weights (must reproduce the committed file byte for byte)
go run ./cmd/llmbench goldset-refit              # after a confirmation pass: apply, rewrite the derived
                                                #    counts, refit the weights, run the suite, and check
                                                #    ADR-0049's pre-registered condition
go run ./cmd/llmbench goldset-ui -by "<your name>" -stratum random   # blind, one-keystroke confirmation pass (loopback only;
                                                                    #    measures Capture Fidelity and renders the page;
                                                                    #    -refetch=false for neither)
```
