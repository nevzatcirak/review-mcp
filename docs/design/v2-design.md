# review-mcp "v2" — Design note (GitHub, GitLab, `pr_describe`, `pr_improve`)

| | |
|---|---|
| Status | **Approved in direction** (owner, 2026-10-08: tools first, one 2.0 release, `pr_describe` may edit descriptions in a marked region). Becomes binding through per-phase specs (`docs/plan/v2-*-spec.md`), as v1.1 did. |
| Scope | The v2 backlog of `docs/plan/phase-plan.md`: two new providers (GitHub, GitLab) and two new tools (`pr_describe`, `pr_improve`), plus the provider contract they need. |
| Builds on | v1.1 (`main` after `v1.1.0-rc.1`): chunked review (X-19), `pr_info` (X-23), repository context (X-22). |
| Non-goals | GraphQL clients, GitHub Apps / OAuth flows (tokens only, as today), webhooks or a bot mode, Bitbucket Cloud, Azure DevOps, auto-merge or any write beyond comments, descriptions and suggestions. |

## 0. Release model (owner decision, 2026-10-08)

- **Y-0 — One 2.0 release.** The additions ship together as `v2.0.0`. Each phase still gets its own release candidate (`v2.0.0-rc.N`, npm `next`) so that it is accepted in use as soon as it lands; `latest` moves only at `v2.0.0`. Nothing planned here breaks the tool surface or the configuration; if a phase must break something, its spec says so and the CHANGELOG lists it under "Breaking".
- **Branching.** v2 phase PRs stay unmerged until `v1.1.0` is tagged (the v1.1 merge rule, repeated). After that they merge to `main` one phase at a time; v1.1.x fixes, if any, are cut from the `v1.1.0` tag on a `release/1.1` branch.

## 1. Order (owner decision, 2026-10-08: tools first)

| Phase | Content | Release candidate | Why this position |
|---|---|---|---|
| 2A | Provider contract test suite; resolver and `PRRef` generalisation | with 2D | Every later phase is checked against it. Small. |
| 2D | `pr_describe` | v2.0.0-rc.1 | Useful at once on the providers already in use; no line anchoring. |
| 2E | `pr_improve` | v2.0.0-rc.2 | Reuses P7 anchors and X-19 parts; the highest review value after `pr_review`. |
| 2B | GitHub provider | v2.0.0-rc.3 | Dogfooding: this repository's own PRs are a personal test instance. |
| 2F | Review quality: CI status, project guidelines and file tree, focused reviews | v2.0.0-rc.4 | Added 2026-10-10 (owner decision). After GitHub so that every provider-specific read is designed for three providers at once; before GitLab so that GitLab ships with it. |
| 2C | GitLab provider | v2.0.0-rc.5 | Largest API surface (nested groups, positions, quick actions). |


## 2. Phase 2A — Provider contract

- **Y-1 — One contract test suite for every provider.** `internal/provider/contract` holds table-driven tests written against the `Provider` interface and a per-provider fake server adapter (`Fixture` interface: "serve this PR with these files, comments, reviewers"). Gitea and Bitbucket Server are moved onto it first; their existing tests stay. Covered behaviour: PR metadata, base strategy, diff file list and change types (rename with edits, deletion, binary, size and file limits), comment threads and ordering, reply semantics, ownership check on edit, inline posting results (posted / unanchorable / failed), review status folding, URL building (`FileLineURL`), error classes and that no error text carries a token or raw server text.
  - **[canary]** break one Gitea behaviour (for example drop rename detection): the contract test fails for Gitea only.
- **Y-2 — `PRRef.Namespace` may contain `/`.** It already holds the Gitea owner or Bitbucket project; GitLab needs `group/subgroup/...`. Each factory's `ParsePRPath` decides how many segments are namespace. Logging and cache keys escape it (the X-22 cache layout already escapes path segments).
- **Y-3 — Capabilities grow by need, never by provider name.** New flags: `SuggestionBlocks` (GitHub, GitLab), `QuickActions` (GitLab: every published body is slash-sanitised, P5 rule extended from `pr_ask` to all bodies), `InlineThreadResolution` / `GeneralThreadResolution` (whether resolved state is readable, per thread kind), `DescriptionEdit`. Code branches on capabilities only.

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

- **Precondition for native suggestion blocks (WP-2h review):** `InlineComment.EndLine` and a per-provider suggestion style; `improved_code` re-indented by the difference between the real lines and `existing_code` before a block replaces them.
- **Y-12 — API surface.** REST v3 only. `github.com` (`https://api.github.com`) and GitHub Enterprise Server (`{base}/api/v3`); the web base and API base are both configured or derived (`github.base_url`, optional `github.api_url`). Token: classic or fine-grained PAT, `Authorization: Bearer`. Rate limits: `403/429` with `X-RateLimit-Remaining: 0` or `Retry-After` map to a fixed class `rate_limited` with the reset time; one bounded wait only when it is under the request deadline.
- **Y-13 — Diff.** `GET /pulls/{n}/files` (paged; GitHub caps at 3000 files: the rest are reported as provider-skipped `file_limit`). A file whose `patch` is absent (too large) is `size_limit`. Contents through `GET /contents/{path}?ref=` with the raw media type. Base: `GET /compare/{base}...{head}` → `merge_base_commit` (`github:merge_base`).
- **Y-14 — Comments.** General: issue comments. Inline: review comments with `line`/`side`/`start_line`; one review per run for inline findings (X-11 pattern) via `POST /pulls/{n}/reviews` with event `COMMENT`. Replies: `POST /pulls/{n}/comments/{id}/replies` for review comments; a quoting issue comment for general ones (as Gitea). **Thread resolution is not readable through REST:** both resolution flags false, threads are all shown and the note says resolved state is unavailable. GraphQL is not used.
- **Y-15 — `pr_info`.** Requested reviewers plus reviews (latest decisive per user; `DISMISSED` does not count; `stale` when `commit_id` ≠ head). Required approvals: first `GET /rules/branches/{branch}` (rulesets, readable with read access: `pull_request.required_approving_review_count`), then classic branch protection (needs admin: else `null` plus note). `mergeable_state` maps to fixed blockers (`dirty` → merge conflict, `blocked`, `behind`, `unstable` → required checks failing; unknown → other).
- **Y-16 — Repository context.** Clone `{web}/{owner}/{repo}.git`, ref `refs/pull/{n}/head`, Basic `x-access-token:<token>` through `http.extraHeader` (X-22 rules unchanged).
- **Acceptance:** this repository's own PRs on github.com.

## 5a. Phase 2F — Review quality (scope; design pending)

Origin: the owner's proposal that a review should know whether the change builds, whether it fits the project, and look at it from specialist angles (security, architecture, performance, UX). Market reference points: CodeRabbit (linters plus path instructions), Greptile (codebase graph; hosted sandbox execution), Qodo Merge / PR-Agent (best-practices file, CI feedback, ticket compliance), multi-agent review plugins. The design note and the spec follow; the entries below fix the scope only.

- **Y-22 — CI and check status.** Read the statuses and checks of the head commit from the provider API (Gitea commit statuses; Bitbucket Server build statuses; GitHub check runs and commit statuses; GitLab pipelines when 2C lands) and give the review a short, budgeted summary: which checks failed or are pending, never raw logs. Reported in the result and the overview; a missing permission is a note, never a failed review. Building or running the code ourselves is rejected (R-1).
- **Y-23 — Project guidelines and file tree.** A repository guidelines file (default candidates such as `.review-mcp/guidelines.md` and `CONTRIBUTING.md`, configurable) read at the base revision, and a budgeted file tree of the directories the change touches, both given to the model as fenced untrusted data (the X-13 pattern), so a review can say that a file is in the wrong place or that a change breaks a stated convention. Reading from the base revision means a pull request cannot rewrite the rules it is judged by.
- **Y-24 — Focused reviews.** `pr_review` gains an optional `focus` (`security`, `architecture`, `performance`, `ux` or free text). A client agent may run several focused reviews in parallel subagents; those calls do not publish and return structured findings that carry their anchors, and one agent merges them and publishes once (single writer; anchored findings inline, the rest in the overview, the X-14 fallback keeps every finding). The pattern is offered to clients through the MCP `instructions` field, the tool description and an MCP prompt (`multi_perspective_review`); the server never fans out on its own. Open question for the design: whether publishing merged findings needs a dedicated mode or the existing tools suffice.
- **Acceptance:** in use on all three providers available by then (Gitea, Bitbucket Server, GitHub).

## 6. Phase 2C — GitLab provider

- **Y-17 — Addressing.** MR URL `{base}/{group}/{subgroup…}/{project}/-/merge_requests/{iid}`, self-managed with a context path supported. Project id is the URL-encoded full path. API v4. Token header `PRIVATE-TOKEN` (personal, project or group tokens).
- **Y-18 — Diff.** `GET /merge_requests/{iid}/diffs` (paged; GitLab 15.7+) with fallback to `/changes` for older servers (recorded as the strategy). Base from `diff_refs` (`base_sha`, `start_sha`, `head_sha`); the diff is against `base_sha` (`gitlab:diff_refs`). Contents via `/repository/files/{path}/raw?ref=`. Collapsed or too-large diffs map to `size_limit`.
- **Y-19 — Comments.** General: notes. Inline: `POST /discussions` with a `position` object (`position_type=text`, the three SHAs, `old_path`/`new_path`, and `new_line` for added lines, `old_line`+`new_line` for context lines). Replies: `POST /discussions/{id}/notes` (in thread). Resolved state readable (both resolution flags true). Every published body is slash-sanitised (`QuickActions`).
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
4. (2026-10-10) Phase 2F, review quality, is added between 2B and 2C (`v2.0.0-rc.4`; GitLab moves to `rc.5`). Building or running pull request code is rejected (R-1 in the decisions document).

## 9. After 2.0 — v2.1 and later (owner decision 2026-10-10; scope only, each item gets a design note)

Taken in full by the owner. Order inside v2.1 is decided when 2.0 ships.

| Item | Content | Target |
|---|---|---|
| V-1 | **Incremental review:** review only the commits since the last review, recorded in the overview marker; rebases and force-pushes fall back to a full review with a note | v2.1 |
| V-2 | **Issue compliance:** judge whether the pull request does what its issue asks (fully, partly, not), with the gaps listed. Two sources: (a) the git provider's own issues (Gitea, GitHub, GitLab), read by review-mcp with the token it already has, the issue found from the title, branch name and description; (b) any other tracker through an optional `issue_context` argument (key, title, description, acceptance criteria, optional URL) on `pr_review` and `pr_describe`, filled by the client agent from a tracker MCP server. Both are budgeted and given to the model as fenced untrusted data | v2.1 |
| V-3 | **Jira: no built-in client (owner decision 2026-10-10).** Jira is reached through an external MCP server in the client (Atlassian's remote MCP server for Cloud; a community server such as `sooperset/mcp-atlassian` in read-only mode for Data Center/Server) that fills `issue_context`; review-mcp never holds a Jira credential. The `instructions` field and the tool descriptions tell the client how to do this, and the setup guide gets a short, generic "with a tracker MCP server" section when V-2 ships. A built-in Jira read path is reconsidered only if CI mode (V-4), which has no client agent, needs it | v2.1 |
| V-4 | **CI mode:** a headless command (and an example GitHub Action / generic CI snippet) that runs a review, describe or improve for the pull request of the current pipeline; credentials from the CI's secrets; still never executes the code (R-1) | v2.1 |
| V-5 | **Repository config:** `.review-mcp.toml` read at the base revision (ignore lists, language, focus defaults, guidelines path); it can never hold secrets or change server URLs, endpoints or limits that protect the user | v2.1 |
| V-6 | **Fallback model chain:** a second model when the first fails or times out (seam exists: `FallbackEligible`) | v2.1 |
| V-7 | **Published container image** for serve mode, signed and with provenance | v2.1 |
| V-8 | **Deeper local analysis:** extend X-22 from symbol search to a read-only local view of the pull request: whole files and the tree from the checkout, and a bounded, read-only tool loop in which the model may ask for files or searches inside the checkout (path-confined, budgeted, logged in the result). The loop runs in the server through the endpoint's tool calling (owner decision 2026-10-10), so it behaves the same in every client and in CI mode; an endpoint without tool calling gets today's single-pass review and a note; deterministic analysis that does not run repository code (for example parsing for symbols, secret patterns); nothing that builds, tests or loads repository configuration as code (R-1) | v2.1 |
| V-9 | **Quality benchmark:** the 20-PR blind evaluation becomes a kept benchmark on personal and public repositories only, used to accept prompt changes (first: the documentation/spec rule from the 1.1 acceptance) | with 2F |
| V-10 | **`pr_tests`:** missing tests for the changed code, verified like `pr_improve` suggestions | v2.2 |
| V-11 | **`pr_labels` and changelog suggestion** | v2.2 |
| V-12 | **More providers** (Bitbucket Cloud, Azure DevOps) | v3 candidate, on demand |
