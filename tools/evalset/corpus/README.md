# Evaluation corpus (V-9, X-49)

Synthetic pull requests for measuring review quality. Each case is written
from scratch for this corpus, with generic domains (orders, billing,
inventory) and placeholder hosts (example.com) only. The corpus is text for
the reviewer to read. Nothing builds or runs it, including the self-check
(R-1).

## Layout

```
<case-id>/
  case.toml      metadata and ground truth
  base/          the repository at the base commit
  head/          files the pull request adds or changes (an overlay on base)
  deleted.txt    optional: paths the pull request deletes, one per line
```

`case.toml` follows §1.1 of `docs/plan/v2.9-V9-spec.md`, with two optional
keys (see the WP-21a report):

- `tags` takes the values `large` (a stacked pull request of 30+ files) and
  `body-only` (the change is a method body only, amendment 2026-11-16
  item 5).
- `twin_of` names the cascade case whose clean twin this case is. A twin has
  the same base, and its pull request also updates the callers.

`go.mod` keeps the Go sources of the cases out of the review-mcp module.
`.gitattributes` keeps every file byte for byte on every OS.

`go test ./tools/evalset/...` checks the corpus (§1.3). It reads the files
only and runs nothing in them.

## Cases

The hops column counts the calls between the changed code and the defect,
for cases whose defect is in an unchanged file.

| id | category | language | hops | files (base/changed) | tests |
|---|---|---|---|---|---|
| `java-cascade-overload` | cascade | java | 2 | 17/7 | a new parameter with a defaulting overload, and a caller two calls away |
| `java-clean-overload-twin` | clean (twin) | java | – | 17/9 | the same change with every caller updated two calls away |
| `csharp-cascade-null` | cascade, body-only | csharp | 1 | 16/3 | a lookup returns null instead of throwing |
| `csharp-clean-null-twin` | clean (twin) | csharp | – | 16/6 | the same change with its callers updated |
| `csharp-cascade-template` | cascade | csharp | 1 | 13/4 | a template method of an abstract base class gains a step |
| `csharp-clean-template-twin` | clean (twin) | csharp | – | 13/5 | the same change with the subclasses updated |
| `kotlin-cascade-enum` | cascade | kotlin | 1 | 13/5 | an enum gains a value |
| `kotlin-clean-enum-twin` | clean (twin) | kotlin | – | 13/7 | the same change with the users of the enum updated |
| `typescript-cascade-unit` | cascade, body-only | typescript | 2 | 15/3 | a function changes the unit of its result |
| `typescript-clean-unit-twin` | clean (twin) | typescript | – | 15/5 | the same change with the users updated |
| `go-cascade-interface` | cascade | go | 1 | 12/4 | an optional interface checked with a type assertion changes |
| `go-clean-interface-twin` | clean (twin) | go | – | 12/6 | the same change with the implementations updated |
| `go-cascade-wrapped-error` | cascade, body-only | go | 2 | 10/3 | a sentinel error gets wrapped |
| `go-clean-wrapped-error-twin` | clean (twin) | go | – | 10/5 | the same change with the callers updated |
| `python-cascade-ordering` | cascade, body-only | python | 2 | 10/3 | a listing changes its order |
| `python-clean-ordering-twin` | clean (twin) | python | – | 10/5 | the same change with the users updated |
| `python-local-off-by-one` | local | python | – | 9/3 | a batch loop over the catalog |
| `go-local-error-check` | local | go | – | 7/3 | parsing a CSV price list |
| `java-local-condition` | local | java | – | 10/2 | a combined condition for a shipping fee |
| `csharp-local-resource` | local | csharp | – | 10/3 | a file written per receipt |
| `kotlin-local-boundary` | local | kotlin | – | 8/3 | a quantity threshold for a bulk rate |
| `typescript-local-copy-paste` | local | typescript | – | 8/2 | a second address built like the first |
| `kotlin-clean-rename` | clean | kotlin | – | 13/5 | a class and method rename with every caller updated |
| `python-clean-refactor` | clean | python | – | 10/2 | a refactor that keeps behaviour |
| `go-clean-docs` | clean | go | – | 10/3 | a docs-only change |
| `java-clean-tests` | clean | java | – | 17/3 | a test-only change |
| `typescript-clean-extract-helper` | clean | typescript | – | 15/5 | a dependency-free helper extraction |
| `go-large-cascade` | cascade, large | go | 2 | 37/33 | a stacked change over three Go modules, with a chain through an unchanged function of a changed file |
| `go-clean-large-twin` | clean (twin), large | go | – | 37/35 | the same stacked change with every caller updated |
| `python-security-injection` | security | python | 1 | 10/2 | a request value passed on to the data layer |
| `typescript-security-authz` | security | typescript | – | 10/3 | a new route next to existing ones |
| `go-concurrency-race` | concurrency | go | – | 9/4 | a cache in a shared converter |
| `java-concurrency-lock` | concurrency | java | – | 7/4 | a new path that updates guarded state |
