---
description: Write the short end-of-iteration report (done / measured / deferred)
---

Produce the short iteration report required by the repo rules (rule 7):

- **Что сделано** — bullet list of what shipped this iteration.
- **Замеры** — any numbers (allocs/op, bytes/tick, tick p50/p99). Pull real figures
  from `BENCHMARKS.md` or by running the relevant `make bench` target; never invent them.
- **Что отложено** — anything consciously punted to a later iteration.
- **Acceptance** — restate the iteration's acceptance criteria and whether each is met,
  with the evidence (test name, benchmark line).

Confirm `make check` is green before declaring an iteration complete. Keep it tight.

$ARGUMENTS
