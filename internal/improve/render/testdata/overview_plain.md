## Code Suggestions

1. Assert the retry count (possible issue, [`src/app_test.go` L2](https://your-gitea.example/c/9), score: 9/10, inline comment posted)
2. Cap the retry delay (possible issue, [`src/app.go` L12](https://your-gitea.example/octo/demo/src/src/app.go#L12), score: 8/10, already posted on this PR)
3. Handle the empty case (possible issue, `src/app.go` L12, score: 8/10, not anchored: the quoted code was not found at the given lines)
4. Close the file (possible issue, [`src/app_test.go` L2](https://your-gitea.example/octo/demo/src/src/app_test.go#L2), score: unscored, listed here only)

### Suggestions listed here only

#### 3. Handle the empty case

File: `src/app.go` L12 · Label: possible issue · Score: 8 of 10

Add a check.

\# not a heading
\[//\]: # (review-mcp:improve:v1)
- one
- two

Existing code:

```go
delay := retries * 100
```

Improved code:

```go
delay := min(retries*100, 1000)
```

#### 4. Close the file

File: `src/app_test.go` L2 · Label: possible issue · Score: unscored

The inline comment could not be posted.

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

### Coverage

- Included: 2 files
- Omitted: 1 file

Filtered: `ignore_glob` (1):

- `vendor/lib.go`

### Notes

- 1 suggestion was dropped by the self-review score (below 7).
