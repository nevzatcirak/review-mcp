# v2 phase 2C — GitLab provider: Specification

| | |
|---|---|
| Release | `v2.0.0-rc.6` (prerelease, npm `next`); the last release candidate before `v2.0.0` |
| Design input | `docs/design/v2-design.md` §6 (Y-17…Y-21), §5a (the 2F reads every provider implements) and §7. R-1 (never build or run pull request code) is binding. Where this spec and the design note differ, this spec wins; §0 lists the amendments. |
| Builds on | `main` after 2F (`3773db8`): the contract suite, capabilities (`QuickActions`, `SuggestionStyleOffset`), X-29 duplicates by place, X-30 code spans, X-31 `GetChecks`, X-32 `GetFileAt`/`ListDir`, X-33 focused reviews. |
| Work packages | WP-6a (client, resolver, read path), WP-6b (comments and threads), WP-6c (inline discussions, suggestion blocks, quick-action safety), WP-6d (`pr_info`, description edit, CI, repository context), WP-6e (docs and acceptance) |
| Branch / PR | `v2-gitlab` from `main`; draft PR "v2: GitLab provider (2C)" |
| Protocol | As for #23: acknowledge every new architect review (match `[architect review]` and `## [architect review]`) before each package; one report per package; canaries with mutation and failure text, rerun by the lead one at a time with a tree-hash check; every commit gated alone in a detached worktree (`golang:1.26.0`, golangci-lint v2.14.0); Windows CI; DESIGN-QUESTIONs with a recommendation; never merge, tag or publish. |
| Model guidance | WP-6a and WP-6c: Opus. WP-6b, WP-6d, WP-6e: Sonnet. |
| Clean-room | Fixtures are synthetic. Examples use `gitlab.example.com` and `https://example.com/gitlab` (a context path); group and project names are placeholders (`group/sub/project`). |
| Live acceptance | A personal project on gitlab.com (Free tier) inside a nested group, run by the owner after rc.6 (§5). Nothing is published to a merge request without the owner's go-ahead. |

## 0. Decisions

- **X-34 — GitLab provider (Y-17…Y-21)**, REST API v4 only, as specified below.
- **X-35 — Quick-action safety on GitLab.** Every published body stays slash-sanitised (`QuickActions`), with one refinement for native suggestion blocks (WP-6c item 4) and one refusal for description edits (WP-6d item 2). The rule never depends on how GitLab's quick-action parser treats code blocks.

**Amendments to the design note (owner defaults, approved 2026-10-14):**

1. **Minimum GitLab version 16.0** (released 2023-05; every 15.x release is out of maintenance). Consequences: `GET …/diffs` is the only diff source (the `/changes` fallback of Y-18 is dropped); `detailed_merge_status` and the `draft` field are always present. An endpoint that answers 404 after the merge request itself was read is `unsupported_version` with the hint "GitLab 16.0 or newer is required".
2. **Inline findings are posted as individual discussions**, one request per finding (Y-19). GitLab's batched review (draft notes plus bulk publish) is not used in rc.6; it would send one notification instead of several, and it moves to the v2.1 backlog.
3. **CI reads the commit statuses of the head commit** in the merge request's project (WP-6d item 3). Merged-results and merge-train pipelines (Premium) and pipelines that ran in a fork's project are not read in rc.6; `docs/gitlab.md` says so, and they move to the backlog.
4. **Quick-action rule X-35** as specified in WP-6c item 4 and WP-6d item 2.

## 1. WP-6a — Client, resolver and read path (Opus)

1. **Configuration.** `gitlab.base_url` (web base, may include a context path such as `https://example.com/gitlab`; no default — `https://gitlab.com` is **not** assumed, the X-2 rule holds), `gitlab.token` (secret), `gitlab.ca_cert`, `gitlab.insecure_skip_verify`; env variables `REVIEW_MCP_GITLAB_*`; serve-mode header `X-Review-MCP-GitLab-Token` (X-10 pattern). The API base is always `{base}/api/v4` (no separate setting). `server_info` shows them, the secret as set/unset. Validation messages as for GitHub (token required when the base is set; a token without a base is a warning; "no provider enabled" names all four bases).
2. **Resolver.** `{base}/{namespace…}/{project}/-/merge_requests/{iid}`, optionally followed by `/diffs`, `/commits`, `/pipelines` or `#…`. The namespace is every segment between the base (with its context path) and the project, at least one (`user/project` and `group/sub/sub2/project` both work; Y-2). Only the `/-/` form is accepted. `%2F`, `..` and empty segments are refused. Project id for the API: the URL-encoded full path (`group%2Fsub%2Fproject`).
3. **HTTP.** `PRIVATE-TOKEN: <token>` (personal, project or group access tokens), `Accept: application/json` (the raw file endpoint: `Accept: */*`; settled DQ-10), `User-Agent: review-mcp/<version>`.
   - **Encoded slashes survive.** The project id and file paths contain `%2F`; requests set `URL.RawPath` (or build the request URL so that Go does not decode them). **[canary]** build the URL from `Path` only: the fake sees `/projects/group/sub/project/…` and the nested-group read case fails.
   - **Pagination** through the `Link` header only (`rel="next"`), with a page cap per list and a fixed note when it is hit; the repository tree uses keyset pagination (`pagination=keyset`), also followed through `Link`.
4. **Rate limits.** `429` (and `403` with `RateLimit-Remaining: 0`) with `Retry-After` or `RateLimit-Reset` → class `rate_limited` with the reset time in the fixed sentence; one bounded wait only when it ends before the request deadline and is at most 60 s; never a retry loop. Any other `403` and a `401` are `auth`, as for every provider (settled DQ-2: there is no `forbidden` class).
5. **Merge request.** `GET /projects/{id}/merge_requests/{iid}`: `title`, `description`, `author` (`id`, `username`, `name`), `source_branch`, `target_branch`, `sha` (head), `diff_refs`, `state` (`opened` → open; `merged` → merged; `closed`, `locked` → closed), `draft`, `detailed_merge_status`, `web_url`, `changes_count`, `source_project_id`, `target_project_id`.
6. **Base strategy.** `diff_refs.base_sha` (`gitlab:diff_refs`); `start_sha` and `head_sha` are kept for inline positions (WP-6c). The head is `diff_refs.head_sha`, the commit GitLab's diff was computed for. The diff is **not ready** when `diff_refs` is empty (right after a merge request is created; GitLab fills it asynchronously) or when the merge request's `sha` differs from `diff_refs.head_sha` (a push GitLab has not finished processing; settled DQ-6): the read is retried once after 2 s (inside the request deadline); if it is still not ready the call fails with class `upstream` and the fixed hint "the merge request's diff is not ready yet; try again in a moment". The diff, the contents and the inline positions therefore always belong to one revision.
7. **Diff.** `GET /projects/{id}/merge_requests/{iid}/diffs` (paged, `per_page=100`):
   - Change type from `new_file` (added), `deleted_file` (deleted), `renamed_file` (renamed, `old_path` → `OldPath`), else modified.
   - `diff` holds hunks without the `---`/`+++` header; the existing hunk parser is used as for GitHub's `patch`.
   - An empty `diff` on a renamed file with equal modes is a rename without edits (the existing shape). An empty `diff` on another file: `too_large` or `collapsed` true → skipped `size_limit`; otherwise binary when the existing binary rule says so, else a change with no hunks (mode-only, the Gitea shape).
   - `changes_count` ending in `+` (GitLab's diff limit was hit) → the files GitLab did not list are not known: the fixed note "GitLab did not list every changed file (its diff limit was reached); the files it left out are not reviewed." in `Diff.Notes` (the GitHub 3000-file path; settled DQ-3 — a cross-provider `Diff.Incomplete` flag that marks coverage partial is backlog).
   - The diff page cap (40 pages of 100) adds "review-mcp reads at most 4000 changed files of a merge request; the files GitLab listed after them are not reviewed." (settled DQ-8). Every note ends with a period (settled DQ-11).
   - Contents for extended context: `GET /projects/{id}/repository/files/{path}/raw?ref={sha}` (path URL-encoded including `/`), with `diff.max_files_full_content` / `diff.max_file_bytes` (`not_fetched_*` statuses, the Gitea shape).
8. **Commits.** `GET /projects/{id}/merge_requests/{iid}/commits` (paged). GitLab lists the newest first; `GetCommitMessages` returns them **oldest first** (contract). **[canary]** skip the reversal: the commit-order contract case fails.
9. **Identity.** `GET /user` → `username` and `id` (works for project and group access tokens, whose user is a bot).
10. **2F reads.**
    - `GetFileAt` uses the same request as the content fetch (item 7), capped at `diff.max_file_bytes`; 404 → `not_found`.
    - `ListDir`: `GET /projects/{id}/repository/tree?path={dir}&ref={rev}&per_page=100&pagination=keyset`; `type` `tree` → directory, `blob` and `commit` (submodule) → not; sorted, truncated at `MaxDirEntries`; 404 → `not_found`; the root is `""`.
    - `GetChecks` is WP-6d.
11. **`FileLineURL`.** `{base}/{full_path}/-/blob/{head_sha}/{path}#L{line}` with each path segment escaped.
12. **Contract.** A `Fixture` for GitLab; `contract.Run` passes. The fake models: nested groups with `%2F`, a context path, `Link` pagination, keyset tree pagination, empty `diff_refs` then filled, renamed with and without edits, `too_large`, `changes_count` with `+`, newest-first commits, rate-limit headers.
13. **[canary]** treat `changes_count` "1000+" as complete: the diff-limit case fails. **[canary]** follow a guessed page instead of `Link`: the pagination case fails.

Commit: `feat(gitlab): add the GitLab provider read path`

## 2. WP-6b — Comments and threads (Sonnet)

1. **Threads.** `GET /projects/{id}/merge_requests/{iid}/discussions` (paged). System notes (`system: true`) are excluded. A discussion whose first note has a `position` (`type: DiffNote`) is inline (`Path` = `position.new_path`, `Line` = `position.new_line`; a note on a removed line has no new line → `Line` 0), otherwise general. `Thread.ID` and `CommentItem.ID` are note ids (the discussion id is internal).
2. **Resolution.** Both flags true (`InlineThreadResolution`, `GeneralThreadResolution`). `Resolved` comes from the discussion's notes (`resolvable` and `resolved`); a discussion that is not resolvable (an individual general note) has `Resolved` nil.
3. **Outdated.** A DiffNote is `Outdated` when its `position.head_sha` differs from the merge request's current `diff_refs.head_sha`. (GitLab moves a note's position forward on new pushes when the line can still be traced; a position left on an older head means the line could not be traced.) This feeds X-29: an outdated own finding never blocks a new one. To be confirmed live (§5 Q3).
4. **Reply.** `ReplyToComment(noteID)` finds the note's discussion in the discussions list (bounded by the page cap; a note that is not found is `not_found`) and posts `POST …/discussions/{discussion_id}/notes` (in thread, for general and inline discussions alike).
5. **Edit.** `PUT /projects/{id}/merge_requests/{iid}/notes/{note_id}` with `body`; ownership check through `author.id` against `CurrentUser` before any write.
6. **Post.** `POST /projects/{id}/merge_requests/{iid}/notes`.
7. **Comment URL** (X-29 link): `{web_url}#note_{note_id}`.
8. Every body goes through `SanitizeBody` (already the case in every caller; the contract checks that a body with a line `/close` is posted as ` /close`).
9. **Contract** cases pass, including `general_reply` (in thread on GitLab), the ownership refusal, resolution, outdated. **[canary]** treat every DiffNote as current: the outdated case fails.

Commit: `feat(gitlab): read, reply to and edit merge request discussions`

## 3. WP-6c — Inline discussions, suggestion blocks and quick-action safety (Opus)

1. **Position.** Each inline item is one `POST /projects/{id}/merge_requests/{iid}/discussions` with `body` and `position`: `position_type=text`, `base_sha`, `start_sha`, `head_sha` (from `diff_refs`), `new_path`, `old_path` (the old path for a rename, else the new path), and
   - an added line: `new_line`;
   - a context line: `old_line` **and** `new_line`.
2. **`InlineComment.OldLine` (new, provider-neutral).** The old-side line of a context line, 0 for an added line; filled where `LineType` is computed (the anchoring step that already knows the hunk). Providers that do not need it ignore it (contract checks this). If `LineType` is computed in more than one place, write a DESIGN-QUESTION instead of duplicating the logic. **[canary]** leave `OldLine` 0 for a context line: the context-line contract case is `unanchorable` instead of posted.
3. **Results.**
   - `201` with a note of `type: DiffNote` → posted.
   - `400` whose structured error names the position or the line code → `unanchorable`; other `4xx`/`5xx` → `failed`. No server text is kept.
   - **Stray-note guard:** a `201` whose note is not a `DiffNote` (GitLab created a general note because it could not place the position) → the provider deletes that note at once (`DELETE …/notes/{note_id}`, its own note just created), and the item is `unanchorable`; if the delete fails, the item is `failed` with a fixed note "an inline comment GitLab could not place was posted as a general comment and could not be removed". **[canary]** accept a non-DiffNote `201` as posted: the stray-note case fails.
   - Ranges: GitLab anchors one line; `EndLine` is ignored for the anchor (the comment sits on `Line`), as Gitea does.
4. **Suggestion blocks (X-35).** GitLab sets `SuggestionBlocks=true`, `SuggestionStyle=offset`; the existing renderer emits `suggestion:-0+N`. Before a native block is used on a provider with `QuickActions`:
   - **`SanitizeBody` becomes suggestion-aware:** inside a fenced block whose info string is `suggestion` or `suggestion:-N+M` (fence of three or more backticks or tildes, up to three spaces of indent, closed by the same character at least as long), a line is prefixed with a space only when it could be a quick action: `/` followed by a letter, digit or underscore. A line such as `// comment` or `/* … */` is left as it is there. Outside such blocks nothing changes (every line starting with `/` is prefixed). `SanitizeQuickActions` (the unconditional `pr_ask` sanitiser) is unchanged.
   - **Fallback:** when the suggested code has a line that could be a quick action (sanitising would change the block), the suggestion is rendered as the diff block instead, with the fixed reason "a line of the suggested code starts with a slash command, so it is shown as a diff". This keeps the suggestion exact and never publishes a possible command.
   - The reason is published in the inline comment itself, as the paragraph "Note: a line of the suggested code starts with a slash command, so it is shown as a diff." above the marker (settled DQ-c4).
   - Tests: a Go doc comment `// Retry …` at column 0 in `improved_code` gives a native block whose content is byte-identical after `SanitizeBody`; a line `/merge` gives the diff block with the reason; a hand-written ```` ```suggestion ```` fence with `/merge` is still sanitised — at the sanitiser and on a raw body (`pr_comment_create`); through the finding path the review renderer already escapes the model's backticks, so that test asserts the end state only (settled DQ-c5). **[canary]** exempt every line inside suggestion fences: the hand-written-fence case fails. **[canary]** keep sanitising `//` lines inside the block: the doc-comment case fails (the published block would add a space to the code).
5. Re-indentation, verification and the one-hunk rule of Y-11 are unchanged.
6. **Contract** inline case passes (posted / posted / unanchorable), plus the context-line and stray-note cases.

Commit: `feat(gitlab): post inline findings and native suggestions on merge requests`

## 4. WP-6d — `pr_info`, description edit, CI and repository context (Sonnet)

1. **`pr_info` (Y-20).**
   - **Reviewers:** `GET …/merge_requests/{iid}/reviewers` (`state`: `approved` → approved; `requested_changes` → changes_requested; `reviewed` → commented; `unreviewed`, `review_started`, `unapproved` and unknown → pending; all `Requested`); users in `approved_by` of the approvals read who are not reviewers are added as approved, not requested. `Stale` is always false (GitLab reports no commit per approval). A 404 on `/reviewers` → the merge request's `reviewers[]` as pending plus `approved_by`.
   - **Required approvals:** `GET …/merge_requests/{iid}/approvals` → `approvals_required` when the field is present; `0` → 0 with the new note `NoteNoApprovalRule` "no approval rule applies to this merge request"; absent or unreadable → nil with `NoteApprovalsUnreadable`.
   - **Merge status** from `detailed_merge_status`:

     | Value | Mergeable | Blocker |
     |---|---|---|
     | `mergeable` | true | — |
     | `unchecked`, `checking`, `approvals_syncing`, `preparing` | nil | — |
     | `conflict`, `broken_status` | false | `BlockerConflict` |
     | `ci_must_pass`, `ci_still_running`, `external_status_checks`, `security_policy_pipeline_check` | false | `BlockerBuilds` |
     | `discussions_not_resolved` | false | new `BlockerDiscussions` "unresolved discussions must be resolved" |
     | `draft_status` | false | `BlockerDraft` |
     | `need_rebase` | false | `BlockerBehind` |
     | `not_approved` | false | `BlockerApprovals` |
     | `requested_changes` | false | `BlockerNeedsWork` |
     | anything else (`not_open`, `blocked_status`, `merge_request_blocked`, `jira_association_missing`, `title_regex`, `merge_time`, `locked_paths`, unknown) | false | `BlockerOtherCheck` |

   - Own marked notes are never reviewers (GitLab approvals carry no body; review-mcp never approves).
2. **Description edit.** `DescriptionEdit=true`: `PUT …/merge_requests/{iid}` with `title` and/or `description` only; the WP-2d re-read concurrency rule applies (no version).
   - **Draft:** GitLab derives `draft` from the title prefix. The draft-prefix list of `internal/describe` gains GitLab's `Draft:`, `[Draft]` and `(Draft)` (case-insensitive), so replacing the title keeps the draft state and a generated title cannot make a merge request a draft. If the list must become per-provider, write a DESIGN-QUESTION (a capability, never a provider name).
   - **Quick-action refusal (X-35):** GitLab runs quick actions in a description when it is updated, and review-mcp must not change the author's text (Y-5). So when the text **outside** review-mcp's region has a line that could be a quick action (`/` followed by a letter, digit or underscore, after up to three spaces), description mode is refused before any write with the fixed sentence "the description has a line GitLab could run as a quick action; publish in comment mode instead". review-mcp's own region is sanitised as usual. **[canary]** skip the check: the case with an author line `/label ~x` fails.
3. **CI (X-31 on GitLab).** `GetChecks`: `GET /projects/{id}/repository/commits/{sha}/statuses` (paged; GitLab's default returns the latest status per name), strategy `gitlab:commit_statuses`. States: `success` → success; `failed` → failure, or neutral when `allow_failure` is true; `canceled` → error; `skipped` → skipped; `manual` → neutral when `allow_failure` is true, else pending; `created`, `waiting_for_resource`, `preparing`, `pending`, `running`, `scheduled` → pending; unknown → neutral. Name from `name`, URL from `target_url` (cleaned by `NewCheck`). `Required` nil. **[canary]** map a failed job with `allow_failure: false` to neutral: the failing-state case fails.
4. **Repository context (Y-21).** Clone URL `{base}/{full_path}.git`, ref `refs/merge-requests/{iid}/head`, HTTP Basic `oauth2:<token>` through `http.extraHeader` (X-22 rules unchanged; a new scheme next to `x-access-token`).
5. **Contract** cases for `update_pull_request`, review status and checks pass.

Commit: `feat(gitlab): add pr_info, description edits, CI status and repository context for GitLab`

## 5. WP-6e — Docs, decisions and acceptance (Sonnet)

- **`docs/gitlab.md`:** URL shapes (nested groups, context path), tokens and scopes (personal access token: `api` for publishing, `read_api` for read-only use, plus `read_repository` for repository context; project or group access tokens with role Reporter for reads and Developer for publishing), minimum version 16.0, quick-action safety (what is sanitised, the suggestion fallback, the description refusal), what is not read (merged-results and fork pipelines), rate limits.
- README provider list, `docs/setup.md`, `docs/troubleshooting.md` (every new note and refusal verbatim), `docs/pr-info.md` (the GitLab merge-status table), CHANGELOG `## [2.0.0-rc.6] - Unreleased`, decisions doc X-34 and X-35 (rows and sections), the H1 leak test covers `REVIEW_MCP_GITLAB_TOKEN`, NOTICE if anything is adapted.
- **Live acceptance (owner, after rc.6, on gitlab.com in a project under `group/subgroup/`):**
  - **Q1** `diag pr` / `diag comments` on a merge request with a rename, a deletion and a new file in a nested directory: files and threads match the UI; the base strategy is `gitlab:diff_refs`.
  - **Q2** `pr_info` on an open and a merged merge request: reviewers, `approvals_required`, the blocker for a draft and for an unresolved thread (with "All threads must be resolved" enabled in the project).
  - **Q3** (owner's go-ahead) `pr_review` with `publish=true` on a throwaway merge request: inline discussions on an added and a context line; a second run posts nothing new (X-29); after a push that moves the commented line away, the old note counts as outdated.
  - **Q4** `pr_improve` with `publish=true`: a native suggestion applies cleanly with "Apply suggestion"; a suggestion whose code starts with `//` stays native; the overview and comments contain no executed quick action (nothing in the merge request's system notes).
  - **Q5** `pr_describe` in description mode: author text unchanged; with an author line `/label ~probe` in the description the call is refused.
  - **Q5b** Column-0 assumption of every sanitiser: post (with `pr_comment_create`) a note whose lines are ` /label ~probe` (one leading space) and `    /label ~probe` (four spaces); the label is not added. If GitLab runs either, stop: rc.6 is not promoted to 2.0.0, and the sanitiser rule changes first in a fix release candidate (settled DQ-e1).
  - **Q6** CI: with a `.gitlab-ci.yml` that has one passing job, one failing job and one `allow_failure` job, the overview CI line and `pr_info.ci` match the pipeline page.
  - **Q7** Guidelines at base and the file tree (X-32) as on GitHub.
- Not verifiable on gitlab.com Free: required approvals greater than 0 (Premium), merged-results pipelines; recorded as not verified.

Commit: `docs: document the GitLab provider`

## 6. Backlog created by this spec
- A cross-provider `Diff.Incomplete` flag, honoured by coverage, for hosts that stop listing changed files (GitLab's diff limit, GitHub's 3000 files).
- A cross-provider `commit_messages` contract case (order, oldest first).
- GitLab batched review (draft notes + bulk publish) to send one notification per run.
- Merged-results, merge-train and fork pipelines in `GetChecks`.
- Multi-line anchors on GitLab (`line_range`) if a client needs them.
- Merge `improve.anchorFor` into `internal/review/anchor` so the line type is computed in one place (WP-6c DQ-c1).
