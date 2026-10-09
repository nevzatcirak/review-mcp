# v2 phase 2E — `pr_improve`: Specification

| | |
|---|---|
| Release | `v2.0.0-rc.2` (prerelease, npm `next`) |
| Design input | `docs/design/v2-design.md` §4 (Y-8…Y-11). Binding; where they differ, this spec wins. |
| Builds on | PR #16 (2A + 2D, approved): contract suite, capabilities, `llmrun` parts/coverage/marker helpers, `provider.SanitizeBody`. |
| Work packages | WP-2f (engine), WP-2g (verification and anchoring), WP-2h (publishing), WP-2i (docs) |
| Branch / PR | `v2-improve` from `v2-describe` (rebased onto `main` once #16 merges); draft PR "v2: pr_improve". Not merged before #16. |
| Protocol | As for #16: read every new `[architect review]` comment before each package and acknowledge it; one report per package; canaries with mutation and failure text; every commit gated alone; Windows CI; never merge, tag or publish. |
| Model guidance | WP-2f and WP-2g: Opus. WP-2h, WP-2i: Sonnet. |

## 0. Decisions (added to the decisions doc in the package that implements them)

- **X-27 — `pr_improve`:** suggestions with a self-review score, verified against the head file before anchoring, rendered per capability, under the X-12/X-13 rules.

## 1. WP-2f — Engine (Opus)

1. **Prompts.** Port PR-Agent's `code_suggestions/pr_code_suggestions_prompts.toml` and `pr_code_suggestions_reflect_prompts.toml` at the revision pinned in the porting map (add the improve section to the map and the files to NOTICE; every deviation in the template header).
   - Suggestion schema (upstream): `relevant_file`, `language`, `existing_code`, `suggestion_content`, `improved_code`, `one_sentence_summary`, `label`. Upstream asks for no line numbers in this pass; keep it so.
   - Reflection schema (upstream): per suggestion `suggestion_number`, `suggestion_summary`, `relevant_file`, `relevant_lines_start`, `relevant_lines_end`, `suggestion_score` (0–10), `why`.
   - Diff format: the decoupled line-numbered mode the porting map describes for `/improve` (`__new hunk__` with new-file line numbers, `__old hunk__` without). If our `diffpipe` numbered mode differs from it, record the difference in the header and keep ours unless the golden comparison shows the model needs upstream's exact shape — a DESIGN-QUESTION, not a silent choice.
2. **Inputs:** title, description, branches, the diff (same budget rules, X-15, `diff.max_tokens`), and the discussion block (X-13) so already-raised points are not repeated. Repository context: used when enabled (X-22), same budget rules as `pr_review`.
3. **Parts:** X-19 exactly. Each part asks for at most `improve.max_suggestions_per_part` (default 4).
4. **Reflection (Y-9).** One reflection call per part, given that part's diff and that part's suggestions. Suggestions scoring below `improve.min_score` (default 7, range 0–10) are dropped and counted in a note ("N suggestions were dropped by the self-review score (below 7)."). If a part's reflection fails: its suggestions are kept **unscored**, marked "unscored", and the fixed note "Part I's suggestions were not scored (the self-review call failed)." A suggestion the reflection does not mention is unscored as well, never silently dropped.
5. **Merge (amended in the WP-2f review):** rank globally — scored suggestions by score descending (ties: part order, then model order), then unscored ones in part order; fingerprint dedup (X-13 fingerprint over file, summary and `existing_code`); then cap `improve.max_suggestions` (default 8, ≤ 30) with the X-19-style note for the rest. The cap never keeps a lower score from an earlier part while cutting a higher score from a later one.
6. **Validation:** a suggestion for a file not in its part's reviewed set is dropped (counted in a note); `existing_code` equal to `improved_code` after whitespace normalisation is dropped as "no change"; labels capped at 40 characters, one line.
7. **Structured result:** `suggestions[]` (`file`, `language`, `label`, `summary`, `content`, `existing_code`, `improved_code`, `start_line`, `end_line`, `score` or null, `why`, `verified` (WP-2g), `anchor` (WP-2h)), `coverage` (standard plus `model_calls`, `failed_parts`), `notes[]`, `metadata` (`llm_calls` counts every completion including reflection). Output schema with the stdio running-status branch and `job_result`.
8. **Tool:** `pr_improve` in stdio and serve; arguments `pr_url`, `publish` (default false; refused with a fixed sentence until WP-2h, as in WP-2c), `output_language`. Annotations as `pr_review` once publishing exists.
9. **Config:** `improve.max_suggestions` (8), `improve.max_suggestions_per_part` (4), `improve.min_score` (7); validation sentences in the existing style; shown in `server_info`.
10. **Tests:** goldens (fake LLM) for one call and three parts; reflection drop, reflection failure, a suggestion the reflection skipped, dedup across parts, the cap, validation drops.
    - **[canary]** keep suggestions below `min_score`: the reflection test fails.
    - **[canary]** drop unscored suggestions when reflection fails: the honesty test fails.

Commit: `feat(improve): add pr_improve with parts and a self-review score`

## 2. WP-2g — Verification and line ranges (Opus)

1. **Line range** comes from the reflection's `relevant_lines_start/end` (new-file numbers). Without a range (unscored or missing), the suggestion is located by searching `existing_code` in the head file.
2. **Verification (Y-10):** `existing_code` must match the head file content at `start_line…end_line` after the P4 snippet normalisation (trailing whitespace, CRLF, common indentation). If the given range does not match, search the head file for a unique match of `existing_code`; a unique match corrects the range (note "N suggestion line ranges were corrected"); none or several → `verified=false`.
3. A range must lie inside one file, `start ≤ end`, within the file length; otherwise `verified=false`.
4. `verified=false` suggestions stay in the result and the overview, marked "not anchored: the quoted code was not found at the given lines", and are never posted inline.
5. **Tests:** exact match; off-by-N range corrected; ambiguous match (two occurrences) unverified; CRLF and indentation differences; a range outside the file.
   - **[canary]** accept a range without comparing the code: the mismatch test fails.
   - **[canary]** pick the first of two matches: the ambiguity test fails.

Commit: `feat(improve): verify suggestions against the head file before anchoring`

## 3. WP-2h — Publishing (Sonnet)

1. **Overview:** one comment with marker `[//]: # (review-mcp:improve:v1)`, edited in place (X-12 helpers in `llmrun`); a table of suggestions (label, file with line link, summary, score), the coverage section, notes; "Reviewed in N model calls." as for parts.
2. **Inline (Y-11), only verified suggestions on head-side lines inside one hunk:**
   - `SuggestionBlocks` capability: a native suggestion block (GitHub/GitLab later, no provider sets it today) — the renderer exists and is tested with a fake capability.
   - Otherwise (Gitea, Bitbucket Server): summary, `suggestion_content`, and a fenced `diff` block of `existing_code` → `improved_code` (adaptive fence).
   - Fingerprint marker per suggestion (X-13 pattern, own prefix `review-mcp:suggestion:`); an already-posted or human-raised suggestion is not posted again (`skipped_duplicate`).
   - `InlineResult` gains `Reason` (`posted`, `unanchorable`, `failed`), decided in the WP-2a review; `pr_review` maps to it without changing its output.
3. **Sanitising:** title/labels escaped; text through the published-text rules of WP-2d item 9; `provider.SanitizeBody` everywhere.
4. **Annotations:** `readOnlyHint=false`, `destructiveHint=false`.
5. **Tests:** both providers through the contract fixtures: overview post and in-place edit, inline posting of verified suggestions only, duplicate skip on a second run, unanchorable reason, suggestion-block renderer with a fake capability.
   - **[canary]** post an unverified suggestion inline: the test fails.
   - **[canary]** drop the fingerprint check: the second-run duplicate test fails.

Commit: `feat(improve): publish an overview and inline suggestions`

## 4. WP-2i — Docs (Sonnet)
`docs/improve.md` (what is sent, reflection and scores, verification, both renderings, config, notes and troubleshooting sentences), README row, setup scopes, CHANGELOG `2.0.0-rc.2` (Unreleased), X-27, NOTICE and porting map.

Commit: `docs: document pr_improve`

## 5. In-use acceptance (`v2.0.0-rc.2`)
- **P1** `pr_improve` without publish on a small and a large PR: suggestions are concrete, scored, and refer to the right lines; unverified ones are marked.
- **P2** Publish twice: one overview edited in place; inline suggestions on the right lines; no duplicates on the second run.
- **P3** A point a reviewer already raised is not suggested again.
- **P4** Record roughly how many suggestions the score threshold drops, to judge `improve.min_score`.
