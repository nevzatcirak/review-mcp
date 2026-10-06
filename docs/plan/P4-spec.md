# P4 — `pr_review`: Specification

| | |
|---|---|
| Phase | P4 (phase-plan.md): the first end-to-end tool |
| Work packages | WP-PR-4a (LLM client) · WP-PR-4b (YAML loading and repair chain) · WP-PR-4c (field descriptors, prompts, review pipeline) · WP-PR-4d (renderers) · WP-PR-4e (`pr_review` MCP tool, `diag review`, docs) |
| Binding inputs | `docs/design/v1-design-decisions.md`: DQ-6, DQ-7, DQ-8, DQ-9, DQ-12, DQ-16, DQ-25, DQ-26, X-1, X-3, X-4, X-6, X-7, X-8, §5, §6. Porting map §C (prompts/schema) and §D (parsing/repair). |
| Builds on | P2 (providers, resolver, `PostComment`, `FileLineURL`) and P3 (`filter`, `tokens`, `patch`, `diffpipe`). Branch `p4-review` from `main` after P3 is merged. |
| Protocol | Same as P1–P3 (P1 spec §0 preamble). One PR comment per package. `[architect review]` comments are the owner's instructions. |
| Model guidance | 4b and 4c are porting-sensitive (repair-chain fidelity, prompt wording), so start them on the stronger model. 4a, 4d and 4e default to Sonnet. |

## 0. Entry criteria (carried over from earlier phases; each must be closed in this phase)

1. **SDK logging audit (X-8):** with `log.level=debug` and tool arguments that carry PR data, the MCP Go SDK's own log lines must contain neither tool arguments nor results. If they do, wrap the SDK logger with a filter that drops those attributes. **[canary]** in WP-PR-4e.
2. **`FileLineURL` formats** (amended 2026-10-06, consolidated V1 acceptance): render finding links. Their live verification moved to `docs/plan/v1-acceptance.md` (item E5), which runs against the release candidate before any stable release. A wrong format found there is a release blocker and is fixed in the next release candidate.
3. **Real prompt-token figures:** measure the rendered prompt scaffolding with an empty diff for every field-toggle combination. Report the numbers, and replace the `diag diff --prompt-tokens` default (1500) with the measured maximum.

## 1. Upstream parity rules

The P3 §0 rules apply unchanged:
- upstream at `8e5a929` is the oracle;
- no code copying;
- contract literals live in one file;
- use the Python parity oracle where feasible, otherwise hand-derived goldens marked as such.

In P4 the oracle targets are `load_yaml` / `try_fix_yaml` (WP-PR-4b) and the prompt templates (WP-PR-4c).

---

## 2. WP-PR-4a — LLM client (`internal/llm`)

- **Endpoint:** `POST {llm.base_url}/chat/completions`, OpenAI-compatible, non-streaming.
- **Request:**
  - `Authorization: Bearer <llm api key>`; `Reveal()` only inside the request builder.
  - Body: `{"model", "messages": [{"role":"system"},{"role":"user"}]}`, plus **only explicitly configured** sampling parameters (DQ-26): `temperature`, `seed`, `reasoning_effort`, and `max_tokens` (from `llm.max_output_tokens`).
  - `max_tokens` is used rather than `max_completion_tokens` because it is the most widely accepted field across OpenAI-compatible servers. An endpoint that rejects it surfaces as a classified error whose hint names `llm.max_output_tokens`.
- **Response:**
  - Read `choices[0].message.content` as a string. Also accept an array of content parts, concatenating the `text` parts.
  - Ignore reasoning fields.
  - Report `finish_reason == "length"` to the caller as `Truncated = true`.
- **Guards** (the same posture as P2 `httpx`; reuse its helpers where it makes sense, but keep the LLM error classes separate):
  - per-request timeout `llm.timeout_seconds`;
  - no redirects outside the configured base;
  - response cap of 8 MiB;
  - debug logs carry only method, redacted URL, status, duration and token usage numbers;
  - **never** log prompts, responses or headers.
- **Error classes (X-6), each a fixed sentence plus a config hint:**

  | Class | When | Hint |
  |---|---|---|
  | `llm_auth` | 401, 403 | `REVIEW_MCP_LLM_API_KEY` |
  | `llm_not_found` | 404 | `llm.base_url` / `llm.model` |
  | `llm_rate_limited` | 429 | — |
  | `llm_context_too_long` | see below | `llm.context_window` |
  | `llm_bad_request` | other 400, and 422 | the sampling keys, if any are set |
  | `llm_upstream` | 5xx | — |
  | `llm_timeout` | timeout | — |
  | `llm_transport` | other transport failures | — |
  | `llm_protocol` | unexpected response shape, or empty content | — |

  - `llm_context_too_long` applies to a 400 or 413 whose body mentions context length. Detect it with a case-insensitive match on `context` together with `length`, `window` or `maximum` **in memory only**: the body is never returned or logged.
- **Retries (DQ-9 step 1):**
  - Up to `llm.max_retries` retries for `llm_rate_limited`, `llm_upstream`, `llm_timeout` and `llm_transport`.
  - Backoff: honour `Retry-After` up to 30 s; otherwise 1 s, then 2 s, then 4 s.
  - No retry for the other classes.
  - Context cancellation stops retrying.
- **Tests:** an `httptest` fake that records request bodies. Cover:
  - **[canary]** unset sampling parameters are absent from the JSON;
  - each error class;
  - the retry counts and the `Retry-After` cap (with a fake clock);
  - content given as a parts array;
  - `finish_reason` `length`;
  - the response cap;
  - **[canary]** the leak test: the key and a prompt marker never appear in logs or errors.

## 3. WP-PR-4b — YAML loading and repair (`internal/yamlrepair`)

- **Library:** `gopkg.in/yaml.v3`, decoding into `map[string]any`.
- **`func Load(raw string, keys Keys) (map[string]any, Trace)`.** `Keys` carries the descriptor-derived key list, `firstKey` (`review`) and `lastKey`.
- **Preprocessing,** in upstream order (porting map §D steps 1–6):
  1. Trim the surrounding newlines.
  2. Strip a leading fence labelled `yaml`/`yml` only when the label is the complete info string. If there is none, strip a bare leading `yaml`.
  3. Drop a sign-off after the wrapper fence, with upstream's "looks like more answer" guard.
  4. Strip a trailing fence.
  5. Sanitize C0 control characters, except TAB, LF and CR, plus DEL. C1 is preserved.
  6. Parse. A text that preprocessing emptied goes to the failure path.
- **Repair chain (DQ-8):**
  - An ordered, table-driven list of upstream tactics **1, 2, 4, 5, 6, 7, 8, 9, 11, 12**, with the semantics from porting map §D. Each tactic transforms a fresh copy and is followed by a parse; the first non-empty map wins.
  - Tactics 3 and 10 are not ported.
  - `Trace` records the winning tactic's **name** only. The caller logs it at debug level.
- **Dialect notes:**
  - The No-detector accepts `false`, `no` and `none` (case-insensitive) and the string `No`.
  - Duplicate keys: last one wins. Document this.
- **Goldens:**
  - Use the parity oracle: upstream's `load_yaml` / `try_fix_yaml` on our fixtures, compared as **parsed structures** (JSON-normalized), not as text.
  - Seed the fixture set from upstream's own unit-test inputs for these functions; they are MIT data, attributed in NOTICE.
- **[canary] per kept tactic:** every tactic has at least one fixture that **only** that tactic repairs. Prove it by disabling the tactic and showing that fixture fails. That gives 10 canary proofs, listed in the report.

## 4. WP-PR-4c — Descriptors, prompts, review pipeline (`internal/review`)

### 4.1 Field descriptors (X-4, DQ-6)
- One table drives four things: the schema text in the prompt, the example YAML in the prompt, validation and conversion, and the MCP output schema.
- Fields, in upstream order:

  | Field | Toggle | Shape |
  |---|---|---|
  | `estimated_effort_to_review` | `review.require_effort_estimate` | int 1–5; accept `3` or `"3"`, or a leading integer in a string |
  | `relevant_tests` | `review.require_tests` | `Yes`/`No`, normalized to a bool |
  | `key_issues_to_review` | always | list of `{relevant_file, issue_header, issue_content, start_line, end_line}`, at most `max_findings` items |
  | `security_concerns` | `review.require_security` | `No` (detected with the No-detector), or a text |

- `lastKey` is the last **enabled** field.
- **Validation:**
  - **Warn-only** (logged at debug level by field name, never by value), except for the gate: a non-empty `review` mapping is required. If it is missing, return the `FallbackEligible` sentinel, which triggers the DQ-9 re-ask.
  - Items beyond `max_findings` are dropped with a note.
  - Line numbers are coerced to int. An unusable finding (no file, or no content) is dropped with a note.

### 4.2 Templates
- `internal/review/prompts/system.tmpl` and `user.tmpl`, embedded with `go:embed`. Each has a header comment naming the upstream file `pr_agent/settings/pr_reviewer_prompts.toml @ 8e5a929` and pointing to NOTICE.
- **Adapted from upstream with these changes:**
  - Only the X-4 fields are included; tickets, todo, split, score, risk, merge recommendation and time cost are removed.
  - The effort key is renamed.
  - The schema section and the example come from the descriptor table.
  - `{{ diff_hunk_format }}` uses the numbered variant of upstream's `prompt_fragments` text.
- **Keep:**
  - the role and the "what to flag" and "constructing comments" rules;
  - the `extra_instructions` block;
  - the `--PR Info--` block (date, title, branch, description fenced with `======`);
  - `The PR code diff:` fenced with `======`;
  - the final line `Response (should be a valid YAML, and nothing else):` followed by an open ```` ```yaml ```` fence (DQ-7).
- **Output language (upstream approach):** when the effective `output_language` (the per-call argument, else config) is not `en-US`, append upstream's fixed English locale sentence, including the clause "keep schema control values … in their original English form", to `extra_instructions` with upstream's separator. Never translate keys.
- **Engine:** `text/template` with `Option("missingkey=error")`.
  - **[canary]** A template referencing an unset variable fails to render.
- **The date is injected through a clock interface,** so that goldens are stable.
- **Goldens:** rendered system and user prompts for these toggle combinations:
  - all fields on;
  - only key issues;
  - non-English output language;
  - with and without extra instructions.

  Have the oracle render upstream's templates with matching variables, and diff our output against it. The expected differences are listed in the report: the removed fields and the renamed key.

### 4.3 Pipeline: `func Run(ctx, deps, args) (*Result, error)`
1. **Config:** if the config is invalid, return an error and send nothing.
2. **Resolve:** resolve the PR URL (X-2), then fetch the PR and its diff with `filter.Include`.
3. **Description:** clip it with `tokens.ClipDescription`.
4. **Measure:** render the prompts with an empty diff to get `PromptTokens`, then build a `tokens.Budget` and run `RequireCapacity`.
5. **Prepare:** call `diffpipe.Prepare` in `ModeNumbered`. `ErrDoesNotFit` becomes a classified "does not fit" error. If `Prepared.Text` is empty (every file was filtered, skipped or empty), do **not** call the LLM. Return a review with no findings, the note "No reviewable changes after filtering." and the full coverage section (P3 review, item 4 of the 3d DESIGN-QUESTIONs).
6. **Render:** render the final prompts.
   - Check `tokens.RequestTokens` + `HardReserve` ≤ `ContextWindow`. If it does not fit, trim through the verified prefix — the guard against estimator drift.
7. **Call the LLM.** If the response is truncated by length, note it.
8. **Parse with the repair chain.** On a parse failure, re-ask **once** (DQ-9 step 2):
   - Use the same prompts, with this sentence inserted on its own line **before** the `Response (should be a valid YAML…` line: `Note: your previous answer could not be parsed as YAML. Answer again with valid YAML only, following the schema exactly.`
   - Temperature is unchanged.
   - A second failure becomes the classified error `review_unparseable`.
9. **Validate and convert** with the descriptors.
10. **Code snippets (DQ-12):** for each finding, take lines `start_line`–`end_line` from the head content when it was fetched, otherwise from a patch walk that resolves **every** line of the range. If the range is unresolvable, keep the finding, omit the snippet, and add the note "lines could not be verified against the diff". Cap the snippet at 30 lines, with a note when it is cut.
11. **Links:** `FileLineURL` for each finding (subject to entry criterion 2).
12. **Result:** a `Result` holding the review struct, the coverage (from `Prepared` plus the provider skips plus the filter reasons), a `Notes []string` list (truncation, dropped findings, clipped files, re-ask used) and run metadata (model, prompt and diff tokens, fast path, repair tactic, re-asked).
13. **Publish:** if `publish` is set, render with the provider profile and call `PostComment`. The result records the comment id and URL, or the publish error **in addition to** the review. A publish failure never discards the review.

## 5. WP-PR-4d — Renderers (`internal/review/render`)

- **Client profile (DQ-16; returned to the MCP client):** portable markdown, with no raw HTML.
  - Header `## PR Review` plus a one-line PR reference.
  - The enabled fields in descriptor order, with upstream's presentation adapted:
    - effort `N/5` with the bar characters;
    - tests: "PR contains tests" / "No relevant tests";
    - security: "No security concerns identified" or the text.
  - **Key issues** last, as a numbered list. Each item:
    - header and file with lines (a link when one is available);
    - the content;
    - the snippet in a fenced block with an adaptive fence and a language tag from `filter.Language`.
  - **Coverage section, always present (X-3):** included and omitted counts, the omitted files grouped by reason, at most 50 shown, then "and N more".
  - **Notes section** when `Notes` is non-empty.
  - All dynamic strings that are not inside a fenced block are escaped for markdown control characters.
- **Provider profile (published comments):**
  - **Gitea (GFM):** upstream-style output with emojis, a `<table>` layout and a `<details>` collapsible per key issue.
  - **Bitbucket Server (no GFM):** headings and pipe tables, no HTML.
  - Coverage and notes are included in both.
- **[canary]**
  - The client profile contains no HTML tags. Prove it by injecting a `<details>`.
  - A finding body containing ```` ``` ```` cannot break the fence.
- **Goldens:** each profile × {all fields; no findings; a finding without a snippet; a non-English sample}.

## 6. WP-PR-4e — `pr_review` tool, `diag review`, docs

### 6.1 MCP tool `pr_review` (DQ-25, §6 of the decisions doc)
- **Arguments:**
  - `pr_url` (required);
  - `extra_instructions`, `output_language` (validated with the config's locale regex);
  - `max_findings` (1–20);
  - `publish` (bool, default false).
- **Description:** "Reviews a pull request with the configured LLM and returns a structured review (key issues, effort, tests, security) with code excerpts. Set publish=true to also post it as a PR comment. The PR's title, description and diff are sent to the configured LLM endpoint."
- **Annotations:** not read-only (because of `publish`), not destructive, not idempotent, open-world.
- **Result:** the client-profile markdown as text content, plus `structuredContent` containing the review, the coverage, the notes, the metadata and the publish result (DQ-6). The output schema is generated from the descriptors.
- **Errors:** the X-6 classified sentences only. A degraded config returns the standard "call server_info" error.
- **Progress:** if the SDK supports progress notifications cheaply, send stage notifications (`fetching`, `preparing diff`, `calling model`, `rendering`). Otherwise skip them and say so in the report.

### 6.2 `review-mcp diag review <PR_URL> [--dry-run] [--show-prompt] [--publish]`
- `--dry-run`: run everything up to the LLM call, then print a JSON report of the prompt tokens, the budget, the coverage and the request token estimate. No LLM call is made.
- `--show-prompt`: print the rendered system and user prompts to stdout after the JSON, under separator lines. They are never logged.
- Without `--dry-run`: run the full review and print the client markdown. With `--publish`, also post it.

### 6.3 Leak and safety canaries (in addition to the per-package ones)
- **[canary] End-to-end:**
  - A fake provider and a fake LLM.
  - A PR description containing a marker.
  - All secrets set.
  - Debug logging on.
  - The marker must reach the fake LLM's request body, and must appear neither in stderr nor in any error text.
  - No secret may appear anywhere.
  - This closes entry criterion 1.
- **[canary]** A degraded config makes **zero** requests to both fakes.

### 6.4 Docs
- **`docs/review.md`:**
  - what `pr_review` sends to the LLM (title, description, diff; never secrets);
  - how to pick `llm.context_window`;
  - the recommended `temperature = 0.2` (DQ-26);
  - reading coverage and notes;
  - `publish`;
  - `diag review --dry-run` for budget tuning.
- **NOTICE:** add the prompt templates and the YAML repair fixtures to the list of adapted files (X-7 acceptance item).

## 7. Architect review checklist (per package)
- The usual checklist.
- **4b:** 10 tactic canaries are listed, and the goldens are oracle-generated or marked "hand-derived".
- **4c:** the prompt diff against upstream contains only the expected removals and renames. `missingkey=error` is set. The re-ask sentence is placed before the `Response` line.
- **4e:** the entry criteria (§0) are closed, with evidence.
- New modules allowed in P4: `gopkg.in/yaml.v3`. Nothing else without a DESIGN-QUESTION.

## 8. Live acceptance (owner)
Use the P2/P3 test PRs and your own OpenAI-compatible endpoint and key. Use a personal account only; nothing about the endpoint goes into the repo or the PR.

1. Register the build in a real MCP client and run `pr_review` on the Gitea test PR, then on the Bitbucket test PR.
   - The review is relevant: the findings point at the right files and lines, and the snippets match the code.
   - The coverage section matches `diag diff`.
2. Run `diag review --dry-run` and record the prompt and diff tokens against `llm.context_window`.
3. Run `pr_review` with `output_language` set to `tr-TR`. The text is in Turkish, and the `security_concerns: No` detection still works.
4. Run `pr_review` with `publish=true` on each provider. The comment renders correctly: GFM with collapsibles on Gitea, no raw HTML on Bitbucket.
5. Open one finding link per provider and confirm it lands on the right file and line (entry criterion 2).
6. Record which repair tactic, if any, fired, using debug logs for at least 3 reviews with the model you actually use. It is fine if none fired.

**Amended 2026-10-06:** these items run in the consolidated V1 acceptance (`docs/plan/v1-acceptance.md`, section E) against the release candidate. The P4 PR merges on green CI plus architect approval.

## 9. Planned commits (branch `p4-review`)
1. `feat(llm): add OpenAI-compatible chat client with classified errors and retries`
2. `feat(yamlrepair): add YAML loading and ordered repair chain`
3. `feat(review): add field descriptors and adapted review prompt templates`
4. `feat(review): add review pipeline with re-ask, snippets and coverage`
5. `feat(review): add client and provider markdown renderers`
6. `feat(server): add pr_review tool`
7. `feat(cli): add diag review command`
8. `docs: document pr_review, LLM settings and adapted-material notice`
