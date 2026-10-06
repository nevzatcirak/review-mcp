# Changelog

All notable changes to review-mcp are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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

### Fixed

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

[1.0.0-rc.2]: https://github.com/nevzatcirak/review-mcp/releases/tag/v1.0.0-rc.2
[1.0.0-rc.1]: https://github.com/nevzatcirak/review-mcp/releases/tag/v1.0.0-rc.1
