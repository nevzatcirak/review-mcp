# Changelog

All notable changes to review-mcp are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.1.0-rc.1] - Unreleased

First v1.1 release candidate. Large pull requests no longer lose most of
their files: the files that do not fit one model call are reviewed in further
calls, up to a configurable number, and the answers are merged into one
review. It ships after v1.0.0 is tagged.

### Added

- Reviews in parts (X-19). When the diff does not hold every reviewable file,
  `pr_review` reviews the rest in further model calls ("parts") and merges the
  answers.
  - `review.max_chunks` (`REVIEW_MCP_REVIEW_MAX_CHUNKS`, default 8, 1 to 32)
    sets the most parts; `1` reviews in one call, as in v1.0.
  - `review.max_total_findings` (`REVIEW_MCP_REVIEW_MAX_TOTAL_FINDINGS`,
    default 10, at most 50) caps the merged findings; when set it must be at
    least `review.max_findings`, and the cap is the larger of the two. Findings
    beyond it are counted in a note.
  - The parts run one after another. Each has its own timeout, YAML repair and
    re-ask, and a line in the prompt that says which part it is; a part that
    fits in full is sent with extended context. Progress reports
    `calling model (part I of N)`; in stdio mode a long review becomes a
    background job as before.
  - Merging: a finding an earlier part already returned is dropped (by its
    fingerprint); the effort is the highest; tests count if any part found
    them; security and performance concerns are joined, prefixed with the part
    when more than one part has one. "No concerns" is shown only when every
    part said so: a part that did not answer leaves the field empty, with a
    note naming the part.
  - A part whose model call fails does not fail the review: its files are
    skipped with reason `model_call_failed`, the review is partial and
    publishable, and a note says "Part I of N failed (<class>); its files were
    not reviewed." with a fixed class, never the error text. Only when every
    part fails does the review fail, with the first part's error.
  - A file too large for a part of its own under `diff.large_patch_policy =
    "skip"` is skipped with reason `too_large`.
  - `coverage` in `pr_review`, `pr_ask` and `job_result` (and in every output
    schema) gains `model_calls` and `failed_parts`. The coverage section says
    "Reviewed in N model calls." when there was more than one, in the tool
    text and the published overview.
  - The partial hint names `review.max_chunks` when every allowed part was
    used and files were still left.
- `pr_ask` admits the files the question names first (X-21): a changed file
  whose full path, or base name of at least 5 characters, appears in the
  question as a whole word (case-sensitive) goes before the others. Filters
  still apply. `pr_ask` keeps one model call.
- `server_info` lists the two new `review.*` settings.
- `pr_info` (X-23), a read-only tool (stdio and serve; no LLM call, a read token
  is enough) that answers which branch a pull request merges into and who has
  reviewed or approved it. It returns the title, author, state, draft flag,
  source and target branch, head and merge-base revisions, the human
  `reviewers` (state, requested, stale, time), `approvals` counted over them,
  `required_approvals`, `mergeable` with `merge_blockers`, and
  `review_mcp_activity`.
  - review-mcp's own marked overview, inline findings and AI reviews are
    reported under `review_mcp_activity` and never count as reviewers or
    approvals; a plain review by the same account does.
  - Gitea: the latest non-dismissed approval or changes request of each user
    decides, a requested reviewer without a review is `pending`, `stale` comes
    from the review. Bitbucket Server: `reviewers[]` statuses, `stale` from
    `lastReviewedCommit`, merge status and fixed blockers from the merge
    endpoint (an unknown veto is "other merge check", never server text).
  - `required_approvals` is a number or `null` with the note "not readable
    with this token"; any optional part that cannot be read is `null` with a
    fixed note, and the tool fails only when the pull request itself cannot be
    read.
  - Docs: [Pull request status](docs/pr-info.md), the token checklist in the
    setup guide (Gitea needs `read:repository`, `read:issue` and `read:user`;
    reading branch protection may need repository admin and is optional).
- Docs: "Large pull requests" in the review guide, files the question names in
  the ask guide, long reviews in serve mode, and "The review took several
  minutes" and "Part I of N failed" in the troubleshooting guide.

### Changed

- A deleted file whose removed lines are left out by design and whose name is
  in the diff's list of deleted files now counts as reviewed (X-20): the model
  was told it is deleted. It is listed under "Deleted (listed by name)" and in
  `coverage.deleted_listed` (every output schema). Only deletions the budget
  cut count as not reviewed, so such pull requests are no longer reported as
  partial because of them.
- `diff.max_tokens` now makes each part smaller rather than leaving the files
  out: the files that no longer fit go to further parts.

## [1.0.0]

First stable release. The code is the same as `1.0.0-rc.4`; the release
candidates were validated in use (acceptance record #8, in-use mode). The
sections below list everything that went into 1.0.0.

### Highlights

- Tools: `server_info`, `pr_comments`, `pr_comment_reply`, `pr_comment_create`,
  `pr_review`, `pr_ask` and, over stdio, `job_result`.
- Providers: Gitea and Bitbucket Server (Data Center); any OpenAI-compatible
  LLM endpoint; per-user tokens, never logged or persisted; no telemetry.
- Reviews publish one overview comment, edited in place on later runs, and
  inline comments on the changed lines; existing discussion is taken into
  account so issues are not repeated.
- A partial review says so first and never claims anything about the files it
  did not review.
- The context window is read from the endpoint when it is not configured;
  long calls over stdio continue as background jobs.
- stdio by default; `serve` runs one shared HTTP server with credentials in
  request headers.
- Distribution: GitHub release archives with provenance and npm packages
  (`@nevzatcirak/review-mcp`) published with trusted publishing.

## [1.0.0-rc.4]

Fourth release candidate. It makes a partial review say so: a review that did
not cover every changed file no longer reads like a clean bill of health. It is
validated by the V1 acceptance run (section H4) before v1.0.0.

### Added

- Partial-coverage honesty (X-18). A result is partial when at least one
  reviewable changed file was omitted by the diff budget, clipped, skipped by
  the provider for size or file limit, or unreadable; files filtered by the
  ignore rules, binary files and files without a text change do not count.
  - `coverage` in `pr_review`, `pr_ask` and `job_result` (and in every output
    schema) gains `partial`, `reviewed_files`, `total_files` and
    `not_reviewed_files`, with `reviewed_files + not_reviewed_files ==
    total_files`.
  - A partial result leads with a fixed line, before everything else: "Partial
    review: N of M changed files were reviewed. K files were not reviewed (see
    Coverage); nothing is concluded about them." (`pr_ask`: "Partial answer: N of
    M changed files were used for this answer. ..."). In the MCP text it is the
    first line; in the published overview it is directly under the heading
    (Gitea: a warning blockquote, Bitbucket Server: a bold line) and stays after
    the overview is edited in place.
  - Notes add how to cover every file: "To review every file, raise or unset
    diff.max_tokens, or use a model with a larger context window." when
    `diff.max_tokens` is the limit that applied, else "To review every file, use
    a model with a larger context window."
  - The tool descriptions of `pr_review`, `pr_ask` and `job_result` tell the
    client model to report how many files were not reviewed and never to say
    that they have no issues.
- Docs: "Partial reviews" in the review and ask guides and "The review says
  partial" in the troubleshooting guide.

### Changed

- In a partial result every "no concerns" statement is limited to what was
  read: "No security concerns identified in the reviewed files", "No
  performance concerns identified in the reviewed files" and "No key issues
  found in the reviewed files". Complete results read as before.
- The npm publish job now verifies every package version on the registry after
  publishing (retries for up to 30 minutes) and fails, naming the packages, when
  one is missing or was staged instead of published. Nothing is republished.

## [1.0.0-rc.3]

Third release candidate. It is for slow and local models: the context window can
come from the endpoint, long reviews no longer hit the client timeout, and the
diff can be capped. It is validated by the V1 acceptance run (section J) before
v1.0.0.

### Added

- The context window from the endpoint: `llm.context_window` is now optional.
  When it is unset, review-mcp asks `GET {llm.base_url}/models` once per
  process and uses 90 % of the window the entry for `llm.model` reports (the
  first of `max_model_len`, `context_length`, `context_window`,
  `max_context_length`; 4096 at least). Training-size fields are never used. A
  set value always wins, and when the endpoint reports no window the call fails
  with a sentence naming `llm.context_window`. Ollama does not report the served
  context, so set it there. `server_info` shows the resolved value and its
  source, and `diag diff --context-window N` works without an LLM.
- Background jobs for long calls (stdio only). `pr_review` and `pr_ask` wait at
  most `wait_seconds` (argument or `llm.wait_seconds`,
  `REVIEW_MCP_LLM_WAIT_SECONDS`, default 45, 0 to 600) and then answer with a
  running status and a `job_id`; the run continues in the server, and
  `publish=true` still posts. The new seventh tool `job_result` returns the
  finished result exactly as the original tool would have, or the running status
  again. At most 4 jobs run at once; results are kept for 30 minutes, at most
  64, in memory only. A fast run returns exactly what it did before. Serve mode
  has no jobs and ignores `wait_seconds`.
- `diff.max_tokens` (`REVIEW_MCP_DIFF_MAX_TOKENS`, at least 1000, unset by
  default) caps the diff budget below the context window, for `pr_review` and
  `pr_ask`. Files that no longer fit are listed in the coverage section. `diag
  diff` and `diag review --dry-run` report which limit applied
  (`budget.limit`).

### Changed

- `llm.timeout_seconds` defaults to 300 (was 120), which local models need on
  large prompts.
- `pr_review`, `pr_ask` and `job_result` declare a root `oneOf` output schema
  (the result or the running status). If a client rejects it, a later candidate
  drops the schema in stdio and keeps the structured content (acceptance J2).

## [1.0.0-rc.2]

Second release candidate. It adds conversation-aware reviews: findings on their
lines, one overview comment that is edited in place, and awareness of what the
pull request discussion already says. It is validated by the V1 acceptance run
(section I) before v1.0.0.

### Added

- Inline findings: with `publish=true`, each finding that falls on a changed or
  context line of the diff is also posted as an inline comment on that line.
  - Gitea gets one review per run (event `COMMENT`, on the head commit);
    Bitbucket Server gets one comment per finding, with the line type computed
    from the diff and never guessed.
  - Findings that cannot be placed on a line stay in the overview, and a note
    says how many.
  - `inline_findings` (tool argument) and `review.inline_findings`
    (`REVIEW_MCP_REVIEW_INLINE_FINDINGS`, default true) switch it.
- A persistent overview: the published review is one comment per pull request
  and token user, found again by a hidden marker and by its author, and edited
  in place on later runs instead of piling up. It shows the run time, the head
  commit, a findings index that links each finding to its inline comment, and
  the coverage. A marker in someone else's comment is never adopted.
  `review.persistent_overview` (`REVIEW_MCP_REVIEW_PERSISTENT_OVERVIEW`,
  default true) switches it.
- A performance field in the review (`review.require_performance`,
  `REVIEW_MCP_REVIEW_REQUIRE_PERFORMANCE`, default true), next to security: "No
  performance concerns identified", or what the model found.
- Discussion awareness: `pr_review` reads the pull request's comment threads and
  gives them to the model as untrusted data, so it does not repeat what is
  already raised. A finding already posted by review-mcp on the same pull
  request (matched by a fingerprint) is not posted again. The overview shows how
  many points were already discussed. `review.max_discussion_tokens`
  (`REVIEW_MCP_REVIEW_MAX_DISCUSSION_TOKENS`, default 1500, 0 turns it off)
  bounds the block; its text is never logged.
- `pr_comment_create`, a sixth tool: posts a new comment, PR-level or on a
  changed line (`file` and `line`). A line that is not part of the diff is
  refused with a fixed sentence and never posted at PR level instead.
- `server_info` lists the new `review.*` settings.
- Provider write surface: the token's own user, editing a comment the token's
  user wrote (refused for anyone else's), and inline comment posting, on Gitea
  and Bitbucket Server.
- `docs/release.md` describes npm trusted publishing.

### Changed

- `pr_review` with `publish=true` posts an overview and inline comments instead
  of one flat comment; the tool description says so. The token now needs the
  scopes listed in the setup guide for reviews and comment editing, and Gitea
  tokens also need `read:user` (all marked "verify at A3").
- `pr_comment_reply` and `pr_comment_create` refuse a body with a line that
  looks like a review-mcp marker, so a comment of ours can never be mistaken
  for an overview or a posted finding.
- The README quick start shows Bitbucket Server and a locally hosted LLM
  endpoint, and the `@next` tag for release candidates.
- The comment URLs in the `pr_comment_create` and `pr_comment_reply` results
  are no longer redacted: they are returned as built from the configured base
  URL and the comment id, so Gitea links keep `#issuecomment-N` and Bitbucket
  Server links keep `?commentId=N`. Logs stay redacted.

### Fixed

- `pr_comment_reply` to an inline (review) comment on Gitea no longer fails
  with a protocol error: Gitea answers the issue-comment lookup with 204 and no
  body for such a comment, and the reply now falls back to the review comments.
- Test fixtures whose directory names differed only by case are renamed. They
  broke checkouts on macOS and Windows and are rejected by the Go module proxy.
  A CI step now fails when two tracked paths are equal under case folding.
- npm packages are published with trusted publishing (GitHub OIDC) and
  provenance; the long-lived npm token is no longer used.

## [1.0.0-rc.1]

First release candidate. It is validated by the V1 acceptance run before v1.0.0.

### Added

- Tool surface over MCP (stdio by default), five tools:
  - `server_info`: version, enabled providers and the effective non-secret
    configuration; secrets are shown only as set or unset.
  - `pr_comments`: lists a pull request's comment threads.
  - `pr_comment_reply`: replies to a comment (in the thread on Bitbucket
    Server; as a quoting PR-level comment on Gitea).
  - `pr_review`: structured review of a pull request with any
    OpenAI-compatible LLM endpoint, optionally published as a PR comment.
  - `pr_ask`: free-text questions about a pull request, grounded in its title,
    description and diff, optionally published as a PR comment.
- Gitea and Bitbucket Server (Data Center 7.0 or later) providers.
- A token-budgeted diff pipeline: file filtering, extra context, compression
  to fit `llm.context_window`, and honest coverage reporting.
- Serve mode (`review-mcp serve`): the same tools over stateless streamable
  HTTP for a shared deployment.
  - Credentials arrive per request in `X-Review-MCP-Gitea-Token`,
    `X-Review-MCP-Bitbucket-Server-Token` and `X-Review-MCP-LLM-API-Key`;
    nothing is kept after a request.
  - Optional bearer access token, TLS or an explicit insecure-HTTP opt-in,
    Host and Origin checks, a concurrency limit (`server_busy`) and `GET /healthz`.
- `diag` commands for connectivity and budget checks (`pr`, `diff`, `review`,
  `ask`, `comment`, `comments`, `reply`) that never print secrets.
- Distribution:
  - GitHub Release archives for linux, darwin and windows on amd64 and arm64,
    with `checksums.txt` and the third-party license bundle.
  - npm packages (`@nevzatcirak/review-mcp` and one optional platform package
    per target) that run without an install script.
  - `go install github.com/nevzatcirak/review-mcp/cmd/review-mcp@<tag>`.
  - A `Dockerfile` for serve mode (no published image in v1).
- Documentation: a setup guide, a serve-mode guide, and guides for review and
  ask.

[1.0.0-rc.3]: https://github.com/nevzatcirak/review-mcp/releases/tag/v1.0.0-rc.3
[1.0.0-rc.2]: https://github.com/nevzatcirak/review-mcp/releases/tag/v1.0.0-rc.2
[1.0.0-rc.1]: https://github.com/nevzatcirak/review-mcp/releases/tag/v1.0.0-rc.1
