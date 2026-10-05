# P1 — Skeleton: Specification

| | |
|---|---|
| Phase | P1 (phase-plan.md) |
| Work packages | WP-PR-1a (module, layout, CI, logging), WP-PR-1b (config layer), WP-PR-1c (MCP stdio server + `server_info`) |
| Binding inputs | `docs/design/v1-design-decisions.md` — especially §5 (config table), §6 (tool surface), DQ-23…DQ-26, X-2, X-6, X-8 |
| Execution | Sequential: 1a → 1b → 1c. Each package is implemented by a Sonnet subagent, then reviewed by the architect and committed on branch `p1-skeleton` before the next starts. |
| Acceptance | Architect pre-acceptance (scripted stdio JSON-RPC smoke test) → user live acceptance in a real MCP client → merge `p1-skeleton` into `main`. |

---

## 0. Rules for every work package (preamble given to each subagent)

1. Repository: `/home/claude/review-mcp`; module path `github.com/nevzatcirak/review-mcp`.
2. Everything written (code, comments, docs, test names, error/log messages) is **English**. Examples use placeholder hosts only (`example.com`, `your-gitea.example`, `bitbucket.example.com`). No organization names.
3. **Do not commit, do not push, do not create branches.** Leave changes in the working tree. Your final message is a report (see §0.9).
4. **DESIGN-QUESTION contract:** when the spec is ambiguous or silent on a decision with real consequences, do not invent. Choose the most conservative option that keeps the code compiling, mark it in code with `// DESIGN-QUESTION: <question> — chose <x> because <y>`, and list it in the report.
5. **Dependencies:** only the modules listed in the package spec. Anything else requires a DESIGN-QUESTION instead of an import.
6. **Quality gates (all must pass before reporting):** `gofmt -l .` empty; `go vet ./...`; `golangci-lint run ./...`; `go test -race ./...`.
7. **Canary rule:** every test that guards a security or correctness property marked **[canary]** below must be proven to catch the failure: temporarily break the guarded behavior (or feed a deliberately wrong case), show the test fails, restore. Report for each canary: what you broke and the failing test name.
8. **Leak-first rules (X-8):** stdout belongs to the MCP protocol — never write to it except through the MCP transport. Logs go to stderr via `log/slog`. Never log secrets, `Authorization` headers, or prompt/response/diff bodies.
9. **Report format:** files added/changed; how each requirement was met (by section number); test list; canary proofs; DESIGN-QUESTIONs; anything you could not verify (say "not verified" — never claim unverified results).

---

## 1. WP-PR-1a — Module, layout, CI, logging

### 1.1 Module
- `go.mod`: module `github.com/nevzatcirak/review-mcp`; `go` directive **1.25** (required by the MCP Go SDK used in 1c).
- No dependencies in this package.

### 1.2 Directory layout (create only what this package needs; later packages fill the rest)
```
cmd/review-mcp/main.go        entry point
internal/version/version.go   build metadata
internal/logging/logging.go   slog setup + redaction helpers
internal/config/              (WP-PR-1b)
internal/tools/               (WP-PR-1c) transport-agnostic tool logic
internal/mcpserver/           (WP-PR-1c) MCP SDK wiring
```

### 1.3 `internal/version`
- Package-level `var Version = "dev"` and `var Commit = ""`, overridable via `-ldflags -X`.
- `func Info() BuildInfo` returning `{Version, Commit, GoVersion}` (`GoVersion` from `runtime.Version()`); when `Commit` is empty, fall back to `debug.ReadBuildInfo()` `vcs.revision` if present.

### 1.4 `cmd/review-mcp/main.go` (1a version)
- Stdlib `flag` only (no CLI framework).
- Subcommands by first argument: none or `stdio` → run the stdio server (in 1a: a placeholder that logs "stdio server not yet implemented" to stderr and exits 1; 1c replaces it); `version` → print `review-mcp <version> (<commit>) <go version>` to **stdout** and exit 0 (allowed: not in MCP mode); `serve` → stderr "serve mode is not available in this version" and exit 2; anything else → usage on stderr, exit 2.

### 1.5 `internal/logging`
- `func New(w io.Writer, level slog.Level) *slog.Logger` — text handler writing to `w` (main passes `os.Stderr`).
- `func ParseLevel(s string) (slog.Level, error)` — accepts `debug|info|warn|error` (case-insensitive); anything else is an error.
- `func RedactURL(raw string) string` — returns the URL with userinfo removed and any query-parameter values replaced by `REDACTED`; on parse failure returns the literal string `"<unparseable URL>"` (never the input).
- `func RedactText(s string) string` — port of upstream's credential redaction idea (see porting map F.1 `redact_credentials`): replaces URL userinfo (`scheme://user:pass@` → `scheme://REDACTED@`) and the value of `Authorization:` / `Authorization=` headers (`Bearer x`, `token x`, `Basic x`, bare values) with `REDACTED`. Write the regexes from scratch.
- Tests: table-driven for both functions, including **[canary]** cases: bearer token, Gitea `token` scheme, Basic, URL userinfo, query `?access_token=`.

### 1.6 Lint and CI
- `.golangci.yml` in **golangci-lint v2** format. Linters: `errcheck, govet, staticcheck, unused, ineffassign, misspell, gosec, bodyclose, errorlint`; formatters: `gofmt, goimports`. Do not disable checks globally to get green; justified per-line `//nolint:<linter> // reason` only.
- `.github/workflows/ci.yml`: on `push` and `pull_request`; `ubuntu-latest`; `actions/checkout`, `actions/setup-go` with `go-version-file: go.mod`; steps: `go vet ./...`, `go test -race ./...`, golangci-lint via the official action pinned to linter **v2.5.0**. Use current major versions of the actions; state in the report which versions you used and that they were not executed locally (CI cannot run here).
- `Makefile`: `build` (with `-ldflags` setting version/commit from `git describe --tags --always --dirty` / `git rev-parse --short HEAD`), `test` (`go test -race ./...`), `lint`, `fmt`.
- README: add a short **Development** section (build, test, lint commands). Do not change the rest.

---

## 2. WP-PR-1b — Configuration layer (`internal/config`)

Implements decisions DQ-3, DQ-4, DQ-23, DQ-24, DQ-26, X-2 (enablement part) and the §5 table **exactly** — key names, env names, defaults, validation. The §5 table is the source of truth; if this spec and §5 disagree, stop and raise a DESIGN-QUESTION.

### 2.1 Dependencies
- `github.com/BurntSushi/toml` (latest v1.x) — the only new module.

### 2.2 Types
- One `Config` struct mirroring the §5 sections: `LLM`, `Gitea`, `BitbucketServer`, `Output`, `Diff`, `Ignore`, `Review`, `Ask`, `Log`, plus `Secrets`.
- Optional values without defaults (`llm.max_output_tokens`, `llm.temperature`, `llm.seed`, `llm.reasoning_effort`) are represented so that "unset" is distinguishable from zero (pointer or `Optional[T]` — your choice, consistent across fields). DQ-26: unset means "do not send".
- **Secret type:** `type Secret struct{ /* unexported */ }` with `func (s Secret) Reveal() string`, `IsSet() bool`, and redacting implementations of `String`, `GoString`, `Format` (all verbs), `LogValue` (`slog.LogValuer`), `MarshalJSON`, `MarshalText` — each yields `"[REDACTED]"` when set and `"[UNSET]"` when unset. `Secrets` holds `LLMAPIKey`, `GiteaToken`, `BitbucketServerToken`.

### 2.3 Loading
- Entry point: `func Load(src Source) (*Config, *Report, error)` where `Source` abstracts the process environment (`LookupEnv`, `Environ`) and file reading, so tests never touch real env or disk. `func LoadFromOS() (*Config, *Report, error)` wraps it.
- Precedence (low → high): compiled defaults → TOML file → environment. Per-call tool arguments are **not** part of this package.
- **Compiled defaults:** exactly the "Default" column of §5. No defaults for URLs, model, context window, temperature, seed, reasoning effort, max output tokens.
- **File:** read only if `REVIEW_MCP_CONFIG` is set (non-empty). Missing/unreadable file → error naming the path. Values present in the file override defaults *only for keys that appear in the file* (use TOML metadata to know which keys were defined).
  - Unknown keys → error `unknown config key "<key>" in <path>`.
  - Secret keys (`llm.api_key`, `gitea.token`, `bitbucket_server.token`, and any key whose last segment matches `(?i)^(token|secret|password|api_key|apikey)$`) → error `secret "<key>" must not be set in the config file; use environment variable <ENV>` (name the env var when known). **Never include the value.** **[canary]**
- **Environment:** a fixed, hand-written table mapping every §5 env name to its key. Parsing is strict:
  - bool: `true|false|1|0` (case-insensitive); int: base-10 `strconv.ParseInt`; float: `strconv.ParseFloat`; lists: comma-separated, items trimmed, empty items rejected; a variable that is **set but empty** sets an empty list for list keys and is an error for scalar keys.
  - Malformed values → error naming the variable (and the expected type), never echoing the value for secret variables; for non-secret variables echoing the value is allowed.
  - Any other `REVIEW_MCP_*` variable not in the table → warning `unknown environment variable <NAME> (ignored)`. **[canary]**
- All errors from file + env + validation are aggregated: `Load` returns every problem at once (a `*ValidationError` with `Problems []string`, `Error()` joins them), not just the first.

### 2.4 Validation (after layering)
- `llm.base_url`, `gitea.base_url`, `gitea.web_url`, `bitbucket_server.base_url`: scheme `http`/`https`, non-empty host, **no userinfo** (error: `credentials must not be embedded in <key>`), no fragment. Path allowed (context paths, `/v1`). Normalize by trimming a trailing `/`. Error messages show URLs only through `logging.RedactURL`. **[canary]** for userinfo rejection and for a URL containing a password never appearing in any error text.
- `llm.model` non-empty; `llm.context_window` set and ≥ 4096.
- `llm.max_output_tokens` if set: > 0 and < `llm.context_window`.
- `llm.temperature` if set: 0 ≤ t ≤ 2. `llm.reasoning_effort` if set: non-empty.
- `llm.timeout_seconds` 1–3600; `llm.max_retries` 0–5; `llm.token_estimate_factor` 0–2.
- Providers (X-2): a provider is **enabled** iff its `base_url` is set. At least one provider must be enabled. Enabled provider without its token → error naming the env var. Token set for a non-enabled provider → warning. `gitea.web_url` set without `gitea.base_url` → error. `*.ca_cert` if set must be a readable regular file (through `Source`). `*.insecure_skip_verify = true` → warning `TLS verification disabled for <provider>`.
- `llm.base_url`/`llm.model`/`llm.context_window` and the LLM API key are required.
- `output.language`: matches `^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8})*$`.
- `diff.extra_lines_before`/`after`: 0–10 (out of range is an **error**, never a silent clamp). `diff.skip_extend_extensions`: each item starts with `.`. `diff.large_patch_policy` ∈ {`clip`,`skip`}. `diff.max_description_tokens`, `diff.max_commits_tokens` ≥ 1. `diff.max_files_full_content` ≥ 1. `diff.max_file_bytes` ≥ 1024. `diff.max_diff_bytes` ≥ `diff.max_file_bytes`.
- `diff.ignore_generated_frameworks` and `ignore.glob`: accepted as non-empty strings in P1; semantic validation is added in P3 (when the embedded tables and glob engine arrive). Add a `// DESIGN-QUESTION`-free comment saying so.
- `ignore.regex`: each compiles with Go `regexp` (RE2); compile failure → error naming the pattern index.
- `review.max_findings` 1–20. `log.level` via `logging.ParseLevel`.

### 2.5 Report and summary
- `Report` carries `Warnings []string` and, per key, the **source** of the effective value (`default` | `file` | `env`).
- `func (c *Config) Summary(r *Report) Summary` returns a JSON-serializable, secret-free view: every non-secret effective value with its source, secrets as `"set"`/`"unset"`, enabled providers (`kind`, `base_url`), warnings. URLs pass through `RedactURL`.

### 2.6 Tests (table-driven; no real env, no real files outside `t.TempDir()`)
- Defaults applied when nothing set; required-missing case lists **all** missing keys in one error.
- Precedence: default < file < env, per key, with sources reported correctly.
- Every env variable in the table parses (generated test iterating the table — catches table/struct drift). **[canary]**
- File: unknown key error; secret-in-file error without value **[canary]**; partial file leaves other defaults intact.
- Validation: one failing case per rule above.
- Secret redaction: `fmt` with `%v %+v %#v %s %q`, `slog` (text and JSON handlers), `encoding/json` of the whole `Config` and of `Summary` — none contains the secret value. **[canary]**
- Unknown `REVIEW_MCP_*` variable → warning, not error.

---

## 3. WP-PR-1c — MCP stdio server and `server_info`

### 3.1 Dependencies
- `github.com/modelcontextprotocol/go-sdk` — latest stable **v1.x** at implementation time (v1.8.0 was the latest stable on 2026-10-06; verify with `go list -m -versions`). Report the exact version used. No other new modules (the SDK's own transitive dependencies are fine).

### 3.2 Architecture
- `internal/tools`: transport-agnostic tool logic. **Must not import the MCP SDK.** For P1: `serverinfo.go` with `func ServerInfo(cfg *config.Config, rep *config.Report, loadErr error) ServerInfoResult` and `func RenderServerInfoMarkdown(ServerInfoResult) string`.
- `internal/mcpserver`: the only package importing the SDK. `func New(deps Deps) *mcp.Server` registers every tool; `func RunStdio(ctx context.Context, s *mcp.Server) error` runs it over the SDK's stdio transport. `serve` mode (P6) will reuse `New` with a different transport.
- Implementation identity reported to clients: name `review-mcp`, version from `internal/version`.

### 3.3 Startup behavior ("degraded start")
- `main` loads config. If loading/validation fails, the server **still starts**: the aggregated error is logged to stderr, and `server_info` reports `status: "config_invalid"` with the full list of problems. (Rationale: a server that exits immediately gives MCP-client users only an opaque "server failed" message; a degraded server lets them call `server_info` and see exactly what to fix.) Later tools will refuse to run while config is invalid.
- Logger level: from config when valid, else `info`. Startup log line (stderr, info): version, enabled providers, warning count. Each warning logged at warn.
- Shutdown: context canceled on SIGINT/SIGTERM (`signal.NotifyContext`); stdin EOF ends the session cleanly with exit code 0.

### 3.4 `server_info` tool
- No input arguments. Description: one sentence ("Reports the review-mcp version, enabled providers, and the effective non-secret configuration, including configuration problems.").
- Result: text content = `RenderServerInfoMarkdown` output (portable markdown per DQ-16: headings, lists, code spans; **no raw HTML**) **and** `structuredContent` with a declared output schema (DQ-6 pattern), shape:
  `{ name, version, commit, go_version, status: "ok"|"config_invalid", problems: [string], warnings: [string], providers: [{kind, base_url}], config: <Summary> }`.
- Annotations: read-only, idempotent, not destructive, closed-world (set what the SDK supports; report what you set).

### 3.5 Tests
- `internal/tools`: unit tests for valid and invalid config results and markdown rendering (no HTML tags: assert no `<` followed by a letter outside code spans).
- `internal/mcpserver`: in-memory client↔server test using the SDK's in-memory transport: initialize, `tools/list` contains exactly `server_info`, `tools/call server_info` returns both text and structured content matching the schema.
- **[canary] Leak test:** configure all three secrets with distinctive values; run a full in-memory session (list + call) with stderr captured; assert none of the secret values appears in tool results (text and structured), in captured logs, or in the degraded-start error path (invalid config that also carries secrets).
- **End-to-end stdio test** (skipped under `-short`): build the binary into `t.TempDir()`, start it with a controlled environment (secrets included), exchange newline-delimited JSON-RPC over its stdin/stdout — `initialize`, `notifications/initialized`, `tools/list`, `tools/call` — assert responses, assert **stdout contains only JSON-RPC messages** **[canary]** (prove by adding a stray `fmt.Println` temporarily), close stdin and assert clean exit 0.

### 3.6 Docs
- `docs/getting-started.md` (new): build from source (`go install github.com/nevzatcirak/review-mcp/cmd/review-mcp@<ref>`), minimal environment (the required §5 variables with placeholder values), and an example MCP-client registration showing **one generic JSON `mcpServers` example** using env references, plus a note that clients differ in syntax. Placeholder hosts only; never literal tokens.

---

## 4. Architect review checklist (applied after each package, before committing)

- Diff read in full; no file outside the package's scope changed.
- Gates re-run by the architect: `gofmt -l .`, `go vet ./...`, `golangci-lint run ./...`, `go test -race ./...`.
- At least one reported canary re-proven independently by the architect.
- §5 table cross-checked line by line against the env table and defaults in code (1b).
- DESIGN-QUESTIONs answered before commit (code updated or comment replaced by the decision).
- Each commit verified green in isolation in a detached-HEAD worktree before push.

## 5. Planned commit sequence (branch `p1-skeleton`)

1. `build: initialize Go module, project layout, lint and CI configuration`
2. `feat(logging): add stderr slog setup and credential redaction helpers`
3. `feat(config): add layered configuration with strict env/TOML loading and validation`
4. `feat(server): add MCP stdio server with server_info tool and degraded start`
5. `docs: add getting-started guide for building and registering the server`

(Final split may change during review; each commit must build and pass on its own.)

## 6. Acceptance

- **Architect pre-acceptance:** the e2e stdio test plus a manual scripted session against the built binary — `initialize`, `tools/list`, `server_info` with valid config and with deliberately invalid config — transcripts attached to the PR.
- **User live acceptance (gate):** register the binary in a real MCP client (opencode or Claude Code), confirm `server_info` is listed and callable, secrets appear only as `set`/`unset`, and a deliberately broken config shows the problems list. Then `p1-skeleton` is merged into `main`.
