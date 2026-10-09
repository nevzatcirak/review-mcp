## Suggestions

### 1. Assert the retry count

- File: `src/app_test.go` (not anchored: the head file was not available to check the quoted code)
- Label: possible issue
- Score: unscored

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

- File: `src/app.go` (not anchored: the head file was not available to check the quoted code)
- Label: possible issue
- Score: unscored

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

- The suggestions were not scored (the self-review call failed).
