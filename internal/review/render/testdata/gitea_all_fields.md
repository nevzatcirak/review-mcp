## PR Review 🔍

Pull request: Gitea #12 — Add retry to the fetcher (`https://your-gitea.example/org/repo/pulls/12`)

<table>
<tr><td>⏱️&nbsp;<strong>Estimated effort to review</strong>: 3/5 🔵🔵🔵⚪⚪</td></tr>
<tr><td>🧪&nbsp;<strong>PR contains tests</strong></td></tr>
<tr><td>🔒&nbsp;<strong>Security concerns</strong><br><br>

SQL injection: the query is built by string concatenation.
Use bound parameters instead.
</td></tr>
<tr><td>🐢&nbsp;<strong>Performance concerns</strong><br><br>

N+1 access: cmd/app/main.go loads each item with its own query.
Batch the &lt;ids&gt; instead.
</td></tr>
<tr><td>⚡&nbsp;<strong>Recommended focus areas for review</strong><br><br>

<details><summary><a href='https://your-gitea.example/org/repo/src/commit/abc123/cmd/app/main.go#L10-L12'><strong>Possible Issue</strong></a> <code>cmd/app/main.go L10-12</code>

The retry loop never stops when \`max\` is 0, so it spins forever.
See the \`for\` loop.
</summary>

```go
for {
	if try() {
		break
	}
}
```

</details>

<details><summary><strong>Resource leak</strong> <code>internal/util/strings_util.go L40</code>

The response body is not closed on the error path.
</summary>

```go
resp, err := http.Get(u)
if err != nil {
	return err
}
```

</details>

</td></tr>
</table>

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
