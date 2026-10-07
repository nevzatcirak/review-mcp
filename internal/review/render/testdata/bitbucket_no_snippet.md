## PR Review 🔍

Pull request: Gitea #12 — Add retry to the fetcher (`https://your-gitea.example/org/repo/pulls/12`)
Reviewed on 2026-10-06 09:30 UTC at commit `abc123d`.

### ⚡ Recommended focus areas for review

1. **Missing check** — `cmd/app/main.go` L7-9
   - The error from Close is ignored.
   - Snippet note: lines could not be verified against the diff

### 📂 Coverage

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

### 📝 Notes

- The model's answer was cut off by its output limit; the review may be incomplete.
