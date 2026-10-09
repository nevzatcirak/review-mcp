# v2 phase 2B — GitHub provider: Specification

| | |
|---|---|
| Release | `v2.0.0-rc.3` (prerelease, npm `next`) |
| Design input | `docs/design/v2-design.md` §5 (Y-12…Y-16) and the suggestion-block precondition. Binding; where they differ, this spec wins. |
| Builds on | PR #17 (2E, approved) stacked on #16 (2A + 2D): contract suite, capabilities, `pr_describe`, `pr_improve`. |
| Work packages | WP-2j (client, resolver, read path), WP-2k (comments and threads), WP-2l (inline writes, reviews, suggestion blocks), WP-2m (`pr_info`, description edit, repository context), WP-2n (docs and live acceptance) |
| Branch / PR | `v2-github` from `v2-improve`; draft PR "v2: GitHub provider", stacked. Not merged before #17. |
| Protocol | As for #16/#17 (acknowledge reviews first, one report per package, canaries rerun by the lead with a tree-hash check, every commit gated alone, Windows CI, never merge/tag/publish). |
| Model guidance | WP-2j and WP-2l: Opus. Others: Sonnet. |
| Live acceptance | On **this repository** (github.com/nevzatcirak/review-mcp) with a fine-grained token limited to it; the architect can run the read-only and `diag` checks. Nothing is published to a PR without the owner's go-ahead. |

## 0. Decisions
- **X-28 — GitHub provider (Y-12…Y-16)**, REST only, as specified below.

## 1. WP-2j — Client, resolver and read path (Opus)
1. **Configuration.** `github.base_url` (web, default unset; `https://github.com` is **not** assumed — the X-2 "no embedded defaults" rule holds), optional `github.api_url` (derived: `https://api.github.com` when the web base is `https://github.com`, else `{base}/api/v3`), `github.token` (secret), `github.insecure_skip_verify`, `github.ca_cert_path`, env variables `REVIEW_MCP_GITHUB_*`; serve-mode header `X-Review-MCP-GitHub-Token` (X-10 pattern). `server_info` shows them, secrets as set/unset.
2. **Resolver.** `{base}/{owner}/{repo}/pull/{n}` (optionally followed by `/files`, `/commits`, `#…`); one-segment owner (Y-2 audit applies); `%2F` refused in owner and repo.
3. **HTTP.** `Authorization: Bearer <token>`, `Accept: application/vnd.github+json`, `X-GitHub-Api-Version: 2022-11-28`, `User-Agent: review-mcp/<version>`. Pagination through the `Link` header only (never by guessing pages), with a page cap per list and a fixed note when it is hit.
4. **Rate limits.** `403`/`429` with `X-RateLimit-Remaining: 0` or `Retry-After` (primary and secondary limits) → class `rate_limited` with the reset time in the fixed sentence; one bounded wait only when it ends before the request deadline and is at most 60 s; never a retry loop. A `403` without those headers is `forbidden`.
5. **PR metadata.** `GET /repos/{o}/{r}/pulls/{n}`: title, body, author, head/base refs and SHAs, state (`open`/`closed` + `merged`), `draft`, `mergeable`, `mergeable_state`, web URL.
6. **Base strategy.** `GET /repos/{o}/{r}/compare/{base_sha}...{head_sha}` → `merge_base_commit.sha` (`github:merge_base`); on failure fall back to `base.sha` (`github:base_sha`), recorded and noted.
7. **Diff.** `GET /pulls/{n}/files` (paged; GitHub returns at most 3000 files): `status` (added, modified, removed, renamed, copied → added, changed → modified), `previous_filename`, `patch`. A file without `patch` (too large or binary) → skipped `size_limit` (or `binary` when the API marks it binary or the extension rule says so). Files past the 3000 limit → `file_limit` with a note. Contents for extended context: `GET /repos/{o}/{r}/contents/{path}?ref={sha}` with `Accept: application/vnd.github.raw`; respect `diff.max_files_full_content` / `diff.max_file_bytes` (→ `not_fetched_*` statuses, the Gitea shape).
8. **Commits.** `GET /pulls/{n}/commits` (paged, GitHub caps at 250) for `GetCommitMessages`.
9. **Identity.** `GET /user` → login and id.
10. **Contract.** A `Fixture` for GitHub; `contract.Run` passes. The fake models: pagination via `Link`, the 3000-file cap, a missing `patch`, renamed with edits, `copied`, rate-limit headers.
11. **[canary]** guess the next page instead of following `Link`: the pagination case fails. **[canary]** treat a `403` with `X-RateLimit-Remaining: 0` as `forbidden`: the rate-limit case fails.

Commit: `feat(github): add the GitHub provider read path`

## 2. WP-2k — Comments and threads (Sonnet)
1. **Threads.** General: issue comments (`GET /issues/{n}/comments`). Inline: review comments (`GET /pulls/{n}/comments`), grouped into threads by `in_reply_to_id`, with `path`, `line`/`original_line`, `side`. Reviews with a body (`GET /pulls/{n}/reviews`) appear as general entries authored by the reviewer.
2. **Resolution.** Not readable through REST: `InlineThreadResolution=false`, `GeneralThreadResolution=false`; all threads are shown; the `pr_comments` output carries the fixed note "Resolved state is not available on GitHub without GraphQL; all threads are shown." (once per call).
3. **Reply.** Inline thread: `POST /pulls/{n}/comments/{id}/replies` (in thread). General: a quoting issue comment (`in_thread=false`), as Gitea.
4. **Edit.** Issue comment `PATCH /issues/comments/{id}`; review comment `PATCH /pulls/comments/{id}`; ownership check through `user.id` before any write.
5. **Post.** `POST /issues/{n}/comments`.
6. **Contract** cases pass, including `general_reply` and the ownership refusal.

Commit: `feat(github): read, reply to and edit pull request comments`

## 3. WP-2l — Inline writes, reviews and suggestion blocks (Opus)
1. **One review per run** (X-11 pattern): `POST /pulls/{n}/reviews` with `event: COMMENT`, `commit_id: head_sha`, `comments[]` with `path`, `line`, `side: RIGHT`, and `start_line`/`start_side` for multi-line ranges. On a `422` for the batch, post items one by one through `POST /pulls/{n}/comments` to find the unanchorable ones; each result carries `Reason`.
2. **`InlineComment.EndLine`** is added (precondition): providers that cannot express ranges ignore it (Gitea, Bitbucket post on the first line as today; contract checks this).
3. **Suggestion blocks.** GitHub sets `SuggestionBlocks=true`. `pr_improve` emits a ```` ```suggestion ```` block on the range `start_line…line` only when the suggestion is verified, the range lies on new-side lines of one hunk, and `improved_code` is **re-indented** by the difference between the real head lines and `existing_code` (precondition). Otherwise the diff-block rendering stays. **[canary]** skip re-indentation: a test where the model dedented `existing_code` produces a mis-indented block and fails.
4. **Context lines:** GitHub accepts comments on any line inside a hunk on the RIGHT side; anchoring uses the same new-side rule.
5. **Contract** inline case passes (posted / posted / unanchorable).

Commit: `feat(github): post inline findings and native suggestions in one review`

## 4. WP-2m — `pr_info`, description edit, repository context (Sonnet)
1. **`pr_info` (Y-15).** Requested reviewers (`requested_reviewers`, `requested_teams` listed as teams), reviews folded per user (latest of `APPROVED`/`CHANGES_REQUESTED`; `DISMISSED` does not count; `COMMENTED` only without a decisive one; `stale` when `commit_id` ≠ head). Required approvals: `GET /repos/{o}/{r}/rules/branches/{branch}` (`pull_request` rule `required_approving_review_count`, max over matching rules); else classic protection `GET /branches/{branch}/protection` (admin only → `null` + note). Blockers from `mergeable_state`: `dirty` → merge conflict, `blocked` → required reviews or checks, `behind` → branch behind base, `unstable` → required checks failing, `draft` → draft; unknown → other merge check. Own marked reviews excluded.
2. **Description edit.** `DescriptionEdit=true`: `PATCH /pulls/{n}` with `title`/`body` only (partial update; no version); the WP-2d re-read concurrency rule applies (Gitea path). Draft is a flag, so the WIP-prefix rule does nothing.
3. **Repository context (Y-16).** Clone URL `{web}/{owner}/{repo}.git`, PR ref `refs/pull/{n}/head`, Basic `x-access-token:<token>` via `http.extraHeader` (X-22 rules unchanged); auth scheme order: Basic first (GitHub does not accept a bare Bearer for git over HTTPS).
4. Contract cases for `update_pull_request` and review status pass.

Commit: `feat(github): add pr_info, description edits and repository context for GitHub`

## 5. WP-2n — Docs and live acceptance (Sonnet)
- `docs/github.md`: token (fine-grained: Pull requests read/write, Contents read, Metadata read; classic: `repo` or `public_repo`), URL shapes, GHES, rate limits, what REST cannot show (resolution), suggestion blocks.
- README, setup, troubleshooting, CHANGELOG `2.0.0-rc.3`, X-28, NOTICE if anything is adapted.
- **Live acceptance (on this repository):**
  - **Q1** `diag pr` / `diag comments` on a real PR of this repository: files, renames, threads match the UI.
  - **Q2** `pr_info` on a merged PR and an open PR: target branch, reviewers, `mergeable_state` blockers.
  - **Q3** (owner's go-ahead) `pr_review` and `pr_improve` with `publish=true` on a throwaway PR of this repository: one review, inline comments on the right lines, a suggestion block that applies cleanly with GitHub's "Commit suggestion"; second run posts no duplicates.
  - **Q4** `pr_describe` in description mode on the throwaway PR: author text unchanged.
  - **Q5** Rate-limit handling observed or simulated (record which).

Commit: `docs: document the GitHub provider`
