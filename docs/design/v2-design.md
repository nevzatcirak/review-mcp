# review-mcp "v2" — Design note (GitHub, GitLab, `pr_describe`, `pr_improve`)

| | |
|---|---|
| Status | **Approved in direction** (owner, 2026-10-08: tools first, one 2.0 release, `pr_describe` may edit descriptions in a marked region). Becomes binding through per-phase specs (`docs/plan/v2-*-spec.md`), as v1.1 did. |
| Scope | The v2 backlog of `docs/plan/phase-plan.md`: two new providers (GitHub, GitLab) and two new tools (`pr_describe`, `pr_improve`), plus the provider contract they need. |
| Builds on | v1.1 (`main` after `v1.1.0-rc.1`): chunked review (X-19), `pr_info` (X-23), repository context (X-22). |
| Non-goals | GraphQL clients, GitHub Apps / OAuth flows (tokens only, as today), webhooks or a bot mode, Bitbucket Cloud, Azure DevOps, auto-merge or any write beyond comments, descriptions and suggestions. |

## 0. Release model (owner decision, 2026-10-08)

- **Y-0 — One 2.0 release.** The four additions ship together as `v2.0.0`. Each phase still gets its own release candidate (`v2.0.0-rc.N`, npm `next`) so that it is accepted in use as soon as it lands; `latest` moves only at `v2.0.0`. Nothing planned here breaks the tool surface or the configuration; if a phase must break something, its spec says so and the CHANGELOG lists it under "Breaking".
- **Branching.** v2 phase PRs stay unmerged until `v1.1.0` is tagged (the v1.1 merge rule, repeated). After that they merge to `main` one phase at a time; v1.1.x fixes, if any, are cut from the `v1.1.0` tag on a `release/1.1` branch.

## 1. Order (owner decision, 2026-10-08: tools first)

| Phase | Content | Release candidate | Why this position |
|---|---|---|---|
| 2A | Provider contract test suite; resolver and `PRRef` generalisation | with 2D | Every later phase is checked against it. Small. |
| 2D | `pr_describe` | v2.0.0-rc.1 | Useful at once on the providers already in use; no line anchoring. |
| 2E | `pr_improve` | v2.0.0-rc.2 | Reuses P7 anchors and X-19 parts; the highest review value after `pr_review`. |
| 2B | GitHub provider | v2.0.0-rc.3 | Dogfooding: this repository's own PRs are a personal test instance. |
| 2C | GitLab provider | v2.0.0-rc.4 | Largest API surface (nested groups, positions, quick actions). |


## 2. Phase 2A — Provider contract

- **Y-1 — One contract test suite for every provider.** `internal/provider/contract` holds table-driven tests written against the `Provider` interface and a per-provider fake server adapter (`Fixture` interface: "serve this PR with these files, comments, reviewers"). Gitea and Bitbucket Server are moved onto it first; their existing tests stay. Covered behaviour: PR metadata, base strategy, diff file list and change types (rename with edits, deletion, binary, size and file limits), comment threads and ordering, reply semantics, ownership check on edit, inline posting results (posted / unanchorable / failed), review status folding, URL building (`FileLineURL`), error classes and that no error text carries a token or raw server text.
  - **[canary]** break one Gitea behaviour (for example drop rename detection): the contract test fails for Gitea only.
- **Y-2 — `PRRef.Namespace` may contain `/`.** It already holds the Gitea owner or Bitbucket project; GitLab needs `group/subgroup/...`. Each factory's `ParsePRPath` decides how many segments are namespace. Logging and cache keys escape it (the X-22 cache layout already escapes path segments).
- **Y-3 — Capabilities grow by need, never by provider name.** New flags: `SuggestionBlocks` (GitHub, GitLab), `QuickActions` (GitLab: every published body is slash-sanitised, P5 rule extended from `pr_ask` to all bodies), `ThreadResolution` (whether resolved state is readable), `DescriptionEdit`. Code branches on capabilities only.

## 3. Phase 2D — `pr_describe`

- **Y-4 — Output.** Ported from PR-Agent's describe prompt (MIT, attribution in NOTICE): `title`, `type` (one or more of bug fix, tests, enhancement, documentation, other), `description` (short summary bullets), and `pr_files` walkthrough (per file: change summary and a one-line label). Labels on the provider are **not** set in this phase.
- **Y-5 — Publishing.** `publish` default false. `publish_mode`:
  - `comment` (default): one comment, edited in place on later runs (overview pattern, its own marker `[//]: # (review-mcp:describe:v1)` plus the author check).
  - `description`: the PR description is updated **without losing the author's text**: review-mcp owns only a fenced region between `[//]: # (review-mcp:describe:start)` and `[//]: # (review-mcp:describe:end)`, appended below the author's text on the first run and replaced on later runs. Text outside the region is never changed. The title is changed only with `update_title=true`. Needs `DescriptionEdit`; refused with a fixed sentence otherwise.
  - Concurrency: the description is re-read immediately before the write; if it changed since it was read, the write is retried once on the new text, then refused with a fixed sentence (never overwrites someone's edit).
- **Y-6 — Large PRs.** Parts as X-19 for the per-file walkthrough; the title, type and summary come from **one extra reduce call** over the parts' structured answers (no diff text). If the reduce call fails, the summary is the concatenation of the parts' summaries with a fixed note. The X-18 banner applies to files left out.
- **Y-7 — Honesty.** A file not seen by the model never gets a walkthrough line; it is listed as "not described (see Coverage)".

## 4. Phase 2E — `pr_improve`

- **Y-8 — Suggestions.** Ported from PR-Agent's code-suggestions prompt (MIT): per suggestion `relevant_file`, `language`, `existing_code`, `improved_code`, `one_sentence_summary`, `label`, line range. Parts as X-19; merged with fingerprint dedup and a total cap `improve.max_suggestions` (default 8).
- **Y-9 — Self-review step.** One extra call scores each suggestion 0–10 against the diff and drops those below `improve.min_score` (default 7), as PR-Agent's self-reflection does. Dropped suggestions are counted in a note, never silently lost. If the scoring call fails, suggestions are kept unscored with a fixed note.
- **Y-10 — Verification before anchoring.** `existing_code` must match the head file at the given lines after whitespace normalisation (the snippet verifier of P4). An unverified suggestion is kept in the overview only, marked "not anchored: the quoted code was not found at the given lines".
- **Y-11 — Rendering per capability.**
  - `SuggestionBlocks` (GitHub, GitLab): an inline comment with a native suggestion block, only when the range is on the head side, contiguous, inside one hunk and verified.
  - Otherwise (Gitea, Bitbucket Server): an inline comment with a before/after diff block.
  - Overview: a table of suggestions with label, file, line link and summary; same overview edit-in-place rule as X-12 (own marker `review-mcp:improve:v1`).
  - Discussion awareness (X-13) applies: a suggestion already raised by a human or already posted is not repeated.

## 5. Phase 2B — GitHub provider

- **Y-12 — API surface.** REST v3 only. `github.com` (`https://api.github.com`) and GitHub Enterprise Server (`{base}/api/v3`); the web base and API base are both configured or derived (`github.base_url`, optional `github.api_url`). Token: classic or fine-grained PAT, `Authorization: Bearer`. Rate limits: `403/429` with `X-RateLimit-Remaining: 0` or `Retry-After` map to a fixed class `rate_limited` with the reset time; one bounded wait only when it is under the request deadline.
- **Y-13 — Diff.** `GET /pulls/{n}/files` (paged; GitHub caps at 3000 files: the rest are reported as provider-skipped `file_limit`). A file whose `patch` is absent (too large) is `size_limit`. Contents through `GET /contents/{path}?ref=` with the raw media type. Base: `GET /compare/{base}...{head}` → `merge_base_commit` (`github:merge_base`).
- **Y-14 — Comments.** General: issue comments. Inline: review comments with `line`/`side`/`start_line`; one review per run for inline findings (X-11 pattern) via `POST /pulls/{n}/reviews` with event `COMMENT`. Replies: `POST /pulls/{n}/comments/{id}/replies` for review comments; a quoting issue comment for general ones (as Gitea). **Thread resolution is not readable through REST:** `ThreadResolution=false`, threads are all shown and the note says resolved state is unavailable. GraphQL is not used.
- **Y-15 — `pr_info`.** Requested reviewers plus reviews (latest decisive per user; `DISMISSED` does not count; `stale` when `commit_id` ≠ head). Required approvals: first `GET /rules/branches/{branch}` (rulesets, readable with read access: `pull_request.required_approving_review_count`), then classic branch protection (needs admin: else `null` plus note). `mergeable_state` maps to fixed blockers (`dirty` → merge conflict, `blocked`, `behind`, `unstable` → required checks failing; unknown → other).
- **Y-16 — Repository context.** Clone `{web}/{owner}/{repo}.git`, ref `refs/pull/{n}/head`, Basic `x-access-token:<token>` through `http.extraHeader` (X-22 rules unchanged).
- **Acceptance:** this repository's own PRs on github.com.

## 6. Phase 2C — GitLab provider

- **Y-17 — Addressing.** MR URL `{base}/{group}/{subgroup…}/{project}/-/merge_requests/{iid}`, self-managed with a context path supported. Project id is the URL-encoded full path. API v4. Token header `PRIVATE-TOKEN` (personal, project or group tokens).
- **Y-18 — Diff.** `GET /merge_requests/{iid}/diffs` (paged; GitLab 15.7+) with fallback to `/changes` for older servers (recorded as the strategy). Base from `diff_refs` (`base_sha`, `start_sha`, `head_sha`); the diff is against `base_sha` (`gitlab:diff_refs`). Contents via `/repository/files/{path}/raw?ref=`. Collapsed or too-large diffs map to `size_limit`.
- **Y-19 — Comments.** General: notes. Inline: `POST /discussions` with a `position` object (`position_type=text`, the three SHAs, `old_path`/`new_path`, and `new_line` for added lines, `old_line`+`new_line` for context lines). Replies: `POST /discussions/{id}/notes` (in thread). Resolved state readable (`ThreadResolution=true`). Every published body is slash-sanitised (`QuickActions`).
- **Y-20 — `pr_info`.** `reviewers[]`, `GET /approvals` (`approved_by`, `approvals_required`, `approvals_left` where the tier provides them, else `null` plus note), `detailed_merge_status` mapped to fixed blockers (its documented enum; unknown → other merge check), `draft`.
- **Y-21 — Repository context.** Clone `{base}/{full_path}.git`, ref `refs/merge-requests/{iid}/head`, Basic `oauth2:<token>`.
- **Acceptance:** a personal project on gitlab.com (free tier), with nested groups.

## 7. Cross-cutting

- **Security posture unchanged:** tokens only in headers or the X-22 git environment; fixed error sentences; no raw server text; no telemetry. New providers add their token variables to the H1 leak test.
- **Docs:** one page per provider (token scopes checklist, URL shapes, known limits) and one per tool.
- **Clean-room:** fixtures are synthetic; examples use `github.example.com`, `gitlab.example.com`.

## 8. Owner decisions (2026-10-08)
1. Order: tools first (2A → 2D → 2E → 2B → 2C).
2. One `v2.0.0` release, with a release candidate per phase.
3. `pr_describe` may edit PR descriptions, only inside its marked region; the default stays `comment`.
