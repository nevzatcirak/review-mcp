## Suggestions

### 1. Assert the retry count

- File: `src/app_test.go` (line 2, as given by the self-review; not checked against the file)
- Label: possible issue
- Score: 9 of 10
- Why: The test cannot fail.

The test asserts nothing; check the retry count.

Existing code:

```go
func TestRetries(t *testing.T) {}
```

Improved code:

```go
func TestRetries(t *testing.T) {
	if retries != 3 {
		t.Fatal("retries")
	}
}
```

### 2. Cap the retry delay

- File: `src/app.go` (line 12, as given by the self-review; not checked against the file)
- Label: possible issue
- Score: 8 of 10
- Why: An unbounded `delay` can stall callers.

Cap the delay so that a large retry count cannot stall the caller.

Existing code:

```go
delay := retries * 100
```

Improved code:

```go
delay := min(retries*100, 1000)
```

## Coverage

- Included: 2 files
- Omitted: 1 file

Filtered: `ignore_glob` (1):

- `vendor/lib.go`

## Notes

- 1 suggestion was dropped by the self-review score (below 7).
