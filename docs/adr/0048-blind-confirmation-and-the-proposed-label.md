# Confirmation is blind, the Proposed Label survives an override, and the confirmation guards are floors

`renderConfirmSheet` withholds every JSON-LD-derived field, the live extractor's verdict and
the gate's own marks, with a sharp reason recorded in the code: showing the confirmer what the
gate concluded "would anchor the answer the rule is being judged against." One anchor was left
in place — the proposed label itself, printed above every row — and it is the strongest of
them all, because unlike a node count or a verdict it is a direct answer to the exact question
being asked.

So a confirmation is taken **blind**: the labeller sees the page and the captured text, gives
a verdict, and only then is shown what the proposer said. Agreement costs the same single
keystroke as disagreement, so blindness adds no work — it removes the option of skipping the
work, which is the only thing that makes a confirmation worth more than the label it confirms.

## A confirmation the labeller disagrees with must not rewrite history

`applyLabels`' proposals path sets a row's label and leaves `proposed_by` untouched, and the
stamping loop fills that field only when it is empty. A human overriding a model label
therefore produces a row reading `proposed_by: llm:claude-opus-5` beside a label that model
never proposed. Harmless at a dozen relabels a year through a TSV; not harmless in a tool
whose design point is that disagreement is the interesting outcome.

`label_provenance` gains a **Proposed Label**: the label the proposer actually proposed, kept
when a human overrides it. The record then says what happened — the model proposed X, the
human made it Y and signed for it — and the by-product is the number that says what the
unconfirmed rows are worth: how often an independent human and the model reach the same
answer. That number is only meaningful because the confirmation was blind; under a
review-the-suggestion flow it would measure how often a reader overrode something they had
already read.

Backfill is truthful exactly where it matters: a row with no confirmer still carries its
proposer's label, so its Proposed Label is its current label. The already-confirmed rows leave
it empty — the set genuinely does not record which of them were relabelled during the pass
that confirmed them.

Those rows stay empty even when a later relabel retracts their confirmation. `proposed_by` is
first-writer-wins, so after a retraction it still names the proposer of the label that was just
replaced; filling the empty field from the *replacement* would put that proposer's name beside
an answer they were never asked for, and the next confirmation of the row would then be counted
as an independent human reaching the proposer's conclusion — inflating the very number the blind
pass exists to produce, on a row nobody independently agreed on. A Proposed Label that is
already recorded is a different thing: it is a true statement about the proposer whatever
happens to the label afterwards, so a retraction leaves it exactly where it is, and the relabel
that follows is counted as the disagreement it is.

## A ratchet asserted in both directions is a pin

`randomSpotChecks` asserts equality: a confirmation that vanishes fails the build, and so does
one that lands without somebody hand-editing the constant. Nine confirmations turned `main`
red until the number was edited by hand (ead832e). A tool that makes confirmation cheap makes
that worse in proportion to how well it works.

Only one of those two directions is a safety property. A confirmation cannot land *unseen*: it
arrives inside a commit that diffs both `goldset.jsonl` and `labels.tsv`, and `goldset-apply`
already computes and prints the new count at the end of every run. So the confirmation guards
become **floors** — they fail when confirmations fall below the recorded standard, which is
the direction that means a signature vanished, and log the figure when it rises so the floor
can be ratcheted deliberately.

Row counts stay pinned in both directions, because a drawing is a fixed act of sampling and
any movement is a fault. `ambiguousRows` stays pinned too: marking a page unresolvable is a
rare, deliberate keystroke that changes what the confusion counts are computed over, and it
should stop the build until it is acknowledged.

## Consequences

- The vanished-confirmation tripwire is preserved and sharpened: a new test asserts the
  substrate and `labels.tsv` agree on which rows carry a confirmer. `goldset-apply` writes both
  from one merged slice, so they can only disagree if something outside it — a bad rebase, a
  half-applied write, a hand edit — touched one of them.
- A note is required whenever the human disagrees with the Proposed Label, not only on
  `ambiguous`, and whenever the decision CHANGES the label already on the record — including an
  answer that agrees with the proposer and so overrules a label some earlier pass wrote. Those
  rows are where a label is actually being moved, and the note is the record of why, which is
  what a prompt fix is built from. On an agreement that changes nothing the note is left empty
  so the proposer's own survives.
- The human's note replaces the proposer's on an override: the old note argues for a label
  that has been overruled, and leaving it makes the row read as if it argues for the new one.
- `goldset-apply` remains the only writer. A labelling tool collects decisions and runs them
  through that verb — its total-validation-then-write, its refusal to overwrite a proposer, and
  its retraction of a confirmation whose label changed are why the record is trustworthy, and a
  second writer forks all three.
