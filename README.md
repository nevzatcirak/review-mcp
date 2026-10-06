# review-mcp

**Status: pre-implementation research.** No usable code yet — the repository
currently contains the research groundwork for the implementation.

`review-mcp` is a standalone, open-source (MIT) MCP server written in Go that
brings AI-powered pull-request tools to any MCP client (opencode, Claude Code,
and others). v1 ships two tools: `pr_review` (structured PR review) and
`pr_ask` (free-text Q&A about a PR). It targets Gitea and Bitbucket Server
(Data Center) first — GitHub support comes later — and works with any
OpenAI-compatible LLM endpoint. Identity is per-user: git provider tokens and
the LLM API key are supplied by the MCP client configuration via environment
variables (stdio mode, the default) or HTTP headers (`serve` mode); secrets
are never logged and never persisted. There is no telemetry, and there are no
embedded defaults for URLs or endpoints — everything comes from configuration.

## Attribution

The design is inspired by [PR-Agent](https://github.com/The-PR-Agent/pr-agent)
(MIT). See [NOTICE](NOTICE) for details on what was derived and
`docs/research/pr-agent-porting-map.md` for the pinned upstream revision
studied during the porting research.

## License

MIT — see [LICENSE](LICENSE).

## Development

Requires Go 1.25 or newer and [golangci-lint](https://golangci-lint.run/) v2.

```sh
make build   # build ./review-mcp with version/commit embedded
make test    # go test -race ./...
make lint    # golangci-lint run ./...
make fmt     # gofmt -w .
```
