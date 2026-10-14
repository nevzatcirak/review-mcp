## PR Review

Pull request: Gitea #12 — Add retry to the fetcher (`https://your-gitea.example/org/repo/pulls/12`)

- Estimated effort to review: 2/5 🔵🔵⚪⚪⚪
- No relevant tests
- No security concerns identified in the reviewed files
- No performance concerns identified in the reviewed files

### Key issues to review

1. **Possible Issue** — [`cmd/app/main.go` L10](https://your-gitea.example/org/repo/pulls/12/files#issuecomment-901)

   The loop never ends.

   ```go
   for {}
   ```

2. **Resource leak** — [`internal/util/strings_util.go` L40](https://your-gitea.example/org/repo/src/commit/abc123/internal/util/strings_util.go#L40)

   The response body is not closed on the error path.

### Coverage

- Included: 1 file
- Omitted: 0 files
- 1 file reviewed, 1 unchanged since the last review
- Inline comments of earlier reviews stay on the pull request.

Unchanged since the last review (1):

- `internal/util/strings_util.go`

### Notes

- 1 finding of earlier reviews on unchanged files is carried forward; it was not reviewed again.

Reviewed commit: `abc123d` (changes since `def5678`)
