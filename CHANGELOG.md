# Changelog

All notable changes to review-mcp are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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

[1.0.0-rc.1]: https://github.com/nevzatcirak/review-mcp/releases/tag/v1.0.0-rc.1
