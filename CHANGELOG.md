# Changelog

All notable changes to review-mcp are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
  publishing (retries for up to 5 minutes) and fails, naming the packages, when
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
