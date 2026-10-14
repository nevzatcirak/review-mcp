## PR Review 🔍

> ⚠️ **Partial review: 2 of 5 changed files were reviewed. 3 files were not reviewed (see Coverage); nothing is concluded about them.**

Pull request: Gitea #12 — Add retry to the fetcher (`https://your-gitea.example/org/repo/pulls/12`)
Reviewed on 2026-10-06 09:30 UTC at commit `abc123d`.

<table>
<tr><td>⚡&nbsp;<strong>Recommended focus areas for review</strong><br><br>

<details><summary>1. <strong>Unbounded <code>retry&lt;T&gt;()</code> loop &amp; a lone ` tick</strong> <code>cmd/app/main.go L10-12</code></summary>

The `retry` loop never stops.

```go
for {
	try()
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

Reviewed commit: `abc123d`
