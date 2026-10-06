## PR Review 🔍

Pull request: Gitea #12 — Add retry to the fetcher (`https://your-gitea.example/org/repo/pulls/12`)
Reviewed on 2026-10-06 09:30 UTC at commit `abc123d`.

| Check | Result |
|---|---|
| ⏱️ Estimated effort to review | 3/5 🔵🔵🔵⚪⚪ |
| 🧪 Tests | PR contains tests |

### 🔒 Security concerns

SQL injection: the query is built by string concatenation.
Use bound parameters instead.

### 🐢 Performance concerns

N+1 access: cmd/app/main.go loads each item with its own query.
Batch the \<ids\> instead.

### ⚡ Recommended focus areas for review

1. **Possible Issue** — [`cmd/app/main.go` L10-12](https://your-gitea.example/org/repo/src/commit/abc123/cmd/app/main.go#L10-L12)
   - The retry loop never stops when \`max\` is 0, so it spins forever.
     See the \`for\` loop.

     ```go
     for {
     	if try() {
     		break
     	}
     }
     ```

2. **Resource leak** — `internal/util/strings_util.go` L40
   - The response body is not closed on the error path.

     ```go
     resp, err := http.Get(u)
     if err != nil {
     	return err
     }
     ```

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

- The diff was shortened to fit the context window; the coverage section lists the files that are incomplete or left out.
