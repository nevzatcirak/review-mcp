## PR Review

Pull request: Gitea #12 — Add retry to the fetcher (`https://your-gitea.example/org/repo/pulls/12`)

- Estimated effort to review: 2/5 🔵🔵⚪⚪⚪
- PR contains tests
- No security concerns identified
- No performance concerns identified

### Key issues to review

1. **Possible Issue** — [`cmd/app/main.go` L10](https://your-gitea.example/org/repo/pulls/12/files#issuecomment-901)

   The loop never ends.

   ```go
   for {}
   ```

2. **Outside the diff** — [`internal/util/strings_util.go` L80-82](https://your-gitea.example/org/repo/src/commit/abc123/internal/util/strings_util.go#L80)

   An unchanged helper no longer fits.
   See the caller.

   Snippet note: lines could not be verified against the diff

3. **Leak** — [`cmd/app/main.go` L12](https://your-gitea.example/org/repo/src/commit/abc123/cmd/app/main.go#L12)

   The body is not closed.

   ```go
   resp, _ := get()
   ```

### Publishing

- Overview: updated in place (`https://your-gitea.example/org/repo/pulls/12#issuecomment-55`)
- Inline comments: 1 posted, 1 failed, 1 not on a changed line

### Coverage

- Included: 3 files (1 in part only, clipped to fit the context window)
- Omitted: 4 files

Included in part only (clipped to fit the context window) (1):

- `internal/big/generated_like.go`

Left out to fit the context window (added files) (1):

- `docs/new.md`

Left out to fit the context window (modified files) (1):

- `internal/other/other.go`

Skipped: `binary` (1):

- `assets/logo.png`

Filtered: `lockfile_or_minified` (1):

- `go.sum`

### Notes

- 1 finding could not be placed on a changed line and is listed in the overview only.
