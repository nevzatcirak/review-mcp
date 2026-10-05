# review-mcp v1 — Design Decisions

| | |
|---|---|
| Status | **Approved** — 2026-10-06 |
| Input | `docs/research/pr-agent-porting-map.md` (PR-Agent v0.47.0 @ 8e5a929), DQ-1 … DQ-26 |
| Scope | review-mcp v1: `pr_review` + `pr_ask`, Gitea + Bitbucket Server (Data Center), any OpenAI-compatible endpoint, stdio-first |
| Effect | Once approved, this document is binding input for every implementation work package (P1 onward). Implementation prompts reference decisions by ID (`DQ-n`, `X-n`). |

Every question raised in the porting map is answered here or explicitly
deferred with a rationale. Deferred items are not open for v1 implementation:
code must not anticipate them beyond the seams named here.

---

## 1. Summary

| ID | Question | Decision | Status |
|---|---|---|---|
| DQ-1 | Dynamic context in v1 | Static extra-lines only; no dynamic-context key in v1 | Decided |
| DQ-2 | Language ranking without a languages API | Derive language sizes locally from the diff, for **all** providers | Decided |
| DQ-3 | Context-window source | Required config `llm.context_window`; no model registry | Decided |
| DQ-4 | Output reserve | One knob (`llm.max_output_tokens`); reserve = max(knob, 1000), soft = reserve + 500 | Decided |
| DQ-5 | Accurate token counting | Local estimates only (`o200k_base` + safety factor) | Decided |
| DQ-6 | `pr_review` output format | Markdown text content + MCP `structuredContent` with declared output schema | Decided |
| DQ-7 | YAML vs JSON | YAML with block scalars + repair chain | Decided |
| DQ-8 | Repair-chain scope | Reduced, table-driven chain (10 tactics); each tactic canary-proven | Decided |
| DQ-9 | Parse-failure retry / fallback models | Single model in v1; one same-model re-ask on parse failure; fallback chain deferred | Decided |
| DQ-10 | Diff representation | One parsed hunk model, multiple renderers (decided now); `/improve` view choice deferred | Partly decided |
| DQ-11 | Anchor acquisition for `/improve` | — | Deferred to v2 |
| DQ-12 | Verification source of truth | Hybrid (head file if complete, else patch walk) — applies to v1 finding snippets | Decided |
| DQ-13 | Gitea inline granularity (`/improve`) | — | Deferred to v2 |
| DQ-14 | Bitbucket Server `lineType` | — | Deferred to v2 |
| DQ-15 | Cross-run inline dedup | — | Deferred to v2 |
| DQ-16 | Capability surface | Typed `Capabilities` struct; renderer takes an output *target profile* | Decided |
| DQ-17 | Gitea per-file patch source | Whole-PR `.diff` + real unified-diff parser + response-size cap | Decided |
| DQ-18 | Gitea inline publish granularity | — | Deferred to v2 |
| DQ-19 | Bitbucket Server diff acquisition | Full base/head files + local unified diff (upstream approach) | Decided |
| DQ-20 | Minimum Bitbucket DC version | ≥ 7.0; feature-detect merge-base, fall back to ancestor walk; honest error below 7.0 | Decided |
| DQ-21 | Bitbucket Basic auth | Bearer (HTTP access token) only in v1 | Decided |
| DQ-22 | Repo-local config file | None in v1 | Decided |
| DQ-23 | Config file | Env + optional TOML file referenced by `REVIEW_MCP_CONFIG` | Decided |
| DQ-24 | Env naming | Fixed table, `REVIEW_MCP_` prefix, unknown-variable warnings | Decided |
| DQ-25 | Per-call overrides | Curated tool arguments only | Decided |
| DQ-26 | Sampling-parameter defaults | Send only explicitly configured sampling parameters | Decided |
| X-1 | Publishing to the PR in v1 | Opt-in `publish` argument, PR-level comment only | Decided |
| X-2 | Provider selection | By PR-URL match against configured base URLs (no global `provider.kind`) | Decided |
| X-3 | Large-PR handling | Single call + mandatory coverage footer; review chunking deferred | Decided |
| X-4 | v1 review schema | Key issues (always) + tests / security / effort toggles | Decided |
| X-5 | `pr_ask` statefulness | Stateless; no conversation-history fetch | Decided |
| X-6 | Error reporting | Allowlist-classified, sanitized messages always; no verbosity knob | Decided |
| X-7 | License compliance for ported text/data | Full PR-Agent MIT notice in NOTICE before first ported text/data lands | Decided |
| X-8 | Logging | `log/slog` to stderr only; never prompt/response bodies, never secrets | Decided |

---

## 2. Cross-section conflict resolutions

The porting map flagged four places where independently studied sections
disagreed. Resolved as follows:

| Conflict | Resolution | Governing decision |
|---|---|---|
| Fallback models: B/D assume a fallback chain, G drops it | **Dropped for v1.** Each fallback model would need its own context window, multiplying required config. The `FallbackEligible` sentinel error is still implemented so the chain is a mechanical v1.x addition. | DQ-9 |
| Dynamic context: A says static-only, G keeps the key | **Static-only, no key.** Unimplemented keys are not reserved; the upstream name returns when the feature does. | DQ-1 |
| Temperature default: C/D keep 0.2, G sends only explicit values | **Explicit-only.** The setup guide's example config sets `temperature = 0.2` as a recommendation, so upstream-tuned behavior is one documented line away. | DQ-26 |
| Error detail: D wants detailed errors on, G drops the key | **No knob.** Sanitized, allowlist-classified messages are always returned; raw provider text never is. | X-6 |

---

## 3. Decisions in detail

Format: **Decision** (normative), **Rationale**, **Consequences** (what
implementation must do or must not do).

### Area A — Diff compression pipeline

#### DQ-1 — Dynamic context in v1
- **Decision:** v1 extends hunks with static context only (`diff.extra_lines_before` = 5, `diff.extra_lines_after` = 1, each capped at 10). Dynamic context (hoisting to the enclosing function/class header) is not implemented and has no config key in v1.
- **Rationale:** Static extension carries most of the value; dynamic context is the most intricate code in `extend_patch`, with several bail-out branches, and is not on v1's critical path.
- **Consequences:** The `extend` function's signature must not preclude a later dynamic variant (it already needs head-file content for the static safety checks). The rendered `@@ … @@ <section header>` text is preserved as-is.

#### DQ-2 — Language ranking without a languages API
- **Decision:** Language ordering is derived locally for every provider: classify each changed file by extension via the embedded extension map, sum patch bytes per language, sort descending; unmatched files go to the final "Other" group. The Gitea languages endpoint is **not** used in v1.
- **Rationale:** One provider-agnostic, deterministic, hermetically testable code path; one fewer API call; ranking by the PR's own languages is at least as relevant as repo-wide byte counts.
- **Consequences:** Documented deviation from upstream (which ranks by repository language sizes). The admission order *within* a language group (token count descending) stays upstream-exact.

### Area B — Token budgeting

#### DQ-3 — Context-window source
- **Decision:** `llm.context_window` (integer tokens) is **required**; startup validation rejects a missing value or one below 4096. No model registry is shipped; no LiteLLM-style metadata lookup exists.
- **Rationale:** Context windows are model- and deployment-specific (some deployments cap around ~250k); a required value is honest, generic, and immune to registry staleness. Avoids upstream's silent 32000-token clamp surprise.
- **Consequences:** The setup guide explains how to choose the value. This single value replaces upstream's `max_model_tokens` + `custom_model_max_tokens` + registry.

#### DQ-4 — Output reserve
- **Decision:** One optional knob, `llm.max_output_tokens`. Hard output reserve = `max(llm.max_output_tokens or 0, 1000)`; soft reserve = hard + 500. When the knob is set it is also sent to the endpoint as the completion-length limit; when unset, nothing is sent.
- **Rationale:** Keeps upstream's soft/hard invariant (1500/1000 by default) while giving users one visible lever for verbose models.
- **Consequences:** Soft/hard reserves are internal derived values, not config keys.

#### DQ-5 — Accurate counting
- **Decision:** All counting is local: tiktoken `o200k_base` estimate, inflated by `llm.token_estimate_factor` (default 0.3) for budget decisions. No network token-count calls.
- **Rationale:** Deterministic, offline, testable; OpenAI-compatible endpoints generally expose no count API.
- **Consequences:** The "heuristic shrink → exact recount" discipline and the verified-prefix binary search are ported as-is; counts are never assumed additive. Message framing allowances (16 per message + 16 per reply) are ported as constants; the image allowance is dropped.

### Area C — Prompts and output schemas

#### DQ-6 — `pr_review` output format
- **Decision:** The tool result carries (1) rendered markdown as the primary text content and (2) the validated review object as MCP `structuredContent`, with the tool declaring a matching output schema.
- **Rationale:** Chat-style clients show the markdown; programmatic clients consume the structure; the cost is near zero because the struct exists anyway.
- **Consequences:** The output schema is generated from the same field-descriptor table that renders the prompt schema and drives validation (one source of truth — prompt, validator, and MCP schema cannot drift). Exact SDK mechanics are settled in P1/P4.

#### DQ-7 — YAML vs JSON
- **Decision:** YAML with block scalars (`|`), the open ```` ```yaml ```` fence at the end of the user prompt, and the ported repair chain. No JSON mode.
- **Rationale:** Battle-tested across many models; JSON mode is optional and frequently absent on OpenAI-compatible servers; multi-line code in JSON strings is an escaping hazard.
- **Consequences:** The prompt tail and `drop_sign_off_after_wrapper_fence` logic are coupled — changing one requires re-proving the other.

### Area D — Parsing and repair

#### DQ-8 — Repair-chain scope
- **Decision:** A table-driven chain (ordered slice of transforms with one shared transform → unmarshal → return driver) containing upstream tactics **1, 2, 4, 5, 6, 7, 8, 9, 11, 12** in upstream order. Tactics 3 (brace re-indent) and 10 (code-section re-indent for other tools) are omitted. First successful parse wins (upstream-faithful).
- **Rationale:** The omitted tactics target `/improve`/`/describe` output shapes v1 never emits; the table makes re-adding any tactic a small change.
- **Consequences:** **Every kept tactic needs a canary fixture**: a real malformed sample that this tactic alone repairs, proven by disabling the tactic and watching the test fail. `keys_fix_yaml` is generated from the v1 field-descriptor table, not copied. The winning tactic's *name* is logged at debug level (never the content). PyYAML↔yaml.v3 drift is covered by replaying upstream's unit-test fixtures as data.

#### DQ-9 — Parse-failure retry and fallback models
- **Decision:** v1 calls one configured model. Retry policy, in order:
  1. Transport-level failure (HTTP 429, 5xx, timeout, connection error): retry the same request up to `llm.max_retries` times (default 1), honoring `Retry-After` up to a bounded cap.
  2. Parse failure after the full repair chain (no non-empty `review` mapping): **one** same-model re-ask with the same prompt plus a terse note that the previous output was not valid YAML; temperature unchanged.
  3. Anything else, or a second parse failure: terminal, reported per X-6.
  Fallback models (`llm.fallback_models`) are deferred to v1.x.
- **Rationale:** Most v1 users have one endpoint and one model; upstream's design would turn a single parse failure into a hard failure for them. A fallback chain needs per-model context windows, which v1's config deliberately avoids.
- **Consequences:** Documented deviation from upstream (the re-ask). The `FallbackEligible` sentinel (`errors.Is`) exists from day one for "diff does not fit / empty after budgeting / unparseable", so adding the chain later is mechanical. Request preparation is per-attempt and fully request-scoped (no shared mutable state — required anyway for per-user tokens).

### Area E — Line anchoring (`/improve`, v2)

#### DQ-10 — Diff representation
- **Decision (now):** The diff layer parses each patch into a structured hunk model once and produces every view by rendering: plain unified (used by `pr_ask`), decoupled with line numbers (`__new hunk__` numbered / `__old hunk__` unnumbered — used by `pr_review`), and decoupled without numbers (reserved for v2). No view is produced by string-stripping another view.
- **Deferred:** Which view `/improve` generation uses (upstream's DQ options b vs c) — decided at v2 design.
- **Rationale:** Eliminates the pair-drift failure class and upstream's `remove_line_numbers` digit-stripping heuristic; costs nothing extra in v1.
- **Consequences:** Rendered bytes must match upstream's formats exactly (golden tests: ordinary hunk, hunk without deletions, multi-hunk file, deleted file, `\ No newline at end of file`, malformed `@@` header).

#### DQ-11 — Anchor acquisition (second pass vs direct)
- **Deferred to v2.** Recorded leaning: two-pass with stable per-suggestion IDs, with a mechanical `existing_code` search prototyped behind a flag. Rationale for deferral: `/improve` is out of v1 scope; no v1 code depends on it.

#### DQ-12 — Verification source of truth
- **Decision:** Hybrid, effective in v1: `pr_review` findings carry `start_line`/`end_line`; when rendering the code snippet under a finding, lines come from the head file when its content is complete, otherwise from a patch walk that must resolve every line of the range. An unresolvable range keeps the finding but omits the snippet, with an explicit note ("lines could not be verified against the diff").
- **Rationale:** Patch-only forbids snippets in legitimately visible extended context; head-file-only turns fetch hiccups into lost information. Honest degradation over silent drop.
- **Consequences:** The same resolver becomes v2's anchor validator.

#### DQ-13, DQ-14, DQ-15 — Gitea inline granularity, Bitbucket Server `lineType`, cross-run dedup
- **Deferred to v2** (all concern inline publication, which v1 does not do — see X-1). Recorded leanings: one Gitea review per run with multi-line suggestions demoted to plain comments; compute Bitbucket `lineType` (ADDED vs CONTEXT) from the hunk model instead of hardcoding; port fingerprint markers and wire them into both providers if committable inline mode ships.

### Area F — Providers

#### DQ-16 — Capability surface
- **Decision:** Each provider returns a typed `Capabilities` struct (v1 fields: `GFM` — HTML `<details>`/`<table>` rendering; `MarkdownTables`; `Labels`; `InlineComments`). The renderer takes an explicit **target profile**:
  - `client` — the MCP tool result: portable markdown only (headings, lists, fenced code, links); **no raw HTML**, because terminal MCP clients do not render it.
  - `provider` — a published PR comment (X-1): derived from the provider's `Capabilities` (Gitea: GFM; Bitbucket Server: no GFM, pipe tables allowed).
- **Rationale:** Rendering for the MCP client is not a provider property — it is the dominant v1 path. Keeps render code provider-agnostic and ready for GitHub.
- **Consequences:** Golden tests per profile. Upstream's emoji/`<details>` presentation applies to the `provider` profile only.

#### DQ-17 — Gitea per-file patch source
- **Decision:** Fetch the whole-PR `.diff` once and parse it with a real unified-diff parser (handles `a/`/`b/` prefixes, `rename from/to`, `/dev/null` sides, quoted/escaped paths, binary markers). Enforce a response-size cap (`diff.max_diff_bytes`); exceeding it is an honest error naming the cap.
- **Rationale:** One request, version-independent, deterministic; fixes upstream's rename/space-in-path bugs instead of inheriting them.
- **Consequences:** `old_filename` is populated for renames. The `/files.patch` field is evaluated only as a later optimization, after live acceptance.

#### DQ-18 — Gitea inline publish granularity
- **Deferred to v2** (no inline publication in v1). Recorded leaning: single review with every anchor pre-validated; unanchorable findings appended to the PR-level comment.

#### DQ-19 — Bitbucket Server diff acquisition
- **Decision:** Port upstream's approach: list changes, download base and head contents via the raw endpoint, generate a hunk-only unified diff locally with 3 context lines (difflib-compatible output).
- **Rationale:** It is what upstream's downstream logic is tuned against; correctness over bandwidth for v1.
- **Consequences:** `diff.max_files_full_content` (default 50) and `diff.max_file_bytes` apply. On Bitbucket Server, files beyond either cap **cannot** be diffed at all; they are listed in the coverage footer as not processed (never silently dropped). The native diff endpoint is a post-acceptance optimization candidate.

#### DQ-20 — Minimum Bitbucket DC version
- **Decision:** Supported: Data Center ≥ 7.0. Base-SHA strategy by feature detection: call the merge-base endpoint; if unavailable (404), use the ancestor walk (first source-commit parent present in destination history). The naive first-parent strategy is not ported. A version probe runs only to return an honest "unsupported server version (< 7.0)" error.
- **Rationale:** Two tested paths cover realistic fleets; feature detection is more robust than version-string parsing; a wrong base silently reviews the wrong delta, which is worse than failing.
- **Consequences:** The strategy used is reported in debug logs and in `server_info`-style diagnostics. Live acceptance must exercise both paths if an older instance is available; otherwise the ancestor walk is marked "hermetically tested only" in the acceptance record.

#### DQ-21 — Bitbucket Basic auth
- **Decision:** Bearer token (HTTP access token) only in v1. The config shape leaves room to add a Basic-auth pair later without breaking changes.
- **Rationale:** Minimal secret surface; HTTP access tokens cover supported DC versions.

### Area G — Config surface

#### DQ-22 — Repo-local config file
- **Decision:** No configuration is read from the reviewed repository in v1.
- **Rationale:** Upstream's largest security surface (an entire allowlist module exists to contain it); low value for a per-user stdio tool.
- **Consequences:** The loader is layered so a repo layer can be inserted later — but only together with a host-only key filter.

#### DQ-23 — Config file
- **Decision:** Environment variables plus an **optional** TOML file whose path is given by `REVIEW_MCP_CONFIG`. No implicit search paths. Precedence (low → high): compiled defaults → file → environment → per-call tool arguments.
- **Rationale:** Keeps MCP client configs short (a path plus secrets) while lists and toggles live in a commented file.
- **Consequences:** Secrets are env-only: the file loader rejects any secret key with a hard error naming the key (never its value). Unknown file keys are errors, not warnings.

#### DQ-24 — Env naming
- **Decision:** A fixed, hand-written table of variables, `REVIEW_MCP_<SECTION>_<KEY>` (e.g. `REVIEW_MCP_LLM_BASE_URL`, `REVIEW_MCP_DIFF_EXTRA_LINES_BEFORE`); list values comma-separated. Unknown `REVIEW_MCP_*` variables produce a startup warning. Values are parsed strictly; a malformed value is a startup error.
- **Rationale:** Exact docs, typo detection, no reflection magic (the cause of upstream's env-replay workaround).

#### DQ-25 — Per-call overrides
- **Decision:** Only these tool arguments override static config: `extra_instructions`, `output_language` (both tools), `max_findings` (`pr_review`), plus `publish` (X-1). No generic key passthrough.
- **Rationale:** These vary per task by nature; generic passthrough reimports upstream's host-only-key problem and bloats the tool schema.

#### DQ-26 — Sampling-parameter defaults
- **Decision:** `temperature`, `seed`, `reasoning_effort` and the completion-length limit are sent **only when explicitly configured**; there are no compiled defaults for them. Endpoint errors caused by them are relayed (sanitized per X-6) with a hint naming the config key.
- **Rationale:** Minimal requests are maximally compatible with the long tail of OpenAI-compatible servers (some models reject `temperature`); consistent with the no-embedded-model-assumptions rule.
- **Consequences:** Documented behavior change versus upstream (which defaults to 0.2). The setup guide's example config sets `temperature = 0.2` as the recommended starting point. If `seed` is set, temperature is not silently forced (unlike upstream); the guide documents the pairing.

---

## 4. Additional decisions (surfaced while resolving the DQs)

#### X-1 — Publishing to the PR in v1
- **Decision:** Both tools always return their result to the MCP client. An optional `publish` argument (default `false`) additionally posts the result as a **new PR-level comment** (rendered with the `provider` profile). No inline comments, no persistent-comment editing, no labels in v1.
- **Rationale:** Returning text is the MCP-native path; opt-in publishing satisfies the P2 acceptance gate (comment posted and visible) without importing upstream's comment-lifecycle machinery.
- **Consequences:** `pr_ask` publishing applies the leading-`/` sanitization. Persistent-comment update is a v1.x candidate.

#### X-2 — Provider selection and base-URL pinning
- **Decision:** There is no global `provider.kind`. A provider is enabled when its `base_url` is configured (its token then becomes required). The PR URL passed to a tool must match an enabled provider's `base_url` (or Gitea `web_url`) by scheme, host, port and path prefix; that provider handles it. A PR URL matching no configured provider is rejected before any network call.
- **Rationale:** Implements sealed decision 13 (no token is ever sent to a host derived from user input) and lets one installation serve both providers.
- **Consequences:** The Bitbucket Server context path is taken from config, never inferred from the PR URL. Canary test: a PR URL on a foreign host must fail without any outbound request.

#### X-3 — Large-PR handling
- **Decision:** v1 `pr_review` makes a single LLM call over the budget-fitted diff. Files omitted by budgeting are always listed in a coverage section (added / modified / deleted / not processed), in both render profiles. Review chunking (`enable_large_pr_chunking`) is deferred.
- **Rationale:** Honest degradation over silent truncation; chunking (planning, merge rules, resize-on-fallback) is substantial machinery.
- **Consequences:** The coverage section is always on — no config key.

#### X-4 — v1 review schema
- **Decision:** Fields: `key_issues_to_review` (always; elements `relevant_file`, `issue_header`, `issue_content`, `start_line`, `end_line`), `security_concerns` (`review.require_security`, default true), `relevant_tests` (`review.require_tests`, default true), `estimated_effort_to_review` (`review.require_effort_estimate`, default true; renamed from upstream's bracketed key, range 1–5 in its description). `review.max_findings` default 3.
- **Rationale:** The core value with the smallest prompt/validation surface; the descriptor table makes later fields (score, risk, merge recommendation, can-be-split, todo scan) cheap additions.
- **Consequences:** Ticket compliance is out (needs a tracker integration). The `security_concerns` "literal English `No`" instruction is kept so No-detection works under any output language; the No-detector accepts `false`, `no`, `none` (case-insensitive).

#### X-5 — `pr_ask` statefulness
- **Decision:** `pr_ask` is stateless: inputs are the PR and the question; no comment-thread history is fetched. It uses the plain unified-diff view and free-text output (no schema, no repair chain).
- **Rationale:** MCP clients carry their own conversation; fetching history adds provider surface and prompt-injection exposure for no v1 benefit.

#### X-6 — Error reporting
- **Decision:** Errors returned to the MCP client are produced by an allowlist classifier (auth failure, rate limit, timeout, context too long, connection error, model output unparseable, unsupported server version, URL not matching a configured provider, …) mapping to fixed English sentences, plus the config key to check where relevant. Raw provider/endpoint response text is never returned. Unknown errors get a generic message. There is no verbosity knob.
- **Rationale:** The classifier is the token-leak firewall; MCP has a native error channel, so the upstream publish/propagate switches are moot.

#### X-7 — License compliance for ported text and data
- **Decision:** Before the first commit that includes text or data adapted from PR-Agent (prompt templates, the bad-extension list, generated-code globs, the language-extension map, marker strings), `NOTICE` is extended with PR-Agent's full MIT notice, reproducing its copyright line verbatim (`Copyright (c) 2026 The PR Agent`) and permission text, and listing which repository files contain adapted material.
- **Rationale:** MIT requires the copyright and permission notice in copies of substantial portions; prose attribution alone is not sufficient once text/data is ported.
- **Consequences:** Ported data files carry a header comment pointing to `NOTICE`. This is an acceptance item for P3 and P4.

#### X-8 — Logging
- **Decision:** `log/slog` to **stderr only** (stdout is the MCP stdio channel), level from `log.level` (default `info`). Never logged: prompt or response bodies, diff content, secrets, `Authorization` headers. A `redact` helper (port of upstream's credential-redaction regexes) wraps every URL/header that reaches a log line.
- **Consequences:** Canary test: a token placed in env must not appear in captured stderr across a full (mocked) tool run, including error paths.

---

## 5. Resulting v1 configuration surface

Secrets are environment-only. All other keys may come from the TOML file or the environment (DQ-23/24). "—" means no default (required or optional-unset).

| Key (TOML) | Env | Default | Notes |
|---|---|---|---|
| — | `REVIEW_MCP_CONFIG` | — | Optional path to TOML file |
| — | `REVIEW_MCP_LLM_API_KEY` | — | **Secret**, required |
| — | `REVIEW_MCP_GITEA_TOKEN` | — | **Secret**, required iff Gitea enabled |
| — | `REVIEW_MCP_BITBUCKET_SERVER_TOKEN` | — | **Secret**, required iff Bitbucket Server enabled |
| `llm.base_url` | `REVIEW_MCP_LLM_BASE_URL` | — | Required |
| `llm.model` | `REVIEW_MCP_LLM_MODEL` | — | Required |
| `llm.context_window` | `REVIEW_MCP_LLM_CONTEXT_WINDOW` | — | Required, ≥ 4096 (DQ-3) |
| `llm.max_output_tokens` | `REVIEW_MCP_LLM_MAX_OUTPUT_TOKENS` | — | Optional (DQ-4) |
| `llm.temperature` | `REVIEW_MCP_LLM_TEMPERATURE` | — | Optional, sent only if set (DQ-26) |
| `llm.seed` | `REVIEW_MCP_LLM_SEED` | — | Optional |
| `llm.reasoning_effort` | `REVIEW_MCP_LLM_REASONING_EFFORT` | — | Optional pass-through |
| `llm.timeout_seconds` | `REVIEW_MCP_LLM_TIMEOUT_SECONDS` | 120 | |
| `llm.max_retries` | `REVIEW_MCP_LLM_MAX_RETRIES` | 1 | Transport retries (DQ-9) |
| `llm.token_estimate_factor` | `REVIEW_MCP_LLM_TOKEN_ESTIMATE_FACTOR` | 0.3 | DQ-5 |
| `gitea.base_url` | `REVIEW_MCP_GITEA_BASE_URL` | — | Enables Gitea (X-2) |
| `gitea.web_url` | `REVIEW_MCP_GITEA_WEB_URL` | — | Optional split web URL |
| `gitea.ca_cert` | `REVIEW_MCP_GITEA_CA_CERT` | — | Optional CA bundle path |
| `gitea.insecure_skip_verify` | `REVIEW_MCP_GITEA_INSECURE_SKIP_VERIFY` | false | Startup warning when true |
| `bitbucket_server.base_url` | `REVIEW_MCP_BITBUCKET_SERVER_BASE_URL` | — | Enables Bitbucket Server, incl. context path (X-2) |
| `bitbucket_server.ca_cert` | `REVIEW_MCP_BITBUCKET_SERVER_CA_CERT` | — | |
| `bitbucket_server.insecure_skip_verify` | `REVIEW_MCP_BITBUCKET_SERVER_INSECURE_SKIP_VERIFY` | false | Startup warning when true |
| `output.language` | `REVIEW_MCP_OUTPUT_LANGUAGE` | `en-US` | Locale code |
| `diff.extra_lines_before` | `REVIEW_MCP_DIFF_EXTRA_LINES_BEFORE` | 5 | Capped at 10 |
| `diff.extra_lines_after` | `REVIEW_MCP_DIFF_EXTRA_LINES_AFTER` | 1 | Capped at 10 |
| `diff.skip_extend_extensions` | `REVIEW_MCP_DIFF_SKIP_EXTEND_EXTENSIONS` | `.md,.txt` | |
| `diff.large_patch_policy` | `REVIEW_MCP_DIFF_LARGE_PATCH_POLICY` | `clip` | `clip` \| `skip` |
| `diff.max_description_tokens` | `REVIEW_MCP_DIFF_MAX_DESCRIPTION_TOKENS` | 500 | |
| `diff.max_commits_tokens` | `REVIEW_MCP_DIFF_MAX_COMMITS_TOKENS` | 500 | |
| `diff.max_files_full_content` | `REVIEW_MCP_DIFF_MAX_FILES_FULL_CONTENT` | 50 | Both providers (DQ-19) |
| `diff.max_file_bytes` | `REVIEW_MCP_DIFF_MAX_FILE_BYTES` | 1048576 | Proposed; tune at live acceptance |
| `diff.max_diff_bytes` | `REVIEW_MCP_DIFF_MAX_DIFF_BYTES` | 20971520 | Proposed; tune at live acceptance (DQ-17) |
| `diff.ignore_generated_frameworks` | `REVIEW_MCP_DIFF_IGNORE_GENERATED_FRAMEWORKS` | (empty) | Names from embedded generated-code table |
| `ignore.glob` | `REVIEW_MCP_IGNORE_GLOB` | `vendor/**` | doublestar semantics |
| `ignore.regex` | `REVIEW_MCP_IGNORE_REGEX` | (empty) | RE2 dialect; compile errors fail startup |
| `review.max_findings` | `REVIEW_MCP_REVIEW_MAX_FINDINGS` | 3 | Per-call override (DQ-25) |
| `review.require_tests` | `REVIEW_MCP_REVIEW_REQUIRE_TESTS` | true | X-4 |
| `review.require_security` | `REVIEW_MCP_REVIEW_REQUIRE_SECURITY` | true | X-4 |
| `review.require_effort_estimate` | `REVIEW_MCP_REVIEW_REQUIRE_EFFORT_ESTIMATE` | true | X-4 |
| `review.extra_instructions` | `REVIEW_MCP_REVIEW_EXTRA_INSTRUCTIONS` | (empty) | Per-call override |
| `ask.extra_instructions` | `REVIEW_MCP_ASK_EXTRA_INSTRUCTIONS` | (empty) | Per-call override |
| `log.level` | `REVIEW_MCP_LOG_LEVEL` | `info` | stderr only (X-8) |

Validation rules: at least one provider enabled; every enabled provider has its
token; URLs parse as `http(s)`; numeric ranges sane; enum values exact. All
violations are reported together in one token-free startup error.

## 6. v1 tool surface

| Tool | Arguments | Result |
|---|---|---|
| `server_info` (P1 diagnostic) | — | Version, enabled providers, effective non-secret config (secrets shown as set/unset only) |
| `pr_review` | `pr_url` (required), `extra_instructions`, `output_language`, `max_findings`, `publish` | Markdown (`client` profile) + `structuredContent` (DQ-6) |
| `pr_ask` | `pr_url` (required), `question` (required), `extra_instructions`, `output_language`, `publish` | Markdown answer |

## 7. Items deferred beyond v1 (with seams)

| Item | Seam that must exist in v1 |
|---|---|
| Fallback model chain | `FallbackEligible` sentinel; per-attempt request preparation |
| Dynamic context | `extend` has head-file content available |
| Review chunking | Pipeline already produces `remaining_files` |
| Repo-local config | Layered loader with explicit layer list |
| Basic auth (Bitbucket Server) | Auth as a provider-level strategy, not a hardcoded header |
| Inline comments, persistent comments, labels | Typed `Capabilities` struct; DQ-12 resolver |
| `/improve` (DQ-11, 13, 14, 15, 18) | Hunk model with multiple renderers (DQ-10) |
| Additional review fields | Field-descriptor table (X-4, DQ-6) |
