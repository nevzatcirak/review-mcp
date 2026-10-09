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
| DQ-3 | Context-window source | Optional config `llm.context_window`, resolved from the endpoint when unset (amended by X-15); no model registry | Decided (amended) |
| DQ-4 | Output reserve | One knob (`llm.max_output_tokens`); reserve = max(knob, 1000), soft = reserve + 500 | Decided |
| DQ-5 | Accurate token counting | Local estimates only (`o200k_base` + safety factor) | Decided |
| DQ-6 | `pr_review` output format | Markdown text content + MCP `structuredContent` with declared output schema | Decided |
| DQ-7 | YAML vs JSON | YAML with block scalars + repair chain | Decided |
| DQ-8 | Repair-chain scope | Reduced, table-driven chain (10 tactics); each tactic canary-proven | Decided |
| DQ-9 | Parse-failure retry / fallback models | Single model in v1; one same-model re-ask on parse failure; fallback chain deferred | Decided |
| DQ-10 | Diff representation | One parsed hunk model, multiple renderers (decided now); `/improve` view choice deferred | Partly decided |
| DQ-11 | Anchor acquisition for `/improve` | — | Deferred to v2 |
| DQ-12 | Verification source of truth | Hybrid (head file if complete, else patch walk) — applies to v1 finding snippets | Decided |
| DQ-13 | Gitea inline granularity | One `COMMENT` review per run, pinned to the head commit (decided in P7, X-11) | Decided (P7) |
| DQ-14 | Bitbucket Server `lineType` | Computed from the hunk model (`ADDED` or `CONTEXT`), never hardcoded (decided in P7, X-11) | Decided (P7) |
| DQ-15 | Cross-run inline dedup | Fingerprint marker on each inline comment; a finding already on the PR from the same identity is skipped (decided in P7, X-13) | Decided (P7) |
| DQ-16 | Capability surface | Typed `Capabilities` struct; renderer takes an output *target profile* | Decided |
| DQ-17 | Gitea per-file patch source | Whole-PR `.diff` + real unified-diff parser + response-size cap | Decided |
| DQ-18 | Gitea inline publish granularity | One review per run; an unanchorable finding is listed in the overview only (decided in P7, X-11) | Decided (P7) |
| DQ-19 | Bitbucket Server diff acquisition | Full base/head files + local unified diff (upstream approach) | Decided |
| DQ-20 | Minimum Bitbucket DC version | ≥ 7.0; feature-detect merge-base, fall back to ancestor walk; honest error below 7.0 | Decided |
| DQ-21 | Bitbucket Basic auth | Bearer (HTTP access token) only in v1 | Decided |
| DQ-22 | Repo-local config file | None in v1 | Decided |
| DQ-23 | Config file | Env + optional TOML file referenced by `REVIEW_MCP_CONFIG` | Decided |
| DQ-24 | Env naming | Fixed table, `REVIEW_MCP_` prefix, unknown-variable warnings | Decided |
| DQ-25 | Per-call overrides | Curated tool arguments only | Decided |
| DQ-26 | Sampling-parameter defaults | Send only explicitly configured sampling parameters | Decided |
| X-1 | Publishing to the PR in v1 | Opt-in `publish` argument; PR-level comment only in rc.1, amended by X-11 and X-12 | Decided (amended) |
| X-2 | Provider selection | By PR-URL match against configured base URLs (no global `provider.kind`) | Decided |
| X-3 | Large-PR handling | Single call + mandatory coverage footer; review chunking deferred | Decided |
| X-4 | v1 review schema | Key issues (always) + tests / security / effort toggles | Decided |
| X-5 | `pr_ask` statefulness | Stateless; no conversation-history fetch | Decided |
| X-6 | Error reporting | Allowlist-classified, sanitized messages always; no verbosity knob | Decided |
| X-7 | License compliance for ported text/data | Full PR-Agent MIT notice in NOTICE before first ported text/data lands | Decided |
| X-8 | Logging | `log/slog` to stderr only; never prompt/response bodies, never secrets | Decided |
| X-9 | PR conversation tools (added 2026-10-06) | `pr_comments` (read threads) + `pr_comment_reply` (reply in thread, honest fallback) in v1 | Decided |
| X-10 | `serve` identity (added 2026-10-06) | Credentials per HTTP request in headers; provider tokens in the server env are refused in serve mode; stateless transport | Decided |
| X-11 | Inline findings (P7, amends X-1) | `publish=true` posts an overview and each anchorable finding as an inline comment; Gitea one `COMMENT` review per run, Bitbucket one comment per finding with a computed `lineType`; single-line anchors | Decided |
| X-12 | Persistent overview (P7, amends X-1, extends X-4) | One overview per PR per token user, found by marker and author, edited in place; new `performance_concerns` field | Decided |
| X-13 | Discussion awareness (P7) | The PR's threads go into the prompt as untrusted, budgeted data; inline findings carry a fingerprint and are not posted twice | Decided |
| X-14 | `pr_comment_create` (P7, extends X-9) | Sixth tool: a new PR-level or inline comment; an unanchorable line is refused, never downgraded | Decided |
| X-15 | Context window from the endpoint (P8, amends DQ-3) | `llm.context_window` is optional; unset, the endpoint's model list is asked once per process and 90 % of the reported window is used; training-size fields are never used | Decided |
| X-16 | Background jobs for long calls (P8) | stdio only: `pr_review` and `pr_ask` wait at most `wait_seconds`, then return a `job_id`; the new tool `job_result` collects the result; serve mode stays synchronous | Decided |
| X-17 | Latency controls (P8) | `llm.timeout_seconds` default 300; optional `diff.max_tokens` caps the diff budget | Decided |
| X-18 | Partial-coverage honesty (P9, extends X-3) | A result is partial when a reviewable file was omitted, clipped, skipped for size or unreadable; it leads with a fixed banner in every rendering, carries counts in `coverage`, and every "no concerns" statement is scoped to the reviewed files | Decided |
| X-19 | Chunked review (v1.1, RC-11 to RC-14) | When the prepared diff does not hold every reviewable file, `pr_review` reviews the rest in further model calls ("parts"), up to `review.max_chunks` (default 8), sequentially, and merges the answers; `review.max_chunks = 1` is the v1.0 behaviour | Decided |
| X-20 | Deletions listed by name are not budget losses (v1.1, settles 9a DESIGN-QUESTION 1) | A deleted file whose patch is dropped by design and whose name is in the prompt's deleted-files list counts as reviewed (`coverage.deleted_listed`); only deletions cut by the budget are not reviewed | Decided |
| X-21 | `pr_ask` does not chunk (v1.1, RC-15) | `pr_ask` keeps one call; the files a question names are admitted first; the X-18 banner stays | Decided |
| X-22 | Repository context (v1.1, RC-1 to RC-10) | Opt-in and stdio only: the PR head is fetched into a credential-safe, self-pruning cache, the symbols the diff changes are searched with `git grep` on that commit, and the best uses go into a budgeted prompt block; every failure is a note and `coverage.repo_context`, never a failed review | Decided |
| X-23 | `pr_info`: target branch, reviewers and approvals (v1.1, Track C) | A read-only tool, no LLM call: target branch, human reviewers with states, approval counts, required approvals and merge status where the provider exposes them; review-mcp's own marked activity is reported separately and never counts as a review | Decided |
| X-24 | Provider contract suite (v2, Y-1) | `internal/provider/contract` holds the behaviour every provider must show, run by each provider's own tests against a fake server through a small `Fixture`; the suite never branches on the provider kind, so GitHub and GitLab add only a fixture | Decided |
| X-25 | Nested namespaces and capabilities (v2, Y-2, Y-3) | `PRRef.Namespace` may hold several segments, escaped per segment; behaviour follows `Capabilities` (`SuggestionBlocks`, `QuickActions`, `InlineThreadResolution`, `GeneralThreadResolution`, `DescriptionEdit`), never the provider name; the slash sanitisation of published bodies is provider-neutral and applies when `QuickActions` is set | Decided |
| X-26 | `pr_describe` (v2, Y-4 to Y-7) | A tool that returns a title, change types, a summary and a files walkthrough; large pull requests are described in parts plus one diff-free summary call; a file the model did not describe is listed, never padded; `publish` writes a comment edited in place or a marked region of the description that never changes the author's text | Decided |
| X-27 | `pr_improve` (v2, Y-8 to Y-11) | A tool that returns code suggestions, each scored 0 to 10 by a second self-review call (below `improve.min_score` dropped and counted, a failed or silent self-review leaves a suggestion unscored, never dropped); the parts are ranked together and capped; every suggestion is verified against the head file (or, without it, through the patch) before it may be anchored, and an unverified one is marked and never posted inline; `publish` writes an overview edited in place and an inline comment per verified suggestion on new-side lines of one hunk, keyed by file and code, not by the summary | Decided |

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
- **Consequences:** The `extend` function's signature must not preclude a later dynamic variant (it already needs head-file content for the static safety checks). The rendered `@@ … @@ <section header>` text is preserved as-is, except in one case decided at P3 review (D2, 2026-10-06). When the section line itself falls inside the added pre-context, the header text is dropped, matching upstream with dynamic context off. The function header is then already visible in the context lines.

#### DQ-2 — Language ranking without a languages API
- **Decision:** Language ordering is derived locally for every provider: classify each changed file by extension via the embedded extension map, sum patch bytes per language, sort descending; unmatched files go to the final "Other" group. The Gitea languages endpoint is **not** used in v1.
- **Rationale:** One provider-agnostic, deterministic, hermetically testable code path; one fewer API call; ranking by the PR's own languages is at least as relevant as repo-wide byte counts.
- **Consequences:** Documented deviation from upstream (which ranks by repository language sizes). The admission order *within* a language group (token count descending) stays upstream-exact.

### Area B — Token budgeting

#### DQ-3 — Context-window source
- **Decision:** `llm.context_window` (integer tokens) is **optional** (amended by X-15, P8); when set, startup validation rejects a value below 4096, and a set value always wins. When unset, the context window is resolved from the endpoint (X-15). No model registry is shipped; no LiteLLM-style metadata lookup exists.
- **Rationale:** Context windows are model- and deployment-specific (some deployments cap around ~250k); an explicit value is honest, generic, and immune to registry staleness (rc.1 and rc.2 required it; X-15 made it optional). Avoids upstream's silent 32000-token clamp surprise.
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
- **Decided in P7** (X-11, X-13); originally deferred to v2. Kept below: the leanings recorded then, which P7 adopted. Recorded leanings: one Gitea review per run with multi-line suggestions demoted to plain comments; compute Bitbucket `lineType` (ADDED vs CONTEXT) from the hunk model instead of hardcoding; port fingerprint markers and wire them into both providers if committable inline mode ships.

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
- **Decided in P7** (X-11); originally deferred to v2. Recorded leaning, adopted: single review with every anchor pre-validated; unanchorable findings appended to the PR-level comment.

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
- **Decision:** Both tools always return their result to the MCP client. An optional `publish` argument (default `false`) additionally posts the result as a **new PR-level comment** (rendered with the `provider` profile). No inline comments, no persistent-comment editing, no labels in v1. **Amended in P7 (rc.2):** `pr_review` with `publish=true` posts an overview and inline comments, and edits its overview in place (X-11, X-12); `pr_ask` still posts one new PR-level comment. Labels remain out.
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
- **Decision:** (P7 adds `performance_concerns`, X-12.) Fields: `key_issues_to_review` (always; elements `relevant_file`, `issue_header`, `issue_content`, `start_line`, `end_line`), `security_concerns` (`review.require_security`, default true), `relevant_tests` (`review.require_tests`, default true), `estimated_effort_to_review` (`review.require_effort_estimate`, default true; renamed from upstream's bracketed key, range 1–5 in its description). `review.max_findings` default 3.
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

#### X-9 — PR conversation tools (added 2026-10-06, extends O1)
- **Decision:** v1 also ships two LLM-free tools so an MCP client can read a PR's discussion and answer it:
  - `pr_comments` (read-only): PR-level comments and inline review threads, with author, timestamps, file/line anchors and resolved state where the provider exposes them;
  - `pr_comment_reply` (write): a reply inside the thread when the provider supports it (Bitbucket Server). Otherwise (Gitea review threads) a new PR-level comment quoting the referenced comment, and the result says so explicitly.
  Implemented as WP-PR-2e on top of the P2 provider layer, before P3, and covered by the P2 live acceptance.
- **Rationale:** a direct user request. The provider layer already exists, so the feature needs no LLM pipeline. It also makes "read the review feedback and respond" workflows possible in any MCP client.
- **Consequences:**
  - Comment bodies are **untrusted third-party content**: the tool description and the output say so, and bodies are rendered inside fenced blocks with adaptive fences so they cannot break the output structure. The size of the output is capped (number of threads, characters per body) with explicit truncation notes.
  - Replying is an explicit write: the tool is not marked read-only, and it never posts without a non-empty body.
  - `pr_ask`'s statelessness (X-5) is unchanged.

#### X-10 — `serve` identity (added 2026-10-06)
- **Decision:** In `serve` mode every credential arrives with the HTTP request (`X-Review-MCP-Gitea-Token`, `X-Review-MCP-Bitbucket-Server-Token`, `X-Review-MCP-LLM-API-Key`). Provider tokens set in the server's environment are a startup error in serve mode. The LLM key comes either from the request header or, by explicit choice (`serve.llm_key_source = server`), from the server environment, in which case an access token is mandatory. The MCP transport runs stateless, so no session outlives a request.
- **Rationale:** Keeps the per-user token model of stdio on a shared server: one person's identity is never used for another person's call, and nothing secret persists between requests.
- **Consequences:** One credential read point (`Config.WithSecrets` per call). Non-loopback binds require TLS or an explicit opt-out for TLS-terminating proxies. Details and canaries: `docs/plan/P6-spec.md` §1.

#### X-11 — Inline findings (added P7, amends X-1)
- **Decision:**
  - With `publish=true`, `pr_review` posts one **overview** comment (X-12) and each **anchorable** finding as an **inline comment** on its file and line, when `review.inline_findings` is true (default true; per-call `inline_findings` argument).
  - A finding is anchorable when its line resolves inside the provider's own diff hunks: an added or a context line of a changed file (`internal/review/anchor`). Hunks are the provider's, never the extended context the prompt uses.
  - Unanchorable findings appear only in the overview, with a note that counts them.
  - DQ-13/DQ-14/DQ-18: Gitea posts **one review per run**, event `COMMENT`, pinned to the head commit; Bitbucket Server posts one comment per finding with `lineType` **computed** from the hunk model (`ADDED` or `CONTEXT`) and never hardcoded. Anchors are single-line on both providers.
- **Rationale:** Findings belong on their lines; a flat comment buries them. Computing the line type and resolving on the server's own hunks is what the servers accept.
- **Consequences:**
  - The overview is posted first, so a failed inline batch never leaves the PR without it; an inline failure never fails the review.
  - Gitea: no PENDING review is ever left behind; a failed post deletes the draft, and an outcome-unknown failure is never reposted.
  - Gitea posting is refused (class `conflict`) when the token's user already has a PENDING review on the PR, because the server would submit that draft with our comments. (Decided in 7b; confirmed by architect review on PR #9 (2026-10-07).)
  - Error classes `not_owner` (an edit of a comment the token's user did not write) and `conflict` (a request that conflicts with the server's state) are new allowlist classes in X-6. (Decided in 7b; confirmed by architect review on PR #9 (2026-10-07).)

#### X-12 — Persistent overview (added P7, amends X-1, extends X-4)
- **Decision:**
  - The overview is **one comment per PR per review-mcp identity** (the token's user). It ends with the marker `[//]: # (review-mcp:overview:v1)` and is found again by that marker **and** by being authored by the token's own user, then **edited in place** on later runs (`review.persistent_overview`, default true).
  - A marker in anyone else's comment is ignored, never adopted or edited. If several of ours match, the newest is edited and a note counts the older ones.
  - A failed lookup or edit posts a new overview with a note; it never fails the review.
  - It carries the enabled X-4 fields, the new **`performance_concerns`** field (`review.require_performance`, default true; the model answers "No" or text, like `security_concerns`), the run time and head SHA, a findings index and the coverage.
- **Rationale:** One living summary instead of a pile of comments, and security and performance covered.
- **Consequences:**
  - The overview is posted, then edited once to link each finding to its inline comment. (7c; decided, architect review on PR #9 (2026-10-07): it costs one extra edit per first publish and relies on `EditComment`.)
  - The performance field description is the spec wording plus the security field's no-translation sentence, so a non-English review still answers the literal English "No" that the No-detector reads. This deviates from upstream and is recorded in the template header. (7d; decided, architect review on PR #9 (2026-10-07).)
  - Identity: `CurrentUser` is Gitea's `GET /api/v1/user` and, for Bitbucket Server, the `X-AUSERNAME` and `X-AUSERID` headers of `GET /rest/api/1.0/application-properties`. Ownership is checked by id when both sides carry one and by case-insensitive name otherwise, before every edit and for the overview lookup. (7b; decided, architect review on PR #9 (2026-10-07); acceptance I1 verifies it live, including with project and repository tokens.)
  - The Gitea token needs `read:user` for it (setup guide; "verify at A3"). (7b; decided, architect review on PR #9 (2026-10-07).)

#### X-13 — Discussion awareness (added P7, decides DQ-15 for review)
- **Decision:**
  - Before the model call, `pr_review` reads the PR's comment threads, excludes its own comments (marker and author) and renders the rest into the prompt as **untrusted data**; the model is told not to repeat what is already raised.
  - The block is fenced adaptively, bodies are sanitized and capped at 600 characters, at most two replies per thread, and the block is budgeted by `review.max_discussion_tokens` (default 1500, 0 disables), clipped by whole threads with the newest first, counted before the diff budget. Comment text is never logged (X-8).
  - Inline findings carry a fingerprint marker; a finding whose fingerprint is already on the PR from the same identity is not posted again (`skipped_duplicate`).
- **Rationale:** A review that repeats the humans' points is noise; reviewers' comments are the best signal of what is already known.
- **Consequences:**
  - A failed read adds a note and the review continues.
  - The discussion yields to the diff: when the context window is too small for both, the discussion is dropped. (7e; decided, architect review on PR #9 (2026-10-07).)
  - Prompt injection through comments cannot be excluded by construction; acceptance I5 observes the model live.

#### X-14 — `pr_comment_create` (added P7, extends X-9)
- **Decision:**
  - A sixth tool posts a new comment for the client: PR-level, or inline at `file` + `line`. `body` is required (at most 20000 characters, counted in runes as `pr_ask` counts its question).
  - An inline request whose line is not on the new side of the file's diff hunks is **refused** with a fixed sentence, "the line is not part of the pull request diff; use a changed or context line of a changed file", and is never silently downgraded to a PR-level comment. The anchor is the same resolver as the review's.
  - Serve mode: `RequireCredentials` and the per-call scope as for the other tools; the call needs the provider token and no LLM key.
- **Consequences:**
  - **Marker lines refused (decided, architect review on PR #9 (2026-10-07)):** `pr_comment_create` and `pr_comment_reply` refuse a body with a line that, trimmed, starts with `[//]:` and contains `review-mcp:`, before any request. A marker in a comment of ours would be adopted by the overview or the duplicate lookup, because the author matches; Gitea's reply is a new PR-level comment of ours, so it is affected as well.
  - **URL redaction (decided, architect review on PR #9 (2026-10-07)):** the URLs in the `pr_comment_create` and `pr_comment_reply` results are built by the provider from the configured base URL plus the comment id (Gitea `...#issuecomment-N`, Bitbucket Server `...?commentId=N`). Config validation forbids credentials in a base URL, so they are returned unredacted and the deep link works. `logging.RedactURL` is for logs only, and every log line stays redacted.
  - The tool is not read-only, not destructive, not idempotent, open-world.

#### X-15 — Context window from the endpoint (added P8, amends DQ-3)
- **Decision:**
  - `llm.context_window` becomes **optional**. A set value always wins.
  - When it is unset, review-mcp asks the endpoint once per process: `GET {llm.base_url}/models`, then the entry whose `id` equals `llm.model` exactly.
  - It reads the first of these fields that holds a positive integer (a JSON number or a numeric string): `max_model_len`, `context_length`, `context_window`, `max_context_length`. It uses **90 %** of that value, rounded down, with a minimum of 4096.
  - Training-size fields are **never** used: `n_ctx_train`, `meta.n_ctx_train`, and model metadata in general. A server often serves a smaller context than the model was trained with, and that is exactly the mistake that makes prompts truncate silently.
  - If no field is found, the call fails with a fixed sentence that names `llm.context_window`. It never guesses.
- **Consequences:**
  - `diag diff --context-window N` works without any LLM access.
  - **Decided, architect review on PR #10 (2026-10-07):** `server_info` keeps the integer `context_window` plus a separate source field; it reports `auto (endpoint)` until the value is resolved.
  - **Decided, architect review on PR #10 (2026-10-07):** an endpoint window that resolves below 4096 is refused, not floored into use.
  - **Decided, architect review on PR #10 (2026-10-07):** concurrent first probes coalesce into one request, and a failure is not cached, so a later call can succeed once the endpoint is up.
  - **Decided, architect review on PR #10 (2026-10-07):** the process-global cache is keyed by `(base_url, model)`. The cached number is not a secret; the LLM key is never stored.

#### X-16 — Background jobs for long calls (added P8, stdio only)
- **Decision:**
  - `pr_review` and `pr_ask` wait at most `wait_seconds` for their result (argument, 0 to 600; default `llm.wait_seconds` = 45).
  - If the run is still going, the tool returns a **running** result with a `job_id` and a fixed instruction to call `job_result`. The run continues in the server process, and `publish=true` still happens inside it.
  - A new tool, `job_result(job_id, wait_seconds?)`, waits again up to `wait_seconds` and returns the finished result exactly as the original tool would have, or the running status.
  - A fast run returns exactly today's result, with no `job_id`.
  - This keeps every call under a typical 60 s client timeout whatever the model speed, and does not depend on any client setting.
  - **serve mode does not use background jobs:** calls stay synchronous, `wait_seconds` is ignored and there is no `job_result`. X-10 requires credentials to live no longer than the request; a background job would outlive it. A serve client sets its own timeout.
- **Consequences:**
  - At most 4 jobs run at once; the fifth call is refused with "too many background jobs are running; wait for one to finish" (decided, architect review on PR #10 (2026-10-07)).
  - Finished jobs are dropped 30 minutes after they finish; at 64 kept results the oldest finished job is evicted (decided, architect review on PR #10 (2026-10-07)). A running job is never dropped.
  - **Prepare/run split (decided, architect review on PR #10 (2026-10-07)):** no job is created before argument validation, URL resolution and credentials succeed; those failures are returned directly.
  - **Output schemas (decided, architect review on PR #10 (2026-10-07)):** the three tools declare a root `oneOf` output schema (result or running status). Whether opencode and Claude Code accept it is checked live (acceptance J2). The fallback is pre-chosen: if either rejects it, `outputSchema` is dropped for `pr_review`, `pr_ask` and `job_result` in stdio only, keeping structured content, and ships in the next release candidate.
  - Jobs live in memory only; nothing is written to disk, and running jobs are cancelled at shutdown.

#### X-17 — Latency controls (added P8)
- **Decision:**
  - `llm.timeout_seconds` rises to **300**. 120 is too short for local models on large prompts.
  - A new optional cap, `diff.max_tokens` (at least 1000, unset by default), caps the diff budget below the context window, so that large windows do not imply very slow requests. The budget is `min(the context-window budget, diff.max_tokens)`; the prompt scaffolding and the reserve rules are unchanged, and `pr_ask` uses the same budget. Files left out are reported in the coverage section as usual (X-3). `diag diff` and `diag review --dry-run` report which limit applied.

#### X-18 — A partial review says so first (added P9, extends X-3)
- **Origin:** on `rc.3` a review of a large PR covered 5 of 62 changed files because of a diff budget, and the client model summarised it as "no major issues". The coverage section (X-3) was correct but last, and nothing told the reader that the "no concerns" statements covered only part of the PR. Owner priority: completeness and correctness come before speed.
- **Decision:**
  - A result is **partial** when at least one reviewable changed file was omitted by the budget, clipped, skipped by the provider because of its size or file limit, or unreadable. Files filtered by ignore rules or generated-file rules do **not** make a result partial: they were excluded on purpose and the coverage section lists them.
  - `coverage` gains `partial` (bool), `reviewed_files` (fully included), `total_files` and `not_reviewed_files`, with `reviewed_files + not_reviewed_files == total_files`. They are in `pr_review`, `pr_ask` and `job_result` and in every output schema.
  - A partial result leads, in every rendering and before anything else, with a fixed banner: `**Partial review: R of T changed files were reviewed. N files were not reviewed (see Coverage); nothing is concluded about them.**` (`pr_ask`: `Partial answer: R of T changed files were used for this answer. …`). Numbers of 1 take the singular form, as the notes do. In the client text it is the first line, before `## PR Review`; in the published overview it sits directly under the heading (Gitea: a `> ⚠️` blockquote, Bitbucket Server: a bold line), and because the overview is edited in place by rendering it again (X-12), every edit carries it. The published `pr_ask` comment has no title, so the banner is its first line.
  - When partial, "no concerns" is scoped: "No security concerns identified in the reviewed files", "No performance concerns identified in the reviewed files", and a run without findings reads "No key issues found in the reviewed files". A complete run keeps the unscoped sentences ("No major issues detected" for an empty findings list).
  - The tool descriptions of `pr_review`, `pr_ask` and `job_result` tell the client model to report how many files were not reviewed and never to say they have no issues.
  - A partial result adds a note: "To review every file, raise or unset diff.max_tokens, or use a model with a larger context window." when `diff.max_tokens` is the limit that applied (`Budget.Limit()`), else "To review every file, use a model with a larger context window." Files lost for a reason the budget does not change (too large for the provider, unreadable) get their own note instead.
- **Mapping of the coverage categories** (one definition, `llmrun.Coverage.Tally`):
  - Included: reviewed.
  - Clipped: not reviewed (the model saw only part of the file).
  - Omitted (added, modified, deleted): not reviewed. Deleted files count: X-3 reports them as left out to fit the context window, and on the compressed path the model sees only their names; a banner that excluded them would disagree with the coverage section it points to.
  - Skipped `size_limit`, `file_limit`, `fetch_failed`, `unparseable_patch`, and any reason not known: not reviewed (a reviewable file was lost; an unknown reason is never treated as fine).
  - Skipped `binary` and `empty_diff` (a pure rename or mode change): outside the count, like Filtered. There is no text change to review. They stay listed in the coverage section.
  - Filtered: outside the count.
  - Providers truncate nothing; a file too large for a provider is skipped whole (`size_limit`).
- **Consequences:** `total_files` is the number of changed files that carry a reviewable text change, not the number of files in the PR. The diff-trimming guard (`diff_trimmed`) recomputes the counts after it moves files.

#### X-19 — Chunked review (added v1.1, RC-11 to RC-14)
- **Origin:** on `rc.3` a 62-file PR was reviewed on 5 files because of the diff budget (X-18 made that visible; this decision covers the rest). Owner priority: completeness and correctness before speed. Background jobs (X-16) remove the client-timeout obstacle to several model calls.
- **Decision:**
  - When the prepared diff leaves files out, `pr_review` packs the remaining files into further parts with the same admission rules (`diffpipe.PrepareChunks`), up to `review.max_chunks` parts in total (`REVIEW_MCP_REVIEW_MAX_CHUNKS`, default 8, 1 to 32). Each further part gets the files earlier parts did not include, clip or list, in the original rank order, and no provider skips (those are accounted once, from part 1). A file is in at most one part.
  - A part that fits in full is sent with extended context, like a small pull request.
  - A file too large for a further part of its own: `large_patch_policy = clip` makes the clipped file that part's content; `skip` records it as skipped with reason `too_large` (not reviewed) and packing continues. A further part never returns "does not fit"; only part 1 can, as before. A part with nothing reviewable never becomes a model call.
  - Each part is one model call with the same scaffolding (system prompt, title, description, discussion block budgeted per call) and, when there is more than one part, one line before the diff: "This pull request is large and is reviewed in N parts. This is part I of N. Review only the files in the diff below; the other files are reviewed separately." (an intentional deviation from upstream, recorded in the template header). The diff budget of a review in parts reserves that line; a review in one call keeps the v1.0 budget and prompts byte for byte.
  - Calls run sequentially (local endpoints are usually single-slot). Progress reports `calling model (part I of N)`. Each call has its own `llm.timeout_seconds`, YAML repair and re-ask.
  - **Merge (RC-12):** findings in part order, then model order; a finding whose fingerprint (X-13) an earlier part returned is dropped; findings already posted on the PR are handled by the X-13 inline dedup as in a single-call review; the list is capped at `review.max_total_findings` (`REVIEW_MCP_REVIEW_MAX_TOTAL_FINDINGS`, default 10, at most 50), and the rest are counted in the note "N further findings were not shown because of review.max_total_findings." Each part still asks for at most `review.max_findings`. Effort is the maximum of the parts that returned one; tests are true if any part says true, false if every part that answered says false, absent if none answered; security and performance concerns are the concern texts when at least one successful part has a concern (joined by a blank line, each prefixed `Part I:` only when more than one part has one), "No" only when every successful part said "No", and otherwise (some parts said "No", another left the field out) absent, with the note "Part I did not answer the security question; nothing is concluded about its files." (or the performance equivalent) per silent part and field, so that "No" never covers files whose part said nothing (architect, DQ-6 on 11e2); when no part answers, the field is absent without a note, as in a single-call review. Snippets, links and inline anchoring run on the merged list against the full PR file set.
  - **Failed part:** a part whose call fails (timeout, LLM error, unparseable after repair and re-ask) makes its files not reviewed with reason `model_call_failed` and adds the note "Part I of N failed (<fixed error class>); its files were not reviewed." The class is a fixed token, never error text. If every part fails, the run returns the first part's classified error. If at least one part succeeds, the result is partial (X-18) and publishable.
  - **Honesty (RC-13):** coverage counts all parts; `coverage` gains `model_calls` (parts sent to the model; re-asks are not counted) and `failed_parts`, in every schema that carries `coverage`. Client and provider renderings add "Reviewed in N model calls." to the coverage section when N > 1. Files left after `review.max_chunks` parts stay not reviewed; the partial hint then also names `review.max_chunks` ("To review every file, raise review.max_chunks, …"), and names `diff.max_tokens` only when it was the limit that applied (X-18).
  - **Time (RC-14):** stdio uses the X-16 background job unchanged. Serve mode stays synchronous; serve clients raise their tool timeout or set `review.max_chunks = 1`.
- **As implemented (lead decisions on 11e2, open to the architect):** `review.max_total_findings ≥ review.max_findings` is checked only when `review.max_total_findings` is set explicitly; at run time the cap is the larger of the two, so a v1.0 configuration with `review.max_findings` above 10 stays valid and a single part never loses findings. The "raise review.max_chunks" hint appears only when `review.max_chunks` is greater than 1. For a review in parts, `metadata.diff_tokens` is the sum over the parts and `metadata.request_tokens` the largest part's request.
- **Consequences:** a review in N parts takes about N times as long as one call. `diff.max_tokens` now makes each part smaller, not the review shorter.

#### X-20 — Deletions listed by name are not budget losses (added v1.1, settles 9a DESIGN-QUESTION 1)
- **Decision:**
  - A deleted file whose patch the deletion handling drops by design, and whose name is in the prompt's deleted-files section, counts as **reviewed**: its deletion was shown to the model. `Prepared` and `coverage` list it under `deleted_listed`; the coverage section shows it under "Deleted (listed by name)".
  - Only deletions whose names are not in the text (cut by the budget, or the section did not fit) stay in `Omitted.Deleted` and count as not reviewed. The request-size guard moves a listed deletion whose name it cuts back to `Omitted.Deleted`.
  - A deleted file that has a patch is reviewed only through its patch; its name in the section does not count.
  - In a review in parts (X-19) a name is listed in at most one part.
- **Consequences:** the accounting invariant gains `len(DeletedListed)`; `Tally()` counts it as reviewed; `deleted_listed` is in every schema that carries `coverage`.

#### X-21 — `pr_ask` does not chunk (added v1.1, RC-15)
- **Decision:** `pr_ask` keeps one model call. Merging several answers needs a further reduce call and its own honesty rules; that is backlog. Instead, the files the question names are admitted first:
  - A changed file is named when its full path, or its base name of at least 5 characters, occurs in the question, case-sensitive, as a whole token: bounded by the start or end of the question or by a character that is not a path character (letters, digits, `.`, `/`, `-`, `_`), with full stops allowed right after the name. A full path may be written with a leading `./`, which is skipped before the same rule is applied (architect, on 11e2): `./src/a.go` names `src/a.go`, `../src/a.go` does not.
  - Named files keep their relative order and go before the ranked rest. Pinning never overrides filters: only the reviewable files after filtering are candidates.
  - Coverage and the X-18 banner are unchanged; `coverage.model_calls` is 1 (0 without a call) and `failed_parts` 0.
- **Consequences:** a question about one file of a large PR is answered from that file when it fits the budget. `pr_ask` chunking stays in the backlog.

#### X-22 — Repository context (added v1.1, RC-1 to RC-10)
- **Origin:** a diff-only review misses the effect of a change on the rest of the project (callers of a changed function, other implementations of a changed interface). The design note `docs/design/v1.1-repo-context.md` holds the rationale; the v1.1 spec §3.0 settled its open questions. Guide: `docs/repo-context.md`.
- **Decision (RC-1 to RC-10):**
  - **Opt-in, stdio only (RC-1).** `context.repo.enabled` defaults to false; in serve mode it is a startup error with a fixed sentence (a shared server would hold code fetched with one user's token).
  - **System `git`, no new module (RC-2).** Minimum 2.31, checked once per process; a missing or old `git` is a note, never an error.
  - **Credentials (RC-3).** The token reaches `git` only through the environment of the one fetch process (`http.extraHeader` as `GIT_CONFIG_*`); never argv, disk, logs or errors. Gitea: `Authorization: token`, then on a 401 HTTP Basic with the token user's name; Bitbucket Server: `Bearer`, then Basic; the scheme that worked is cached for the process (§3.0 items 1 and 2).
  - **Pinned clone URL (RC-4)** from the configured base URL and the resolved repository; no redirects.
  - **Fetch and verify (RC-5).** The PR ref (`refs/pull/<n>/head`, `refs/pull-requests/<n>/from`) with `--depth=1 --filter=blob:limit=1m --no-tags`; the fetched commit must equal the provider's head SHA.
  - **Cache lifecycle (RC-6).** Idle and LRU sweeps at the start of every use, a lock file per repository, `diag cache [--prune]`.
  - **Symbols and uses (RC-7).** Symbols from hunk headers and definition lines, `git grep -w -F` on the head commit with the PR's own files excluded by pathspec and again by a post-filter.
  - **Block (RC-8).** After the discussion block and before the diff, counted inside the prompt tokens, clipped by whole entries; used by `pr_review` and `pr_ask`.
  - **Honesty (RC-9).** A coverage line and `coverage.repo_context` (`status`, `reason`, `symbols`, `references`, `files`) in every schema that carries `coverage`.
  - **Measure before claiming (RC-10).** `diag review --repo-context=on|off` and `tools/evalrepo`; stage 2 (a code graph) starts only if the owner's ratings show a clear gain on at least 20 pull requests.
- **Settled on PR #13 (architect):**
  - **Isolation.** Every `git` child gets an allowlisted environment (never the parent's), `HOME` and `XDG_CONFIG_HOME` at an empty `<cache_dir>/.home`, `GIT_CONFIG_NOSYSTEM=1` except on Windows, and before any request `git ls-remote --get-url origin` must print exactly the pinned URL (`insteadOf` guard; reason `redirect`). Expanding `origin`, not the literal URL.
  - **Proxy and TLS** mirror the provider client: proxy variables passed on, `ca_cert` as `http.sslCAInfo` (with `http.sslBackend=openssl` on Windows), `insecure_skip_verify` as `http.sslVerify=false`.
  - **Cache layout** `host[_port]/<escaped base path or _>/namespace/repo`, fixed depth with injective escaping (a nested namespace is one directory, segments joined with `+`; fixed depth stays the invariant), so two instances on one host never share an entry; dot-prefixed host names are skipped; the old three-level layout is not recognised.
  - **Offline reads.** Searches run with no credential, `protocol.allow=never` and `GIT_NO_LAZY_FETCH=1`; a blob the partial clone lacks is counted as skipped, never fetched. `GIT_NO_LAZY_FETCH` is set where git supports it; on older git `protocol.allow=never` alone blocks lazy fetches, which is tested. Errors from `git` are mapped to fixed reasons; stderr is never passed on.
  - **Ranking.** Removed or renamed, changed signature, other changed (existing), new definitions, then the symbols of test files (by file-name rules), each in diff order.
  - **Budget.** The diff always wins. With `review.max_chunks > 1`, `context.repo.max_tokens` is reserved up front when the head is ready and the diff has symbols, for the one call and for every part alike; files the reservation pushes out go to a further part (acceptable; counted in `model_calls`). With `review.max_chunks = 1` and in `pr_ask`, the block uses only the room left and is skipped with reason `budget` otherwise. A block that would make the request-size guard trim the diff is dropped.
  - **Parts (X-19).** Each part searches the symbols of its own files and has its own block; the head is fetched once. A part excludes only its own files: uses in files another part reviews are shown, marked `(changed in this pull request; reviewed in part J)`, and uses in files no part reviews `(changed in this pull request; not reviewed)`. In one call every PR file is excluded.
  - **Reasons.** `auth`, `not_found`, `timeout`, `too_large`, `sha_mismatch`, `redirect`, `git_failed`, `git_unavailable`, `busy`, `cache_unusable`, `unsupported` (from `gitctx`), plus `budget` and `nothing_to_review` (an empty diff is `skipped`, not `off`; `off` means only "disabled").
  - **Progress.** The fetch is its own stage, `fetching repository context`, before `preparing diff`, reported only when context is on and the diff has symbols; per-part searches add no stage.
- **Consequences:** `context.repo.*` keys in §5 (the shipped `max_tokens` range is 200 to 16000); `coverage.repo_context` in every schema; the prompt templates record the deviation from upstream in their header comments; the cache holds only re-fetchable copies.

#### X-23 — `pr_info`: target branch, reviewers and approvals (added v1.1, Track C, owner request)
- **Origin:** "which branch does this pull request merge into, and who has approved it?" had no tool. The obvious answer, "reviews on the PR", is wrong for a PR that review-mcp itself reviewed: the AI review is a review on Gitea, and counting it would read as an approval.
- **Decision:**
  - `pr_info` is read-only (GET only), makes no LLM call and needs only a read token. It returns the PR facts (title, author, state `open`/`merged`/`closed`, `draft` where the provider has it, branches, head and merge-base revisions with `base_strategy`), the **human** `reviewers` (`user`, `requested`, `state` `approved`/`changes_requested`/`commented`/`pending`, `stale`, `at`), `approvals` counted over them, `required_approvals`, `mergeable` with `merge_blockers`, and `review_mcp_activity`.
  - **Gitea:** per user, the latest non-dismissed review with state `APPROVED` or `REQUEST_CHANGES` decides; `COMMENT` counts only when there is no decisive one; `PENDING` drafts are not reported; a requested reviewer without a review is `pending` and `requested`. `stale` is the review's own `stale` field. `official` is only counted in debug logs. Required approvals come from `branch_protections/{target}`; any failure (403 and 404 in practice) gives `null` with the note "not readable with this token". Gitea gives no structured merge blockers, so `merge_blockers` is empty and `mergeable` is the PR's own flag.
  - **Bitbucket Server:** only `reviewers[]` is listed (participants that are not reviewers are not): `APPROVED` is approved, `NEEDS_WORK` changes requested, `UNAPPROVED` pending. Every listed reviewer is `requested`, because Bitbucket's reviewers are the ones asked. `stale` is "`lastReviewedCommit` present and not the head" for approved and changes-requested reviewers. The payload has no review time, so `at` is omitted. Merge status comes from `.../merge` (open pull requests only): `canMerge` is `mergeable`; vetoes map to the fixed blockers "required approvals missing", "a reviewer marked the pull request as needs work", "required builds are missing or failing", "merge conflict", and anything else is "other merge check". Required approvals are read from a veto only when its text states the total ("requires 2 approvals"); a remaining count ("1 more approval") or no number gives `null` with the note.
  - **review-mcp's own activity is not a reviewer (X-12, X-13).** A review of the token's user that carries a review-mcp marker, in its body or in one of its comments (the inline batch has an empty body and marked comments), is left out of `reviewers`; `review_mcp_activity` is `{overview, inline_findings}` from the token user's marked overview and fingerprint comments. A marker in anyone else's text counts for nothing. A plain review by the same account, with no marker, is a reviewer.
  - **Honesty:** every optional part (reviews, protection, merge status, the token's user, the comments) failing leaves its fields `null` with a fixed note; the tool fails as a whole only when the PR itself cannot be read, with the existing provider sentences. Comment and review bodies are inspected for markers and never stored, returned or logged; veto and error text is never passed on; display names pass through the markdown sanitizer. `null` is never a guess: an unknown required-approvals number, an unknown merge verdict and an unreadable review list are `null`, not 0 or an empty list.
- **As implemented (lead to confirm):** `mergeable` is `null` for a PR that is not open; a state the tool does not know is `unknown`; Gitea team reviewers are not listed; if the token's user cannot be read, nothing is excluded and a note says reviews by that user may include review-mcp's own.
- **Consequences:** the `Provider` interface gains `GetReviewStatus`; the veto and approval wording of Bitbucket Server is unconfirmed until live acceptance (M4).

#### X-24 — Provider contract suite (added v2, Y-1; WP-2a)
- **Origin:** every provider so far had its own tests, and a behaviour that one of them dropped could pass unnoticed (rename detection, the ownership check before an edit, a response body in an error text). GitHub and GitLab are next, and each must be held to the same behaviour. Design note `docs/design/v2-design.md` §2.
- **Decision:**
  - `internal/provider/contract` has a small exported `Fixture` interface (`Kind()`, and `Serve(t, spec)` returning a provider pointed at a fake server that serves the synthetic pull request in a provider-neutral `Spec`) and `Run(t, f)`. Gitea and Bitbucket Server implement `Fixture` in their own test packages and call `Run`; their older tests stay.
  - The cases: metadata round trip and the documented base strategy; the file list (modified, added, deleted, renamed with edits, binary, size limit, file limit); hunks and contents byte for byte; threads (general before inline, replies in order, resolved threads hidden where the provider reports resolution) and reply semantics (`in_thread`); the ownership check on edit, before any write request; inline posting on an added line, a context line and an out-of-hunk line; review status folding; `FileLineURL`; the error classes for 401, 403, 404, 500 and a network failure, with a sentinel in every fake error body that must never appear in an error text; and, from WP-2d, `UpdatePullRequest`.
  - `Run` never branches on a provider kind. Facts the `Provider` interface does not expose come from an optional `Declarer` the fixture implements: the documented base strategies (exported by each provider package, so the fixture only forwards them) and whether the provider omits the no-newline marker.
  - The package imports `testing`, so a guard (`go list -deps`) fails if any non-test package imports it.
- **Settled on PR #16 (architect):**
  - Resolution is two explicit capabilities, `InlineThreadResolution` and `GeneralThreadResolution`; the per-provider trait that said which thread kinds are resolvable was deleted and the `capabilities` case reads the flags (see X-25).
  - A file the limits cut has two valid shapes, and the contract checks each exactly: listed with a `not_fetched_file_limit` or `not_fetched_size_limit` status and its patch (Gitea), or skipped with `file_limit` or `size_limit` and no patch (Bitbucket Server). A file whose patch is in the prompt is reviewed (only extended context is missing); a file the model never saw is not reviewed and raises the X-18 banner. This is documented on `Provider.GetDiff`.
  - Bitbucket Server builds its patches from file contents and omits `\ No newline at end of file`. The marker is documented as present only when the host provides it, consumers must not rely on it, and the contents stay byte-exact.
  - `in_thread` of a reply is checked against the thread's own `ReplyInThread`. General-thread replies are in the suite (a Gitea quoting comment with `in_thread=false`; in the thread on Bitbucket Server).
  - There is no "unanchorable" result class in this phase. `InlineResult` gains a `Reason` (`posted`, `unanchorable`, `failed`) in the phase that needs it (`pr_improve`).
- **Consequences:** a provider behaviour change that breaks the contract fails that provider's tests only. Adding a provider means writing a fixture, not new cases.

#### X-25 — Nested namespaces and capabilities (added v2, Y-2, Y-3; WP-2b)
- **Origin:** GitLab addresses a project as `group/subgroup/.../project`, and later providers differ in what they can do. Branching on the provider name does not scale.
- **Decision:**
  - `PRRef.Namespace` may contain `/`. Each factory's `ParsePRPath` decides how many segments are namespace. Gitea and Bitbucket Server take exactly one, and refuse an escaped slash (`%2F`) in the namespace and in the repository segment (such a URL is `url_malformed`). Every place that builds a URL, a cache key or a log field from the namespace escapes it per segment (`provider.EscapeNamespace`): a clone URL keeps the real `/`, `logging.RedactURL` leaves a nested path unchanged, and the X-22 cache stores a nested namespace as one directory (the `+` join is recorded in X-22).
  - `Capabilities` gains `SuggestionBlocks` (an inline comment can carry a native suggestion block), `QuickActions` (a published line that starts with `/` runs a quick action), `InlineThreadResolution` and `GeneralThreadResolution` (whether the provider reports a thread of that kind as resolved) and `DescriptionEdit` (the pull request description can be updated). Gitea: inline resolution, `DescriptionEdit`. Bitbucket Server: both resolution flags, `DescriptionEdit`. No provider has the others yet. Code branches on capabilities only.
  - The P5 rule (a line that starts with `/` gets a leading space) moves from `pr_ask` into `provider.SanitizeBody(caps, body)`, which every publisher calls (overview, inline, reply, new comment, describe) and which changes a body only when `QuickActions` is set. Today no provider sets it, so published output is unchanged.
- **Settled on PR #16 (architect):**
  - Resolution is split in two flags (decided on WP-2a); the planned single `ThreadResolution` was never released.
  - `pr_ask` keeps sanitising its question and answer on every provider (defence in depth; its v1 output stays byte for byte). `SanitizeBody` is the cross-tool rule on top.
  - An escaped slash in the repository segment is refused too, with tests for both providers (a follow-up to WP-2b).
  - The `FilePatch.Patch` and `Provider.GetDiff` documentation changes of X-24 belong to this package.
- **Consequences:** a provider's behaviour is declared in one struct. The sanitisation is not covered by a provider that sets `QuickActions` until one exists (GitLab); a test with a fake capability proves it for every body kind.

#### X-26 — `pr_describe` (added v2, Y-4 to Y-7; WP-2c, WP-2d)
- **Origin:** owner request (design note `docs/design/v2-design.md` §3): a title, summary and files walkthrough for a pull request, on the providers already in use, without line anchoring. Guide: `docs/describe.md`.
- **Decision:**
  - **Tool.** `pr_describe` (stdio and serve) with `pr_url`, `output_language`, `publish` (default false), `publish_mode` (`comment`, default, or `description`), `update_title` (default false) and, in stdio mode, `wait_seconds`; a background job like `pr_review` (X-16). Invalid `publish_mode`, and `update_title` without `publish=true` and `publish_mode=description`, are argument errors with fixed sentences. The tool is not read-only (publishing writes) and not destructive.
  - **Prompt and schema (Y-4).** Adapted from PR-Agent's `pr_description_prompts.toml` at the pinned revision (MIT, NOTICE): `type` (`Bug fix`, `Tests`, `Enhancement`, `Documentation`, `Other`), `description` (up to four bullets), `title`, `pr_files` (`filename`, `changes_title`, `changes_summary`, `label`). Dropped: the diagram, ticket, skills and repository-context blocks and custom labels; every deviation is in the template header. Labels on the provider are not set.
  - **Inputs.** Title, description (`diff.max_description_tokens`), source and target branch, commit messages (`diff.max_commits_tokens`) and the plain diff under the usual budget. No discussion (the change is described, not the conversation) and no repository context.
  - **Parts and the summary call (Y-6).** When the diff leaves files out and `review.max_chunks` is above 1, the files are packed as X-19 packs them. Each part asks for `pr_files` only and may describe only its own files. One further call with no diff, over the merged walkthrough, returns `title`, `type` and `description`. If it fails or cannot fit, `title` and `type` are null, the description is the files' titles, and the fixed note "The summary could not be generated; the walkthrough lists the described files." is added. A failed part follows X-19 (its files are not described, `model_call_failed`).
  - **Validation.** Types outside the enum, walkthrough entries for a path the call was not shown and duplicate paths are dropped and counted in a note; labels are one line of at most 40 characters. Parsing and the one re-ask are those of `pr_review`.
  - **Honesty (Y-7, extends X-18).** Every changed file is described, listed under "Not described" with its reason, or filtered on purpose. A file the model was shown and did not describe gets the skip reason `not_returned`; upstream's padding of the walkthrough is not ported. The X-18 banner reads "Partial description: R of T changed files were described. N files were not described (see Coverage)."
  - **Result.** `title`, `type`, `description`, `files`, `coverage` (the standard object, "reviewed" reading "described", with `model_calls` and `failed_parts`), `notes`, `metadata`, `publish`; registered like `pr_review`, with the running-status branch and `job_result` support.
  - **Publishing: comment (Y-5).** One comment whose last line is `[//]: # (review-mcp:describe:v1)`, found with the X-12 rule (marker plus author) and edited in place; older duplicates are noted, never deleted; rendered per provider profile.
  - **Publishing: description (Y-5).** Requires `DescriptionEdit`, else the fixed sentence "This provider does not support editing the pull request description; use publish_mode=comment." The managed region is the lines from `[//]: # (review-mcp:describe:start)` to `[//]: # (review-mcp:describe:end)` inclusive: appended after the author's text and one blank line on the first run, replaced on later runs; the text outside it is never changed. A damaged region (two or more starts or ends, an end before a start, a missing marker) is refused with "The pull request description contains a damaged review-mcp region; fix or remove it and run again." and nothing is written. `update_title=true` replaces the title; otherwise it is untouched.
  - **Concurrency.** The pull request is read again right before the write. If the description changed, the region is computed again once; Bitbucket Server sends the fresh read's `version` and treats a `409` as the same single retry; a second change refuses with "The pull request description changed while it was being updated; nothing was written." Gitea has no version, so the comparison of the text is its only check.
  - **Provider method.** `UpdatePullRequest(ctx, ref, UpdatePR{Title, Description, Version})`, validated before any request. Gitea: `PATCH` with only the fields that are set. Bitbucket Server: a full `PUT` (version, title, description, the reviewers by name from a fresh read, the draft flag), because an update without the reviewer list wipes them.
- **Settled on PR #16 (architect):**
  - Upstream's `max_items=20` is removed; a file the model leaves out is reported as `not_returned`. Commit messages go through `diff.max_commits_tokens`. `output_language` applies to every call, the summary call included. `update_title` is refused unless `publish=true` and `publish_mode=description`.
  - The walkthrough entry of a clipped file is kept but marked "(partial: only part of this file was shown)" in every rendering, and the file keeps counting as not described.
  - Published text: title, types and labels are escaped as in the client view; summaries keep their list markers and go through the same sanitising as published review text (no raw HTML on Bitbucket Server, the profile's escaping, `provider.SanitizeBody`). Model text cannot form a marker line.
  - `coverage.model_calls` counts the calls with a diff; `metadata.llm_calls` counts every completion, the summary call and re-asks included.
  - Fences are not special: a marker line inside a code fence counts, because tracking fences would let an unclosed fence hide the region and append a second one.
  - The region is written with the line endings of the document. A run that would write the same bytes sends nothing. `idempotentHint` stays false. "Publishing" appears in the client text only when publishing ran. `update_title` without a generated title leaves the title alone with a note ("The title was not generated, so the pull request title was not changed.").
  - After a successful full-replace write the reviewers are read again and compared with those read before it; a dropped reviewer or changed state adds "Bitbucket Server changed the reviewer list or a review state while the description was updated; check the pull request.", and an unreadable comparison adds a note that it could not be ruled out. It cannot be undone, but it is never silent.
  - Nothing described: description mode writes nothing and returns "Nothing was described, so the pull request description was not changed." Comment mode still publishes the coverage, as `pr_review` does.
  - A Gitea draft is a title prefix: `update_title` keeps the pull request's own `WIP:` or `[WIP]` prefix in front of the generated title when the server reports a draft, and strips such a prefix from the generated one. A non-default `WORK_IN_PROGRESS_PREFIXES` is not known to review-mcp. Bitbucket Server echoes the `draft` flag.
- **Consequences:** `NOTICE` lists the adapted templates; the porting map has the `/describe` section. Live checks (reviewer states surviving the Bitbucket `PUT`, the Gitea draft prefix) belong to the in-use acceptance N3 and are not verified by tests against a real server.

#### X-27 — `pr_improve` (added v2, Y-8 to Y-11; WP-2f to WP-2i)
- **Origin:** owner request and PR-Agent's `/improve` (design note `docs/design/v2-design.md` §4; spec `docs/plan/v2-2E-spec.md`): concrete code suggestions, scored, tied to the right lines of the right file, under the X-12 and X-13 rules. Guide: `docs/improve.md`.
- **Decision:**
  - **Tool.** `pr_improve` (stdio and serve) with `pr_url`, `output_language`, `publish` (default false) and, in stdio mode, `wait_seconds`; a background job like `pr_review` (X-16). Not read-only (publishing writes), not destructive, not idempotent. The server has ten tools in stdio mode and nine in serve mode.
  - **Prompts and schema (Y-8).** Adapted from PR-Agent's `pr_code_suggestions_prompts.toml` and `pr_code_suggestions_reflect_prompts.toml` at the pinned revision (MIT, NOTICE): per suggestion `relevant_file`, `language`, `existing_code`, `suggestion_content`, `improved_code`, `one_sentence_summary`, `label`; no line numbers are asked for in the suggestion pass. Every deviation is in the template headers. `focus_only_on_problems` is fixed to upstream's shipped value.
  - **Inputs.** Title, date, branches, description (`diff.max_description_tokens`), the numbered diff (`diffpipe.ModeNumbered`, upstream's decoupled view) under the usual budget, the discussion block (X-13) so that raised points are not repeated, and repository context when enabled (X-22). Not sent: commit messages. The self-review gets the part's numbered diff and suggestions only: no discussion, no repository context.
  - **Parts (X-19).** Exactly as `pr_review`: packing by `review.max_chunks`, sequential calls, a failed part makes its files not reviewed. Each part asks for at most `improve.max_suggestions_per_part` suggestions, may name only its own files, and has its own self-review call.
  - **Self-review (Y-9).** One call per part with suggestions, over that part's diff and suggestions; it returns per suggestion a score 0 to 10, a reason and the new-file line range. A score below `improve.min_score` drops the suggestion and is counted in a note. A failed self-review (error, unparseable after the one re-ask, request does not fit) keeps the part's suggestions **unscored** with a fixed note; a suggestion the answer does not score is unscored as well. Upstream's default score of 7 is not used.
  - **Merge.** One global ranking over the parts: scored suggestions by score descending (ties: part order, then model order), then the unscored ones in part order; fingerprint dedup (X-13: file, summary, existing code) on that ranking; then the cap `improve.max_suggestions` (default 8, up to 30) with a note for the rest.
  - **Validation.** Entries without a file, a summary or the code, entries for a file the call was not shown, and entries whose improved code equals the existing code after whitespace folding are dropped and counted in notes; labels are one line of at most 40 characters.
  - **Verification (Y-10).** `existing_code` must equal the head file at the given lines after `normalizeSnippet` (trailing white space, CRLF, whitespace-only lines, common indentation); otherwise a unique match elsewhere corrects the range (a note counts the corrections), none gives `not_found` and several give `ambiguous`, and the first of several is never taken. A file without full head content is `head_unavailable`, except that a **given** range is verified when every line of it is a new-side line of the patch and equals the quote. An unverified suggestion stays in the result and the overview, marked, and is never posted inline.
  - **Publishing (Y-11).** The overview carries the marker `[//]: # (review-mcp:improve:v1)` and is edited in place (X-12 rules, older duplicates noted and never deleted). Inline comments go only to verified suggestions whose whole range is on new-side lines of one hunk of the provider's own diff, on the first line of the range. With `SuggestionBlocks` the comment holds a native suggestion block; otherwise (Gitea, Bitbucket Server) a fenced `diff` block from the existing to the improved code. No provider sets `SuggestionBlocks` yet. Every body goes through `provider.SanitizeBody` and the published-text escaping of X-26.
  - **Result.** `suggestions[]` (`file`, `language`, `label`, `summary`, `content`, `existing_code`, `improved_code`, `start_line`, `end_line`, `score` or null, `why`, `verified`, `unverified_reason`, `anchor`), `coverage` (standard, with `model_calls` counting the suggestion calls and `failed_parts`), `notes`, `metadata` (`llm_calls` counts every completion, `self_review_calls` the self-review part), `publish`.
  - **Config.** `improve.max_suggestions` (8, 1 to 30), `improve.max_suggestions_per_part` (4, 1 to 10), `improve.min_score` (7, 0 to 10); validation sentences in the existing style; shown in `server_info`.
  - **`InlineResult.Reason`** (`posted`, `unanchorable`, `failed`), planned in X-24, is added to the provider interface and the contract suite; `pr_review` ignores it.
- **Settled on PR #17 (architect):**
  - The suggestion and self-review calls get the same numbered diff, where upstream strips the numbers for the suggestion call; if a model copies numbers into `existing_code`, the verifier is where it is handled, not the prompt.
  - Entries are matched to suggestions strictly: by `suggestion_number` (a field added to upstream's schema), with a present `relevant_file` and `suggestion_summary` required to match, a duplicated number voiding its claims, and positional matching only without a number and with equal counts. A mismatch costs a score, never a suggestion. A run in one call words the failure note as "The suggestions were not scored (the self-review call failed)."
  - **Global ranking, not per part (amends spec §1.5).** The cap must never keep a 7 from part 1 while cutting a 9 from part 3. Test: three parts where the per-part order and the global order disagree under the cap; canary: restore part-first ordering.
  - The self-review budget is reserved in the diff budget (the larger of the two scaffoldings plus the hard output reserve); a self-review that still does not fit is not sent and counts as failed. The self-review carries the output-language instruction. Upstream's sentence that invites an ellipsis in `existing_code` is removed, because an elided quote can never verify.
  - A file a suggestion may name is one whose content the call was shown (included or clipped). `improve.max_suggestions_per_part` is asked for in the prompt, not enforced. Dedup across parts stays: it is structurally unreachable in a real run (a suggestion's file must be in its own part) but matters against already-posted suggestions and is tested directly.
  - A range outside the file is a mismatch that the search may correct; a verified range always lies inside the file. A search-only location is not counted as "corrected"; reasons are per suggestion, with no aggregate note; verification runs after the cap and never reorders.
  - **Head content unavailable: patch-walk (first commit of WP-2h).** For a file without full head content, a given range is verified when every line of it is a new-side line of the patch (added or context) and those lines equal `existing_code` after normalisation. No search and no correction (uniqueness cannot be proven without the file); a missing or non-matching range, a range across a hunk gap and a removed-only position stay `head_unavailable`. `patchLines` moved from `review` to `llmrun` with an identical body (no review golden changed).
  - **The already-posted key excludes the summary.** It is file plus normalised `existing_code` plus normalised `improved_code`, because a model rewords its summary between runs and the second run must recognise its own comment. The inline marker (`review-mcp:suggestion:<key>`, 12 hex digits) carries it, and a comment with the older key (the X-13 fingerprint) from a previous build is still recognised. The X-13 fingerprint, with the summary, stays for the merge dedup. Spec §3.2 amended.
  - **Every review-mcp tool marker is "own" in every tool's discussion block, `pr_review` included.** `ownMarked` uses `llmrun.HasToolMarkerLastLine` (the token user plus a last line starting with `[//]: # (review-mcp:`), so one tool's comments are never shown to another tool's model as human threads. A concrete suggestion for an issue `pr_review` flagged is still useful; human-raised points are what must not repeat. No review golden changed.
  - Points raised by people are handled through the discussion block, as in `pr_review`; there is no deterministic human-raised check.
  - If the PR's threads or the token's user cannot be read, no inline comment is posted (the suggestions are `failed` with a note), because it could repeat one; the overview is still posted. This is safer than `pr_review`, which posts anyway.
  - A Gitea 500 on one comment's request is `unanchorable` (Gitea answers 500 for a position outside the diff). The `anchor` (`status`, `line`, `comment_id`, `url`, `error`) and `publish` (with `inline` counts) fields are as in the guide. Publishing never fails the run.
  - **Native suggestion blocks wait for the GitHub and GitLab providers (2B, 2C).** They need `InlineComment.EndLine` and a suggestion style per provider, and `improved_code` must be re-indented by the difference between the real lines and `existing_code` before a block replaces them; that is a precondition recorded in the 2B spec (design note §5). Nothing sets `SuggestionBlocks` until then; the renderer is tested with a fake capability.
  - Smaller choices kept: `code_suggestions: null` is zero suggestions, a missing key gets the one re-ask, the self-review has the same re-ask, the running noun is "suggestion job", and `diag improve` mirrors `diag describe` and never publishes.
- **Consequences:** `NOTICE` lists the adapted templates; the porting map has the `/improve` section. How well the score threshold, the verification and the line ranges work on real models is unmeasured until the in-use acceptance (spec §5, P1 to P4).

**Confirmed from 9a (v1.1 spec §0):** binary and empty-diff files stay outside `total_files` (9a DESIGN-QUESTION 2), as the X-18 mapping says.

---

## 5. Resulting v1 configuration surface

Secrets are environment-only (in `serve` mode, credentials come from request headers instead; X-10, P6 spec §1.2 adds the `serve.*` rows). All other keys may come from the TOML file or the environment (DQ-23/24). "—" means no default (required or optional-unset).

| Key (TOML) | Env | Default | Notes |
|---|---|---|---|
| — | `REVIEW_MCP_CONFIG` | — | Optional path to TOML file |
| — | `REVIEW_MCP_LLM_API_KEY` | — | **Secret**, required |
| — | `REVIEW_MCP_GITEA_TOKEN` | — | **Secret**, required iff Gitea enabled |
| — | `REVIEW_MCP_BITBUCKET_SERVER_TOKEN` | — | **Secret**, required iff Bitbucket Server enabled |
| `llm.base_url` | `REVIEW_MCP_LLM_BASE_URL` | — | Required |
| `llm.model` | `REVIEW_MCP_LLM_MODEL` | — | Required |
| `llm.context_window` | `REVIEW_MCP_LLM_CONTEXT_WINDOW` | — | Optional, ≥ 4096 when set; unset means resolved from the endpoint (DQ-3, X-15) |
| `llm.max_output_tokens` | `REVIEW_MCP_LLM_MAX_OUTPUT_TOKENS` | — | Optional (DQ-4) |
| `llm.temperature` | `REVIEW_MCP_LLM_TEMPERATURE` | — | Optional, sent only if set (DQ-26) |
| `llm.seed` | `REVIEW_MCP_LLM_SEED` | — | Optional |
| `llm.reasoning_effort` | `REVIEW_MCP_LLM_REASONING_EFFORT` | — | Optional pass-through |
| `llm.timeout_seconds` | `REVIEW_MCP_LLM_TIMEOUT_SECONDS` | 300 | X-17 (120 in rc.1 and rc.2) |
| `llm.max_retries` | `REVIEW_MCP_LLM_MAX_RETRIES` | 1 | Transport retries (DQ-9) |
| `llm.token_estimate_factor` | `REVIEW_MCP_LLM_TOKEN_ESTIMATE_FACTOR` | 0.3 | DQ-5 |
| `llm.wait_seconds` | `REVIEW_MCP_LLM_WAIT_SECONDS` | 45 | X-16; 0 to 600; stdio only; per-call `wait_seconds` argument |
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
| `diff.max_tokens` | `REVIEW_MCP_DIFF_MAX_TOKENS` | — | Optional, ≥ 1000 when set (X-17) |
| `diff.ignore_generated_frameworks` | `REVIEW_MCP_DIFF_IGNORE_GENERATED_FRAMEWORKS` | (empty) | Names from embedded generated-code table |
| `ignore.glob` | `REVIEW_MCP_IGNORE_GLOB` | `vendor/**` | doublestar semantics |
| `ignore.regex` | `REVIEW_MCP_IGNORE_REGEX` | (empty) | RE2 dialect; compile errors fail startup |
| `review.max_findings` | `REVIEW_MCP_REVIEW_MAX_FINDINGS` | 3 | Per-call override (DQ-25) |
| `review.require_tests` | `REVIEW_MCP_REVIEW_REQUIRE_TESTS` | true | X-4 |
| `review.require_security` | `REVIEW_MCP_REVIEW_REQUIRE_SECURITY` | true | X-4 |
| `review.require_performance` | `REVIEW_MCP_REVIEW_REQUIRE_PERFORMANCE` | true | X-12 (extends X-4) |
| `review.require_effort_estimate` | `REVIEW_MCP_REVIEW_REQUIRE_EFFORT_ESTIMATE` | true | X-4 |
| `review.extra_instructions` | `REVIEW_MCP_REVIEW_EXTRA_INSTRUCTIONS` | (empty) | Per-call override |
| `review.inline_findings` | `REVIEW_MCP_REVIEW_INLINE_FINDINGS` | true | X-11; per-call `inline_findings` argument |
| `review.persistent_overview` | `REVIEW_MCP_REVIEW_PERSISTENT_OVERVIEW` | true | X-12 |
| `review.max_discussion_tokens` | `REVIEW_MCP_REVIEW_MAX_DISCUSSION_TOKENS` | 1500 | X-13; 0 disables the discussion block; must not be negative |
| `review.max_chunks` | `REVIEW_MCP_REVIEW_MAX_CHUNKS` | 8 | X-19 (v1.1); 1 to 32; 1 reviews in one call |
| `review.max_total_findings` | `REVIEW_MCP_REVIEW_MAX_TOTAL_FINDINGS` | 10 | X-19 (v1.1); 1 to 50; at least `review.max_findings` when set; the run-time cap is the larger of the two |
| `ask.extra_instructions` | `REVIEW_MCP_ASK_EXTRA_INSTRUCTIONS` | (empty) | Per-call override |
| `context.repo.enabled` | `REVIEW_MCP_CONTEXT_REPO_ENABLED` | false | X-22 (v1.1); repository context, stdio only; `server_info` shows `enabled, git <version>` or `enabled, unavailable: <reason>` when set |
| `context.repo.cache_dir` | `REVIEW_MCP_CONTEXT_REPO_CACHE_DIR` | (empty) | X-22; absolute path; empty means `os.UserCacheDir()/review-mcp/repos`; created with mode 0700; a non-empty directory without review-mcp's `CACHEDIR.TAG` is refused |
| `context.repo.idle_days` | `REVIEW_MCP_CONTEXT_REPO_IDLE_DAYS` | 7 | X-22; 1 to 365 |
| `context.repo.max_cache_mb` | `REVIEW_MCP_CONTEXT_REPO_MAX_CACHE_MB` | 2048 | X-22; 1 to 1048576 |
| `context.repo.max_repo_mb` | `REVIEW_MCP_CONTEXT_REPO_MAX_REPO_MB` | 500 | X-22; 1 to 1048576, at most `max_cache_mb`; measured after the fetch |
| `context.repo.fetch_timeout_seconds` | `REVIEW_MCP_CONTEXT_REPO_FETCH_TIMEOUT_SECONDS` | 60 | X-22; 1 to 600; covers the lock wait and the fetch, including the auth retry |
| `context.repo.max_symbols` | `REVIEW_MCP_CONTEXT_REPO_MAX_SYMBOLS` | 20 | X-22; 1 to 50; symbols taken from the diff, ranked (removed or renamed, changed signature, other changed) |
| `context.repo.max_hits_per_symbol` | `REVIEW_MCP_CONTEXT_REPO_MAX_HITS_PER_SYMBOL` | 5 | X-22; 1 to 20; uses kept per symbol, distinct files and the definition's language group first |
| `context.repo.max_tokens` | `REVIEW_MCP_CONTEXT_REPO_MAX_TOKENS` | 2000 | X-22; 200 to 16000; token budget of the repository-context block, counted inside the prompt tokens; a part of a review in parts has its own; clipped by whole entries, dropped when the diff needs the room |
| `improve.max_suggestions` | `REVIEW_MCP_IMPROVE_MAX_SUGGESTIONS` | 8 | X-27 (v2); 1 to 30; the most suggestions after the parts are ranked and deduplicated |
| `improve.max_suggestions_per_part` | `REVIEW_MCP_IMPROVE_MAX_SUGGESTIONS_PER_PART` | 4 | X-27; 1 to 10; asked for per suggestion call, not enforced |
| `improve.min_score` | `REVIEW_MCP_IMPROVE_MIN_SCORE` | 7 | X-27; 0 to 10; suggestions scoring below it are dropped, unscored ones are kept |
| `log.level` | `REVIEW_MCP_LOG_LEVEL` | `info` | stderr only (X-8) |
| `serve.listen` | `REVIEW_MCP_SERVE_LISTEN` | `127.0.0.1:8787` | serve only (X-10); `host:port`; `--listen` overrides |
| `serve.tls_cert` | `REVIEW_MCP_SERVE_TLS_CERT` | — | serve only; PEM path, set together with `tls_key` |
| `serve.tls_key` | `REVIEW_MCP_SERVE_TLS_KEY` | — | serve only; PEM path |
| `serve.allow_insecure_http` | `REVIEW_MCP_SERVE_ALLOW_INSECURE_HTTP` | false | serve only; non-loopback bind without TLS; startup warning when true |
| `serve.llm_key_source` | `REVIEW_MCP_SERVE_LLM_KEY_SOURCE` | `header` | serve only; `header` \| `server` |
| `serve.allowed_origins` | `REVIEW_MCP_SERVE_ALLOWED_ORIGINS` | (empty) | serve only; exact origins |
| `serve.max_concurrent_calls` | `REVIEW_MCP_SERVE_MAX_CONCURRENT_CALLS` | 4 | serve only; 1–64 |
| — | `REVIEW_MCP_SERVE_ACCESS_TOKEN` | — | **Secret**, serve only; required when `llm_key_source = server`, enforced whenever set |

Validation rules: at least one provider enabled; every enabled provider has its
token; URLs parse as `http(s)`; numeric ranges sane; enum values exact. All
violations are reported together in one token-free startup error.

## 6. v1 tool surface

| Tool | Arguments | Result |
|---|---|---|
| `server_info` (P1 diagnostic) | — | Version, enabled providers, effective non-secret config (secrets shown as set/unset only) |
| `pr_review` | `pr_url` (required), `extra_instructions`, `output_language`, `max_findings`, `publish`, `inline_findings` (P7), `wait_seconds` (X-16) | Markdown (`client` profile) + `structuredContent` (DQ-6) |
| `pr_ask` | `pr_url` (required), `question` (required), `extra_instructions`, `output_language`, `publish`, `wait_seconds` (X-16) | Markdown answer |
| `pr_comments` (X-9) | `pr_url` (required), `include_resolved` (default false) | Markdown thread listing + `structuredContent` |
| `pr_comment_reply` (X-9) | `pr_url` (required), `comment_id` (required), `body` (required) | Posted comment id/URL + whether it landed in-thread or as a PR-level fallback |
| `pr_comment_create` (X-14) | `pr_url` (required), `body` (required), `file`, `line` (together) | Posted comment id/URL + `inline` true or false |
| `job_result` (X-16, **stdio only**) | `job_id` (required), `wait_seconds` | The finished `pr_review` or `pr_ask` result, or the running status |

## 7. Items deferred beyond v1 (with seams)

| Item | Seam that must exist in v1 |
|---|---|
| Fallback model chain | `FallbackEligible` sentinel; per-attempt request preparation |
| Dynamic context | `extend` has head-file content available |
| Review chunking | Pipeline already produces `remaining_files` |
| Repo-local config | Layered loader with explicit layer list |
| Basic auth (Bitbucket Server) | Auth as a provider-level strategy, not a hardcoded header |
| Labels | Typed `Capabilities` struct (inline and persistent comments shipped in P7, X-11, X-12) |
| Duplicate-skipped finding links to its earlier inline comment (v1.0.x) | `CommentItem.URL` filled for inline comments |
| Shared package for the P2e sanitizers duplicated in `internal/review` (v1.0.x) | None; a refactor |
| `/improve` (DQ-11; DQ-13, 14, 15, 18 were decided in P7) | Hunk model with multiple renderers (DQ-10) |
| Additional review fields | Field-descriptor table (X-4, DQ-6) |

## 8. Rejected options

Recorded so that they are not proposed again without new arguments.

| ID | Option | Decision |
|---|---|---|
| R-1 | Building or running pull request code on the user's machine to check whether it compiles or its tests pass | Rejected (owner decision, 2026-10-10) |

#### R-1 — Building or running pull request code (rejected)
- **Option:** clone the pull request, install its toolchain and dependencies, and build or test it, so that a review can say whether the change works.
- **Decision:** rejected, permanently. review-mcp never executes code from a pull request.
- **Rationale:** the server runs with the user's own credentials and file system. A pull request is untrusted input: its build scripts, dependency hooks and tests can run arbitrary commands, so building it locally turns a malicious pull request into code execution on the reviewer's machine. Sandboxing it safely needs hosted, isolated infrastructure, which a stdio tool with per-user tokens does not have and should not grow.
- **Boundary:** reading the code locally is allowed and wanted. X-22 already fetches the pull request into a credential-safe cache for read-only search, and v2.1 extends that to deeper local analysis. What is rejected is execution: no build, no tests, no package-manager or repository hooks, and no tool that loads configuration from the repository as code (for example a JavaScript linter config or a build script).
- **Replacement:** the build and check results the repository's own CI already produced are read from the provider's API (phase 2F, `docs/design/v2-design.md`).
