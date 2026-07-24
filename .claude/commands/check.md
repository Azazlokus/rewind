---
description: Run the mandatory pre-commit gate (lint + race tests)
---

Run the project's mandatory gate and report the result concisely.

1. `make check` (which is `go vet ./...` + golangci-lint if installed, then `go test -race -count=1 ./...`).
2. If golangci-lint is not installed, note that vet ran but the linter was skipped — do not treat its absence as a failure.
3. Summarise: pass/fail per stage, and for any failure show the exact failing test or vet line. Do not fix anything unless asked.

$ARGUMENTS
