## PR Description 📝

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
- `src/app_test.go` (tests): Add a retry test
  - Add \`TestRetries\`

### 📂 Coverage

- Included: 2 files
- Omitted: 1 file

Filtered: `ignore_glob` (1):

- `vendor/lib.go`
