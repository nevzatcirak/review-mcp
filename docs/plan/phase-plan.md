# review-mcp — Phase Plan

Status: draft v1 — 2026-10-05
Scope basis (see docs/design/v1-design-decisions.md): sealed decisions O1 (v1 = `pr_review` + `pr_ask`), O2 (WP-PR-0 first), O3 (name = `review-mcp`).

Every phase ends with a **live acceptance gate**: a real interaction against a real
Gitea and/or Bitbucket Server instance (or a real MCP client), run by the project
owner. A phase is not sealed on green tests alone.

**Amendment (2026-10-06, owner decision): consolidated V1 acceptance.** The
per-phase live gates of P2–P6 are collected into one checklist,
`docs/plan/v1-acceptance.md`, which the owner runs once against the first
release candidate (`v1.0.0-rc.1`), installed the way users install it. Until
then, a phase PR merges on green CI plus architect approval. "Merged" still
does not mean "sealed": no phase is sealed, and no stable `v1.0.0` tag exists,
before the consolidated record is posted and every blocker it finds is fixed.
Known cost: seams that only live runs reveal (auth scopes, API differences
between server versions, line anchoring, endpoint compatibility) surface late
and together. Release candidates (`rc.N`) absorb those fixes.

---

## P0 — Research: PR-Agent porting map (WP-PR-0)

**Goal:** Understand, document, and map the parts of PR-Agent we will port, before
writing any implementation code.

Work packages:
- WP-PR-0: clone PR-Agent at a pinned release tag, study the areas listed in the
  WP-PR-0 prompt, produce `docs/research/pr-agent-porting-map.md` with
  DESIGN-QUESTION markers. Includes minimal repo bootstrap (LICENSE, NOTICE,
  README stub, `.gitignore`, `docs/` layout).

**Acceptance gate:** porting-map report reviewed by the architect; all
DESIGN-QUESTIONs answered or explicitly deferred with rationale. No code beyond
bootstrap exists.

---

## P1 — Skeleton: Go module, config layer, stdio transport

**Goal:** A runnable MCP server binary that registers over stdio and exposes a
trivial diagnostic tool (e.g. `server_info`), with the config layer in place.

Work packages (indicative):
- WP-PR-1a: Go module init, directory layout, CI (GitHub Actions:
  `go test -race ./...` + lint), conventional-commit tooling.
- WP-PR-1b: config layer — everything from env/config file, **no embedded
  defaults** for URLs/endpoints/models; validation with actionable error
  messages; output-language setting (default `en`).
- WP-PR-1c: MCP stdio transport + tool registry abstraction (transport-agnostic
  tool layer, so `serve` mode can reuse it later).

**Acceptance gate:** server configured in a real MCP client (opencode or
Claude Code); client lists the tools; `server_info` returns config summary with
secrets redacted.

---

## P2 — Provider layer: Gitea + Bitbucket Server

**Goal:** Provider abstraction with two working implementations: fetch PR
metadata, changed files, diffs; post a PR-level comment.

Work packages (indicative):
- WP-PR-2a: provider interface (shaped by porting-map findings), error model,
  auth injection (per-user tokens, request-lifetime only, never logged).
- WP-PR-2b: Gitea provider.
- WP-PR-2c: Bitbucket Server (Data Center) provider — validate Server-vs-Cloud
  API differences flagged in the porting map.

**Acceptance gate (live):** against a real Gitea PR and a real Bitbucket Server
PR: metadata + diff retrieved correctly; a test comment posted and visible.

---

## P3 — Diff pipeline + token budgeting

**Goal:** Port the heart of PR-Agent: file ranking, hunk context expansion,
token-budget clipping, large-PR chunking — behind a configurable token budget.

Work packages (indicative):
- WP-PR-3a: tokenizer/budget layer (configurable ceiling; assume some
  deployments cap around ~250k context).
- WP-PR-3b: compression pipeline with fixture-based hermetic tests
  (small/large/pathological diffs) + canary tests (prove a clipping bug is
  actually caught by reverting the fix or injecting a known-bad case).

**Acceptance gate:** pipeline output inspected on real diffs pulled live from
both providers (including one deliberately oversized PR); budget respected;
degradation is explicit (noted in output), never silent.

---

## P4 — `pr_review` tool

**Goal:** First end-to-end tool: prompt assembly, LLM call (OpenAI-compatible
endpoint), structured output schema, parsing + repair path, rendering in the
configured output language.

**Acceptance gate (live):** `pr_review` run from a real MCP client against a
real Gitea PR and a real Bitbucket Server PR; output quality sanity-checked
against the actually-used model; parse-repair path demonstrated (forced
malformed response in a hermetic test, plus live observation).

---

## P5 — `pr_ask` tool

**Goal:** Free-text Q&A over the compressed PR context; reuses the P3 pipeline
and P4 LLM/parsing plumbing.

**Acceptance gate (live):** `pr_ask` answers grounded questions on a real PR on
both providers; refuses/flags when the answer is not derivable from the PR
context (honesty over fluency).

---

## P6 — `serve` mode + packaging/distribution

**Goal:** Secondary HTTP/streamable transport (identity via headers) and the
release pipeline: platform binaries on GitHub Releases, thin npm wrapper
(postinstall selects the binary), setup guide with a copy-paste token checklist
per provider (which scopes, where to create).

**Acceptance gate (live):** fresh machine: `npm install` → configure MCP client
→ `pr_review` works in stdio mode; `serve` mode validated with per-request
header identity; setup guide walked through end-to-end by the project owner.

Specification: `docs/plan/P6-spec.md`. Its live items are section A and G of
`docs/plan/v1-acceptance.md`.

---

## P7 — Conversation-aware review (added 2026-10-06, owner decision)

**Goal:** Close the gaps found in the first real session on `v1.0.0-rc.1`
(acceptance record #8, H3): anchorable findings as inline comments, one
persistent overview comment edited in place (with a new performance field),
and awareness of the existing PR discussion so that findings are not
repeated. Also carries the rc.2 hygiene items (case-colliding fixture paths,
npm trusted publishing, README examples) and the `pr_comment_create` tool.

Specification: `docs/plan/P7-spec.md`. Its live items are section I of
`docs/plan/v1-acceptance.md`. `v1.0.0` is tagged after P7 and the full
consolidated acceptance on the resulting release candidate.

---

## P8 — Slow-endpoint usability (added 2026-10-07, owner decision)

**Goal:** Make review-mcp comfortable with slow, locally hosted models:
context window read from the endpoint when not configured (90 % of the
served value), background jobs so that long `pr_review`/`pr_ask` calls never
hit an MCP client's timeout (stdio only; serve stays synchronous), a higher
LLM timeout default and an optional diff token cap.

Specification: `docs/plan/P8-spec.md`. Live items: section J of
`docs/plan/v1-acceptance.md`. `v1.0.0` follows P8 and the full acceptance.

---

## P9 — Partial-coverage honesty (added 2026-10-07, owner decision)

**Goal:** A review that could not cover every changed file says so first,
in every rendering, and never states "no issues" about unreviewed files
(X-18). Also: the npm publish job verifies every package version on the
registry. Ships as `v1.0.0-rc.4`. Specification: `docs/plan/P9-spec.md`.

---

## v1.1 — Repository context and chunked review (planned, after v1.0.0)

**Goal:** Reviews that see beyond the diff: an opt-in, stdio-only cache of
shallow PR-head clones (idle repositories deleted after 7 days, total size
capped), symbols extracted from the diff, their uses found with `git grep`,
and a budgeted "related code" prompt block. A code graph follows only if
measurement shows a clear gain.

Also: large pull requests are reviewed in several model calls ("parts")
and the results merged, so coverage no longer ends at one context window.

Binding spec: `docs/plan/v1.1-spec.md` (track A chunked review first, then
track B repository context; both PRs stay unmerged until `v1.0.0` is
tagged). Design note: `docs/design/v1.1-repo-context.md`.

---

## v2 backlog (explicitly out of v1 scope)

Design note: `docs/design/v2-design.md` (draft, 2026-10-08): provider contract suite, `pr_describe`, `pr_improve`, GitHub, review quality (2F, added 2026-10-10: CI status, project guidelines and file tree, focused reviews), GitLab.

- `pr_describe` (cheapest next tool — no line anchoring).
- `pr_improve` (code suggestions). Its anchoring builds on P7's inline anchors.
- GitHub provider.
- GitLab provider (added 2026-10-07, owner decision). Notes for its design:
  merge requests; nested group paths, so the X-2 resolver must accept a
  variable number of namespace segments; discussions API with `position`
  objects (`base_sha`/`start_sha`/`head_sha`) for inline comments; native
  quick actions (`/` commands), so the P5 slash sanitization applies to every
  published body; project and personal access tokens; self-managed instances
  with a context path.
- Anything surfaced by usage friction during v1 (friction reports become work
  items).

---

## Standing rules (apply to every phase)

- Atomic conventional commits; each commit verified green in isolation
  (detached-HEAD worktree); commit only when asked; push at package end.
- `go test -race ./...` + lint green per package; canary culture for tests.
- English-only repo content; generic examples only (`example.com`,
  `your-gitea.example`); no org-specific anything.
- Secrets never logged, never persisted, request-lifetime only; no telemetry.
- PR-Agent (MIT) attribution maintained in README + NOTICE.
