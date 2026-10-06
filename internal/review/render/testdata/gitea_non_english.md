## PR Review 🔍

Pull request: Bitbucket Server #5 — Yeniden deneme ekle (`https://bitbucket.example.com/projects/P/repos/r/pull-requests/5`)

<table>
<tr><td>⏱️&nbsp;<strong>Estimated effort to review</strong>: 4/5 🔵🔵🔵🔵⚪</td></tr>
<tr><td>🧪&nbsp;<strong>PR contains tests</strong></td></tr>
<tr><td>🔒&nbsp;<strong>Security concerns</strong><br><br>

Hassas bilgi ifşası: şifre günlüğe yazılıyor.
</td></tr>
<tr><td>⚡&nbsp;<strong>Recommended focus areas for review</strong><br><br>

<details><summary><strong>Olası hata 🐞</strong> <code>src/main.py L3-4</code>

Döngü, \`sınır\` sıfır olduğunda sonsuza kadar sürer. 日本語のテスト。
</summary>

```python
while True:
    pass
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

- Fark kısaltıldı; kapsam bölümüne bakın.
