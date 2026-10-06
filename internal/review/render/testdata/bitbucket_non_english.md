## PR Review 🔍

Pull request: Bitbucket Server #5 — Yeniden deneme ekle (`https://bitbucket.example.com/projects/P/repos/r/pull-requests/5`)

| Check | Result |
|---|---|
| ⏱️ Estimated effort to review | 4/5 🔵🔵🔵🔵⚪ |
| 🧪 Tests | PR contains tests |

### 🔒 Security concerns

Hassas bilgi ifşası: şifre günlüğe yazılıyor.

### ⚡ Recommended focus areas for review

| # | Issue | Location |
|---|---|---|
| 1 | Olası hata 🐞 | `src/main.py` L3-4 |

#### 1. Olası hata 🐞

Döngü, \`sınır\` sıfır olduğunda sonsuza kadar sürer. 日本語のテスト。

```python
while True:
    pass
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

- Fark kısaltıldı; kapsam bölümüne bakın.
