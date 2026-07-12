# Gate deterministically rejects single-posting URLs, with a terminal-hub-word exemption

The Discovery Gate hard-rejects any career URL whose path is a job-section
segment followed by a further segment (`/careers/senior-engineer`, `/jobs/12345`)
instead of forwarding it to the LLM. This is a deliberate reversal of "let the
model decide": the catalog audit (ADR-0007 / #45) established that the LLM
rubber-stamps single postings, so both the old JobPosting-JSON-LD bypass and a
plain uncertain-accept admitted them anyway. The Gate is a structural guard, not
an LLM vote, so a structurally-decidable non-hub must die at the Gate.

The reject is exempted when the final path segment is a **Terminal-Hub Word** — an
openings-index token (`open-positions`, `opportunities`, `vacancies`, …) that
marks a deep path a Career Page hub rather than a posting. The exemption's word
list is not guessed: a naive "segment + segment ⇒ posting" veto leaked six real
deep-path hubs in the Gold Set (`/careers/open-positions`, `/karriere/jobs/alle-jobs`,
…), so the list is fixed by the requirement of zero Leaks on the Gold Set, and the
Leak guard (ADR-0008) is the backstop that keeps it honest as new hubs appear.

## Consequences

- The veto is pure URL structure, so it also sheds deep career **sub-pages**
  (culture/section pages under `/careers/…`) — desirable, and a large slice of the
  audited false positives.
- It can Leak the rare Company whose only hub lives at a deep, non-terminal career
  path with no bare-root hub. Accepted: the Gold Set Leak check turns such a case
  red, at which point we add its tail to the Terminal-Hub Word list.
- The exemption is a second curated keyword list to maintain alongside the career
  and reject signals; it is small, unit-tested, and bench-guarded.
