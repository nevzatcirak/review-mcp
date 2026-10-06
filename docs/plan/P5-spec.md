# P5 — `pr_ask`: Specification

| | |
|---|---|
| Phase | P5 (phase-plan.md) |
| Work packages | WP-PR-5a (prompts and ask pipeline) · WP-PR-5b (`pr_ask` MCP tool, `diag ask`, docs) |
| Binding inputs | `docs/design/v1-design-decisions.md`: X-1, X-3, X-5, X-6, X-8, DQ-16, DQ-25, DQ-26, §6. Porting map §C, the `/ask` part. |
| Builds on | P3 (`filter`, `tokens`, `diffpipe` in `ModePlain`) and P4 (`llm` client, render helpers, the publish path, error classification). Branch `p5-ask` from `main` after P4 is merged. |
| Protocol | Same as P1–P4: the P1 spec §0 preamble applies. Report one PR comment per package. `[architect review]` comments are the owner's instructions. |
| Model guidance | Sonnet throughout. This phase is small and reuses tested components. |

`pr_ask` is a thin tool over existing parts. It runs the same fetch → filter → budget → prepare pipeline as `pr_review`, but uses the plain diff view and free-text output (no schema, no YAML, no repair chain), as X-5 and porting map §C require.

## 1. WP-PR-5a — Prompts and pipeline (`internal/ask`)

### 1.1 Templates
- Add `internal/ask/prompts/system.tmpl` and `user.tmpl`, embedded with `go:embed`. Each has a header naming `pr_agent/settings/pr_questions_prompts.toml @ 8e5a929` and pointing to NOTICE, and both are added to the NOTICE file list.
- **Adapted from upstream:**
  - The role statement (answer questions about the code introduced by the PR).
  - The `extra_instructions` block, including its precedence sentence.
  - Title, branch and description, the description fenced with `======`.
  - `Main PR language: '<language>'`. This is the first language group from `diffpipe` (DQ-2 ranking); omit the line when the group is `Other`.
  - The plain diff, with upstream's one-line explanation of the `+`, `-` and space prefixes.
  - The question, fenced with `======`.
  - The closing line `Response to the PR Questions:`.
- **Removed:**
  - `skills_context` (not ported).
  - `conversation_history` (X-5: `pr_ask` is stateless).
- **Honesty deviation** (phase-plan P5 acceptance: "refuses/flags when the answer is not derivable"):
  - Upstream's system prompt says the model *must* answer. Replace that sentence with: "Answer only from the PR information and diff provided. If the answer cannot be determined from them, say so explicitly and state what information is missing. Do not guess."
  - Also add one sentence telling the model that the diff may list files omitted for size, and that it must not draw conclusions about their content.
  - Cite this deviation in a template comment.
- **Output language:** use the same mechanism as P4 §4.2. Reuse P4's helper; do not duplicate it.
- **Engine:** `text/template` with `missingkey=error` and an injected clock, as in P4.
- **Goldens:**
  - Rendered prompts for:
    - with and without extra instructions;
    - with and without a main language;
    - a non-English output language;
    - a multi-line question.
  - Have the oracle render upstream's template for comparison. The only expected differences are the removed blocks and the honesty sentences, and the report lists them.

### 1.2 Pipeline: `func Run(ctx, deps, args) (*Result, error)`
1. **Config.** If the config is invalid, return the standard error. Nothing is sent.
2. **Validate the question.**
   - The question must be non-empty after trimming.
   - It may be at most **8000 characters**, measured in runes. Longer questions are rejected with a fixed message; they are never silently truncated.
   - It goes through the same UTF-8 sanitization as the P2e comment bodies.
3. **Fetch.** Resolve the URL, then fetch the PR and its diff with `filter.Include`. Clip the description with `tokens.ClipDescription`.
4. **Budget.** Render the prompts with an empty diff to get `PromptTokens`. This includes the question, because a question is part of the scaffolding. Then build the `tokens.Budget` and check `RequireCapacity`.
5. **Prepare.** Call `diffpipe.Prepare` in **`ModePlain`**. `ErrDoesNotFit` becomes the classified "does not fit" error. An empty prepared diff means no LLM call: return the note "No reviewable changes after filtering." together with the coverage (the same rule as P4).
6. **Render and check fit.** Render the final prompts. Apply the same `RequestTokens` plus reserve guard as P4.
7. **Call the LLM** through the P4 client, with the same retry rules.
   - Empty content is an `llm_protocol` error.
   - A response cut off by `finish_reason == "length"` is kept and gets the note "The answer was cut off by the model's output limit."
   - There is no re-ask, because there is nothing to parse.
8. **Result.** Return the trimmed answer, the coverage (included, omitted, clipped, skipped and filtered, as in P4), the notes, and the metadata: model, prompt and diff tokens, fast path, truncated.
9. **Publish.** When `publish` is set, render the provider profile and call `PostComment`. As in P4, a publish failure is reported alongside the answer and never discards it.

## 2. WP-PR-5b — Tool, diag, docs

### 2.1 MCP tool `pr_ask` (§6 of the decisions doc)
- **Arguments:**
  - `pr_url` (required);
  - `question` (required);
  - `extra_instructions`;
  - `output_language`, validated with the locale regex;
  - `publish` (bool, default false).
- **Description:** "Answers a question about a pull request using the configured LLM, grounded in the PR's title, description and diff. Set publish=true to also post the question and answer as a PR comment. The PR content and the question are sent to the configured LLM endpoint."
- **Annotations:** not read-only (because of `publish`), not destructive, not idempotent, open-world.
- **Result.**
  - **Text content: the client profile.** This is portable markdown:
    - `## Question` with the question in a fenced block (adaptive fence);
    - `## Answer` with the model's answer as-is, since it is model-authored markdown;
    - the coverage section, always present (X-3);
    - the notes, when present.
  - **`structuredContent`:** `{ question, answer, coverage, notes, metadata, publish }`.
- **Errors:** the X-6 classified sentences, and the standard degraded-config error.

### 2.2 Provider profile (published comment)
- **Layout:** follows upstream: a heading for the question, the question, a heading for the answer, then the answer.
  - **Gitea (GFM):** upstream's emoji headings (`Ask ❓` / `Answer:`).
  - **Bitbucket Server:** plain headings.
  - **Coverage:** in both, as a short section at the end.
- **Quick-action sanitization:** port upstream's rule. No line of the published body may start with `/`, so prepend a space where one would; this applies after `\n` and after `\r` too. Apply the sanitization to both the question and the answer.
  - **[canary]** An answer containing `\n/close` must be published as `\n /close`. Prove it by removing the sanitizer.

### 2.3 `review-mcp diag ask <PR_URL> --question <TEXT> [--dry-run] [--show-prompt] [--publish]`
- `--dry-run` and `--show-prompt` behave as in `diag review`: `--dry-run` reports tokens, budget and coverage without calling the LLM, and `--show-prompt` prints the rendered prompts.
- Without `--dry-run`, the command prints the client markdown.
- Usage errors exit 2 and send nothing over the network. Usage errors include:
  - a missing or empty question;
  - a question longer than 8000 characters.

### 2.4 Canaries (in addition to the per-package ones)
- **[canary] End-to-end leak:** set up a fake provider and a fake LLM, put a marker in the question and another in the PR description, set all secrets, and run at debug level. Both markers must reach the fake LLM's request body. Neither may appear in stderr or in any error text. No secret may appear anywhere.
- **[canary]** A degraded config makes zero requests.
- **[canary]** An over-long question is rejected without any request to the provider or the LLM.

### 2.5 Docs
- Add `docs/ask.md`, covering:
  - what gets sent to the LLM;
  - how answers stay grounded, including the honesty behaviour;
  - what the coverage section means for answers about omitted files;
  - `publish` and the slash sanitization;
  - `diag ask --dry-run`.
- Update NOTICE: the list of adapted files gains the ask templates.

## 3. Architect review checklist
- The usual checklist.
- The prompt diff against upstream shows only the listed removals and the honesty sentences.
- `ModePlain` is used, never `ModeNumbered`.
- No YAML or repair code is imported into `internal/ask`.
- New modules allowed in P5: **none**.

## 4. Live acceptance (owner)
Use the earlier test PRs and your own LLM endpoint.

1. In a real MCP client, run `pr_ask` on each provider with three questions:
   - a factual question, for example "Which files change the request validation?";
   - a reasoning question, for example "Could this change break existing callers?";
   - an **unanswerable** question, for example "What is the deployment schedule for this change?".

   The first two answers must be grounded in the diff. The third must say the answer cannot be determined from the PR.
2. Ask a question about a file omitted by the budget. Set a small `REVIEW_MCP_LLM_CONTEXT_WINDOW` to force the omission. The answer must not invent the file's content, and the coverage section must list it.
3. Run `pr_ask` with `output_language` set to `tr-TR`. The answer must be in Turkish.
4. Run `pr_ask` with `publish=true` on each provider. Check how the comment renders. Confirm that an answer line starting with `/` is published with a leading space and does not trigger a quick action.

**Amended 2026-10-06:** these items run in the consolidated V1 acceptance (`docs/plan/v1-acceptance.md`, section F) against the release candidate. The P5 PR merges on green CI plus architect approval.

## 5. Planned commits (branch `p5-ask`)
1. `feat(ask): add adapted question prompts with grounding instructions`
2. `feat(ask): add ask pipeline reusing diff preparation and the LLM client`
3. `feat(server): add pr_ask tool with client and provider rendering`
4. `feat(cli): add diag ask command`
5. `docs: document pr_ask and update the adapted-material notice`
