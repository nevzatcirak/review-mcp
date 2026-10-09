## PR Description

**Partial description: 1 of 6 changed files was described. 5 files were not described (see Coverage).**

### Title

Raise the retry count and test it

### Type

Enhancement, Tests

### Summary

- Raise the retry count to three
- Add a retry test ANSWER-MARKER-f7e5

### Walkthrough

- `src/app.go` (enhancement): Raise the retry count
  - Raise \`retries\` from 1 to 3

### Not described

- `src/big.go`: included only in part (clipped to fit the context window)
- `src/later.go`: left out to fit the context window
- `src/part2.go`: its part's model call failed
- `src/app_test.go`: shown to the model, but it returned no walkthrough entry
- `src/huge.go`: skipped: `size_limit`

### Coverage

- Included: 2 files (1 in part only, clipped to fit the context window)
- Omitted: 6 files

Included in part only (clipped to fit the context window) (1):

- `src/big.go`

Left out to fit the context window (modified files) (1):

- `src/later.go`

Skipped: `binary` (1):

- `assets/logo.png`

Skipped: `model_call_failed` (1):

- `src/part2.go`

Skipped: `not_returned` (1):

- `src/app_test.go`

Skipped: `size_limit` (1):

- `src/huge.go`

Filtered: `ignore_glob` (1):

- `vendor/lib.go`

### Notes

- 1 file was shown to the model but got no walkthrough entry; listed as not described (see Coverage).
