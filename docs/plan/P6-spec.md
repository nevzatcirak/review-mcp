# P6 — `serve` mode, packaging and distribution: Specification

| | |
|---|---|
| Phase | P6 (phase-plan.md) |
| Work packages | WP-PR-6a (per-request credentials and the `serve` transport) · WP-PR-6b (release pipeline and third-party license bundle) · WP-PR-6c (npm distribution) · WP-PR-6d (setup guide, README, changelog, Dockerfile) |
| Binding inputs | `docs/design/v1-design-decisions.md`: X-2, X-6, X-8, §5, §6. Phase plan P6 and its 2026-10-06 amendment (consolidated V1 acceptance). |
| Builds on | P1 (config, `Secret`, `mcpserver.New`), P2 (resolver, providers), P4 (LLM client), P5 (`pr_ask`). Branch `p6-release` from `main` after P5 is merged. |
| Protocol | Same as P1–P5: the P1 spec §0 preamble applies. Report one PR comment per package. `[architect review]` comments are the owner's instructions. |
| Model guidance | **Opus for WP-PR-6a**: it is security design with concurrency. Sonnet for 6b–6d. |
| New Go modules allowed | **None.** New CI actions are allowed only when pinned by full commit SHA, with the version in a trailing comment. |

P6 turns the server into something a stranger can install and run. It has two halves:

- **`serve`:** a second transport for shared deployments. Identity travels in per-request HTTP headers, never in the server's environment.
- **Distribution:** platform binaries on GitHub Releases, an npm wrapper that needs no install script, a third-party license bundle, and a setup guide.

The phase ends with the first release candidate, `v1.0.0-rc.1`. The owner runs the consolidated acceptance (`docs/plan/v1-acceptance.md`) on it.

## 0. Entry criteria

1. **SDK request-data path (E1).** Before any 6a code, prove in a throwaway test how per-HTTP-request data reaches a tool handler in the MCP Go SDK v1.8.0's streamable HTTP handler in stateless mode. Candidates:
   - the request's `Extra.Header`;
   - the HTTP request context propagating into the handler's `ctx`.

   The test sends two concurrent calls with different header values. Each handler must observe only its own value. Report:
   - which mechanism works;
   - the SDK source lines that guarantee it;
   - the test output.

   If neither works without session state, stop and write a DESIGN-QUESTION.
2. **SDK logging audit carried forward.** The P4 audit covered stdio. Repeat it for the HTTP handler: at debug level, the SDK must log no header, argument or result. If it does, apply the P4 filter.

## 1. WP-PR-6a — Per-request credentials and `serve`

### 1.1 The credential seam
- Today providers and the LLM client read their secrets from `cfg.Secrets`. Keep that single read point. Add `func (c *Config) WithSecrets(s Secrets) *Config`:
  - it returns a shallow copy with the secrets replaced;
  - the copy's other fields are shared and read-only, and a test asserts that the original is unchanged.
- Each tool call gets an effective config from one function, `deps.ConfigFor(ctx, req)`:
  - **stdio:** returns the startup config. No change in behaviour.
  - **serve:** returns `cfg.WithSecrets(secretsFromRequest)`. The resolver and the LLM client are then built from that config for the call, as they already are per request.
- **Lifetime.** Credentials live only in the request-scoped config. They are never stored:
  - in a struct field;
  - in a cache;
  - in a map keyed by anything;
  - in a package variable.

  The architect checks this by code review. The canary in §1.6 #1 checks it behaviourally.

### 1.2 Configuration (new rows for the decisions doc §5)

| Key (TOML) | Env | Default | Notes |
|---|---|---|---|
| `serve.listen` | `REVIEW_MCP_SERVE_LISTEN` | `127.0.0.1:8787` | `host:port`. The `--listen` flag overrides it. |
| `serve.tls_cert` | `REVIEW_MCP_SERVE_TLS_CERT` | — | PEM path. Set together with `tls_key`. |
| `serve.tls_key` | `REVIEW_MCP_SERVE_TLS_KEY` | — | PEM path. |
| `serve.allow_insecure_http` | `REVIEW_MCP_SERVE_ALLOW_INSECURE_HTTP` | false | Required to bind a non-loopback address without TLS, for example behind a TLS-terminating proxy. Startup warning when true. |
| `serve.llm_key_source` | `REVIEW_MCP_SERVE_LLM_KEY_SOURCE` | `header` | `header` \| `server` |
| `serve.allowed_origins` | `REVIEW_MCP_SERVE_ALLOWED_ORIGINS` | (empty) | Comma-separated exact origins. |
| `serve.max_concurrent_calls` | `REVIEW_MCP_SERVE_MAX_CONCURRENT_CALLS` | 4 | Range 1–64. |
| — | `REVIEW_MCP_SERVE_ACCESS_TOKEN` | — | **Secret.** Required when `llm_key_source = server`. When set, it is enforced in either mode. |

**Mode-aware validation.** Validation takes the mode, stdio or serve. All violations are still reported together, token-free, as they are today.

- **stdio:** unchanged. The `serve.*` keys are ignored, and setting them is not an error.
- **serve:**
  - `REVIEW_MCP_GITEA_TOKEN` and `REVIEW_MCP_BITBUCKET_SERVER_TOKEN` must be **unset**. Fixed sentence: "serve mode takes provider tokens from request headers; unset REVIEW_MCP_GITEA_TOKEN". This prevents a shared server from acting under one person's identity for everyone.
  - With `llm_key_source = header`, `REVIEW_MCP_LLM_API_KEY` must be unset.
  - With `llm_key_source = server`, `REVIEW_MCP_LLM_API_KEY` and `REVIEW_MCP_SERVE_ACCESS_TOKEN` are both required.
  - A non-loopback `listen` host needs TLS (both files readable at startup) or `allow_insecure_http = true`. Loopback means `127.0.0.0/8`, `::1` or `localhost`. An empty host or `0.0.0.0` counts as non-loopback.
  - Each enabled provider still needs its `base_url` (X-2 is unchanged). Only its token moves to the headers.

### 1.3 Request credentials (header contract)

| Header | Carries |
|---|---|
| `X-Review-MCP-Gitea-Token` | Gitea token |
| `X-Review-MCP-Bitbucket-Server-Token` | Bitbucket Server token |
| `X-Review-MCP-LLM-API-Key` | LLM key, used only when `llm_key_source = header` |
| `Authorization: Bearer <access token>` | Server access token, when one is configured |

- **Value checks:**
  - Values are trimmed.
  - Empty means absent.
  - A value longer than 4096 bytes, or one containing anything other than visible ASCII, is rejected with "malformed credential header: <header name>". The value is never echoed.
- **Missing credential:** when a call needs a credential the request lacks, it fails with a new X-6 class, `credentials_missing`, and no outbound request is made:
  - provider token: "no Gitea token in this request: set the X-Review-MCP-Gitea-Token header in your MCP client configuration";
  - LLM key: the same sentence shape.

  The check happens after URL resolution and before any provider or LLM I/O.
- **`server_info` in serve mode** adds:
  - `transport: serve`;
  - the listen address;
  - `llm_key_source`;
  - for this request, each credential header shown as `set` or `unset`, never its value.

### 1.4 HTTP server (`internal/serve`)
- **Endpoints:**
  - `POST /mcp`: the SDK's streamable HTTP handler in **stateless** mode. There are no sessions, so no server-side state outlives a request.
  - `GET /healthz`: returns `200 ok`. It is unauthenticated and carries no other information.
  - Everything else returns 404.
- **Middleware order** on `/mcp`. Every rejection uses a fixed body, and no tool code runs before all checks pass.
  1. **Body limit:** 1 MiB, enforced with `http.MaxBytesReader`.
  2. **Host check:** when listening on loopback, the `Host` header must be a loopback name or IP with the configured port. Anything else gets 403. This is the DNS-rebinding defence.
  3. **Origin check:** a request carrying an `Origin` header is rejected with 403 unless that origin is listed exactly in `allowed_origins`. Requests without `Origin` (ordinary MCP clients) pass.
  4. **Access token:** when one is configured, it is compared with `subtle.ConstantTimeCompare`. A missing or wrong token gets 401 with `WWW-Authenticate: Bearer`.
  5. **Credential extraction**, through the E1 mechanism.
  6. **Concurrency gate:** a semaphore sized by `max_concurrent_calls`, applied to tool calls only. When it is full, the call fails at once with a new X-6 class, `server_busy`: "the server is busy: retry shortly".
- **`http.Server` settings:**
  - `ReadHeaderTimeout` 10 s;
  - `IdleTimeout` 120 s;
  - `MaxHeaderBytes` 64 KiB;
  - no `WriteTimeout`, because long LLM calls are bounded by the existing per-call deadlines;
  - TLS minimum version 1.2.
- **Shutdown:** on SIGINT or SIGTERM, stop accepting connections and drain for up to 30 s, then exit 0.
- **Logging (X-8):** one access line per request at `info`, with method, path, status, duration and response bytes. Never log headers, query strings or bodies. The remote address is logged at `debug` only.

### 1.5 CLI
- `review-mcp serve [--listen host:port]` replaces the P1 placeholder.
  - Startup prints one `info` line with the listen URL, the TLS state and the key source.
  - Validation errors exit 2, as in stdio.
- `review-mcp` and `review-mcp stdio` are unchanged.

### 1.6 Canaries (each proven by removing or breaking the guarded code)
1. **No cross-talk:**
   - Setup: 32 concurrent `pr_review` dry-run style calls against a fake Gitea and a fake LLM. Each call carries a distinct token and a PR number paired with it.
   - Check: the fake servers see every token only with its paired PR.
   - Break: store the extracted credentials in a shared variable. The test must fail.
2. **Env tokens refused:** serve with `REVIEW_MCP_GITEA_TOKEN` set fails validation and opens no listener.
3. **Missing header:** a call without the Gitea header returns `credentials_missing`, and the fake provider sees zero requests.
4. **Insecure bind:** `listen = 0.0.0.0:8787` without TLS and without `allow_insecure_http` fails validation.
5. **Origin and Host:**
   - `Origin: https://attacker.example` gets 403;
   - `Host: rebind.example` on a loopback listener gets 403;
   - in both cases no tool runs.
6. **Access token:** a wrong token gets 401, and no tool runs.
7. **End-to-end leak:**
   - Setup: debug level, every credential header set to a distinct marker, a provider error forced, and `server_info` called.
   - Check: no marker appears in stderr, in any HTTP response body, or in any error text.
8. **stdio regression:** the existing stdio e2e tests pass unchanged.

## 2. WP-PR-6b — Release pipeline and license bundle

### 2.1 Third-party license bundle
- Add `tools/licensebundle/`, a `main` package that uses the standard library only and is not shipped. It:
  1. lists the modules actually linked into `./cmd/review-mcp`, using `go list -deps -f '{{with .Module}}…{{end}}'` and excluding the main module;
  2. reads each module's `LICENSE*`, `LICENCE*`, `COPYING*` and `NOTICE*` files from `Module.Dir`;
  3. checks every module against `tools/licensebundle/allowlist.txt`. Each line is `module SPDX`. The permitted SPDX set is MIT, BSD-2-Clause, BSD-3-Clause, Apache-2.0 and ISC; anything else needs an architect decision;
  4. writes `THIRD_PARTY_LICENSES` at the repo root: per module, its path, version, SPDX id and the full license and notice texts, sorted by path.
- **Failure cases:** the generator fails when:
  - a linked module has no allowlist entry;
  - a module has no license file;
  - a module's SPDX id is outside the permitted set.
- **Evidence for review:** the report lists each allowlist entry with the first non-empty line of its license file.
- **Tokenizer data:** report where the `o200k_base` vocabulary embedded by `tiktoken-go/tokenizer` comes from and under which license. If the module's own license does not cover it, add a NOTICE entry and raise a DESIGN-QUESTION.
- **Make targets:**
  - `make licenses` regenerates the bundle;
  - `make licenses-check` regenerates it into a temporary file and diffs it against the committed one. CI runs this check.
- **[canary]** Add a fake entry to the allowlist, or remove a real one. `licenses-check` must fail.

### 2.2 GoReleaser
- **`.goreleaser.yaml`** (GoReleaser v2 schema):
  - Builds:
    - `CGO_ENABLED=0`, `-trimpath`;
    - `ldflags` set `-s -w`, `-buildid=`, `version.Version={{.Version}}` and `version.Commit={{.FullCommit}}`;
    - `mod_timestamp: {{.CommitTimestamp}}`.
  - Targets: linux, darwin and windows, each for amd64 and arm64.
  - Archives:
    - `tar.gz`, with `zip` on windows;
    - each contains the binary, `LICENSE`, `NOTICE`, `THIRD_PARTY_LICENSES` and `README.md`.
  - `checksums.txt` uses SHA-256.
  - Release notes come from the matching `CHANGELOG.md` section.
  - A tag with a pre-release suffix is marked as a pre-release.
- **`release.yml`** runs on push of tags `v*`:
  - permissions: `contents: write`, `id-token: write`, `attestations: write`;
  - jobs:
    1. checkout with full history;
    2. `setup-go` from `go.mod`;
    3. `make licenses-check`;
    4. `go test -race ./...`;
    5. GoReleaser release;
    6. `actions/attest-build-provenance` over the archives and `checksums.txt`;
    7. then the npm job (§3.4).
- **PR CI additions:**
  - `goreleaser release --snapshot --clean`;
  - the binary from the snapshot reports `version` containing the snapshot version;
  - `make licenses-check`.
- **[canary]** Break the `ldflags` variable path. The snapshot `version` check must fail.

## 3. WP-PR-6c — npm distribution

### 3.1 Packages (all versions in lockstep with the git tag, without the leading `v`)
- **Main package: `@nevzatcirak/review-mcp`.** The scope is the owner's to confirm at the owner gate; do not hard-code it beyond one constant in the build script.
  - `bin: { "review-mcp": "bin/review-mcp.js" }`;
  - `optionalDependencies`: the six platform packages, pinned to the exact same version;
  - `engines.node >= 18`;
  - `license: MIT`;
  - `files`: the launcher, `LICENSE`, `NOTICE`, `THIRD_PARTY_LICENSES` and `README.md`.
- **Platform packages:** `@nevzatcirak/review-mcp-<platform>-<arch>`, where:
  - `<platform>` is one of `linux`, `darwin` or `win32`;
  - `<arch>` is `x64` or `arm64`;
  - the names follow Node's `process.platform` and `process.arch`.

  Each has matching `os` and `cpu` fields, `bin/review-mcp[.exe]` taken from the GoReleaser output, and the same license files.
- **No install scripts anywhere.** Installation must work with `--ignore-scripts`. There is no network access at install time other than the npm registry.

### 3.2 Launcher (`npm/review-mcp/bin/review-mcp.js`, plain Node, zero dependencies)
- **Binary path:**
  - When `REVIEW_MCP_BINARY` is set, use that path.
  - Otherwise resolve `@nevzatcirak/review-mcp-${process.platform}-${process.arch}/bin/review-mcp[.exe]` with `require.resolve`.
- **Execution:**
  - spawn the binary with `stdio: 'inherit'` and the arguments unchanged;
  - forward SIGINT, SIGTERM and SIGHUP to the child;
  - exit with the child's exit code, or re-raise its signal.
  - If the binary is not executable, apply a best-effort `chmod 0o755` once.
- **stdout purity:** the launcher itself writes **nothing to stdout**, because stdout is the MCP channel. All launcher messages go to stderr.
- **Platform package missing:** a fixed stderr message naming the expected package and suggesting a reinstall without `--omit=optional`; exit 1.

### 3.3 Tests
- **`node:test` unit tests:**
  - platform-to-package mapping;
  - the missing-package message;
  - signal forwarding, with a fake child.
- **[canary] stdout purity:**
  - Setup: run the launcher against a fake binary that writes a known byte sequence.
  - Check: stdout equals exactly those bytes.
  - Break: add a `console.log` to the launcher. The test must fail.
- **CI smoke job** (ubuntu, macos and windows runners):
  1. build the npm tarballs from the snapshot output with `npm pack`;
  2. install them into an empty directory with `--ignore-scripts`;
  3. run `npx review-mcp version`;
  4. run a stdio `initialize` → `tools/list` round trip through the launcher, expecting the five v1 tools (`server_info`, `pr_comments`, `pr_comment_reply`, `pr_review`, `pr_ask`; corrected at the P6 review).

### 3.4 Publishing (in `release.yml`, after GoReleaser)
- `npm/scripts/build-packages.mjs` assembles the seven package directories from `dist/` and sets the version from the tag.
- Publish the platform packages first, then the main package:
  - `npm publish --provenance --access public`;
  - `--tag next` for pre-releases, `latest` otherwise.
- Authentication is `NPM_TOKEN` from repository secrets. Never echo it, and never pass it on the command line.
- **Republishing guard:** a version that already exists on the registry fails that publish step and stops the job. There is no force or unpublish logic.

## 4. WP-PR-6d — Docs and container

### 4.1 `docs/setup.md` (the user's path from nothing to a first review)
1. **Install.**
   - npm (`npx -y @nevzatcirak/review-mcp`);
   - a release archive, with checksum verification;
   - `go install …/cmd/review-mcp@<tag>`, which requires the Go version in `go.mod`. This closes backlog F-1.
2. **Token checklist per provider.**
   - **Gitea:** where to create a token, and the minimal scopes:
     - read-only use (`pr_review` and `pr_ask` without `publish`, `pr_comments`);
     - read-write use (`publish`, `pr_comment_reply`).
   - **Bitbucket Server:** an HTTP access token, its permission level, and where to create it.
   - Derive the scopes from the endpoints the code actually calls, in a table of endpoint, purpose and scope. Mark each scope "verify at V1 acceptance (item A3)" until the record confirms it.
   - Extend the existing P2 troubleshooting and scope doc rather than duplicating it, and link both ways.
3. **LLM endpoint:**
   - any OpenAI-compatible base URL, with generic examples only;
   - choosing `context_window` and `max_output_tokens`;
   - what `diag review --dry-run` tells you.
4. **MCP client configuration (stdio):**
   - Claude Code (`claude mcp add … --env … -- npx -y @nevzatcirak/review-mcp`);
   - opencode (`opencode.json`, a `local` server with `environment`);
   - a generic JSON block.

   Where a client supports environment-variable references, show those instead of literal tokens. Verify each client's current syntax from that client's own documentation and cite the doc URL in a comment in the guide source.
5. **First run:** `server_info`, then `diag pr <url>`, then `pr_review` with `publish=false`.
6. **Troubleshooting:** every X-6 class sentence, mapped to its cause and the key to check.

### 4.2 `docs/serve.md`
- When to use serve and when to use stdio.
- The trust model:
  - headers carry identity;
  - the server keeps nothing after a request;
  - TLS is required off loopback;
  - what the access token protects.
- Configuration rows and the header contract.
- Client configuration for a remote server, with headers: Claude Code `--transport http --header …`, and an opencode `remote` server with `headers`.
- A reverse-proxy note: terminate TLS, then set `allow_insecure_http`.

### 4.3 `Dockerfile`
- Multi-stage:
  1. a `golang` builder image pinned by digest, with the same flags as the release;
  2. `gcr.io/distroless/static-debian12:nonroot`, also pinned by digest.
- `ENTRYPOINT ["/review-mcp"]`, `CMD ["serve", "--listen", "0.0.0.0:8787"]`.
- Container docs explain that this bind needs TLS files or `allow_insecure_http` behind a proxy.
- **No image publishing in v1.** A CI job builds the image only on PRs that touch the `Dockerfile`.

### 4.4 README and changelog
- **README:**
  - one-paragraph pitch;
  - a quick start: npm plus Claude Code and opencode snippets;
  - the tool table;
  - links to the setup, serve, review and ask docs;
  - the PR-Agent attribution, kept;
  - the license.
- **`CHANGELOG.md`:** Keep a Changelog format, with a `1.0.0-rc.1` section listing the v1 tool surface.

## 5. Architect review checklist
- The usual checklist.
- The credential lifetime rule (§1.1) holds by inspection.
- The middleware order matches §1.4.
- Every new error is an X-6 class with a fixed sentence.
- No new Go modules.
- Every new action is pinned by SHA.
- No install scripts in any npm package.
- `THIRD_PARTY_LICENSES` is complete, and the allowlist evidence has been reviewed.
- **Clean-room sweep:** run a script over the entire git history, every commit message and every PR and comment text. It lists every hostname, email address and URL that is not an RFC 2606 example domain, `github.com`, `npmjs.com` or a cited documentation site. The architect reviews the list before any tag is pushed.

## 6. Owner gate and release-candidate flow
1. P6 PR merged (green CI plus architect approval).
2. The clean-room sweep list is reviewed and clean.
3. **Owner decisions:**
   - repository visibility. The npm packages are public, so the binaries become public either way;
   - the npm scope or package name;
   - a granular `NPM_TOKEN` (publish-only, limited to the packages) added as a repository secret.
4. The architect pushes `v1.0.0-rc.1`. The release workflow publishes to GitHub Releases and npm under `next`.
5. The owner runs `docs/plan/v1-acceptance.md` against `rc.1`, installed through npm on a clean machine or user profile.
6. Each blocker becomes a fix PR, then `rc.N+1`. When the record shows no open blockers, the architect tags `v1.0.0`, and npm `latest` follows.

## 7. Planned commits (branch `p6-release`)
1. `refactor(config): add mode-aware validation and per-request secret overlay`
2. `feat(serve): add streamable HTTP transport with header credentials`
3. `feat(cli): add serve command`
4. `build: add third-party license bundle generator and bundle`
5. `ci: add GoReleaser release pipeline with snapshot validation`
6. `feat(npm): add launcher and platform package builder`
7. `ci: add npm packaging smoke test and publish job`
8. `docs: add setup guide and serve-mode guide`
9. `build: add Dockerfile for serve mode`
10. `docs: rewrite README and add changelog for v1.0.0-rc.1`
