# review-mcp

**Status: under development.** The server runs over stdio with these tools:
`server_info`, `pr_comments`, `pr_comment_reply`, `pr_review` and `pr_ask`. Start
with [Getting started](docs/getting-started.md).

`review-mcp` is a standalone, open-source (MIT) MCP server written in Go that
brings AI-powered pull-request tools to any MCP client (opencode, Claude Code,
and others). v1 ships two tools: `pr_review` (structured PR review, see
[Reviewing pull requests](docs/review.md)) and `pr_ask` (free-text Q&A about a
PR, see [Asking questions](docs/ask.md)). It targets Gitea and Bitbucket Server
(Data Center) first — GitHub support comes later — and works with any
OpenAI-compatible LLM endpoint. Identity is per-user: git provider tokens and
the LLM API key are supplied by the MCP client configuration via environment
variables (stdio mode, the default) or HTTP headers (`serve` mode); secrets
are never logged and never persisted. There is no telemetry, and there are no
embedded defaults for URLs or endpoints — everything comes from configuration.

## Documentation

- [Getting started](docs/getting-started.md): build, configure, register with a client.
- [Reviewing pull requests](docs/review.md): what `pr_review` sends to the LLM, choosing `llm.context_window`, reading coverage and notes, `publish`, `diag review --dry-run`.
- [Asking questions about a pull request](docs/ask.md): what `pr_ask` sends to the LLM, grounding and honesty, coverage, `publish` and the slash sanitization, `diag ask --dry-run`.
- [Troubleshooting](docs/troubleshooting.md): the `diag` commands and every error message.

## Attribution

The design is inspired by [PR-Agent](https://github.com/The-PR-Agent/pr-agent)
(MIT). The review and question prompt templates, the YAML repair fixtures and
parts of the review and answer output presentation are adapted from PR-Agent; [NOTICE](NOTICE)
lists every adapted file and carries PR-Agent's license. See it for details on
what was derived and
`docs/research/pr-agent-porting-map.md` for the pinned upstream revision
studied during the porting research.

## License

MIT — see [LICENSE](LICENSE).

## Development

Requires Go 1.26 or newer and [golangci-lint](https://golangci-lint.run/) v2.14.0 or newer (v2.x releases built with Go 1.26+; older ones refuse to lint a Go 1.26 module).

```sh
make build   # build ./review-mcp with version/commit embedded
make test    # go test -race ./...
make lint    # golangci-lint run ./...
make fmt     # gofmt -w .
```
