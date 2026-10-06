# review-mcp

`review-mcp` is a standalone, open-source (MIT) MCP server written in Go that
brings AI-powered pull-request tools to any MCP client, such as Claude Code and
opencode. It reviews a pull request with `pr_review`, answers questions about
one with `pr_ask`, and reads, answers and writes comments on pull requests. It targets Gitea
and Bitbucket Server (Data Center) first, with GitHub planned for later, and
works with any OpenAI-compatible LLM endpoint. Identity is per user: provider
tokens and the LLM API key come from the MCP client configuration (as
environment variables over stdio, the default, or as HTTP headers in `serve`
mode), are never logged and are never persisted. There is no telemetry and there
are no embedded defaults for URLs or endpoints: everything comes from your
configuration.

**Status: release candidate.** v1.0.0-rc.1 is being validated before v1.0.0;
see the [changelog](CHANGELOG.md).

## Quick start

1. Create a read-only token on your provider and note your LLM endpoint
   ([Setup guide, steps 2 and 3](docs/setup.md#2-create-tokens)).
2. Register the server with your client. With npm there is nothing to install.

   **Claude Code**

   ```sh
   claude mcp add review-mcp \
     --env REVIEW_MCP_LLM_BASE_URL=https://llm.example.com/v1 \
     --env REVIEW_MCP_LLM_MODEL=your-model-name \
     --env REVIEW_MCP_LLM_CONTEXT_WINDOW=32000 \
     --env REVIEW_MCP_GITEA_BASE_URL=https://your-gitea.example \
     --env REVIEW_MCP_LLM_API_KEY="$REVIEW_MCP_LLM_API_KEY" \
     --env REVIEW_MCP_GITEA_TOKEN="$REVIEW_MCP_GITEA_TOKEN" \
     -- npx -y @nevzatcirak/review-mcp
   ```

   **opencode** (`opencode.json`)

   ```json
   {
     "$schema": "https://opencode.ai/config.json",
     "mcp": {
       "review-mcp": {
         "type": "local",
         "command": ["npx", "-y", "@nevzatcirak/review-mcp"],
         "enabled": true,
         "environment": {
           "REVIEW_MCP_LLM_BASE_URL": "https://llm.example.com/v1",
           "REVIEW_MCP_LLM_MODEL": "your-model-name",
           "REVIEW_MCP_LLM_CONTEXT_WINDOW": "32000",
           "REVIEW_MCP_LLM_API_KEY": "{env:REVIEW_MCP_LLM_API_KEY}",
           "REVIEW_MCP_GITEA_BASE_URL": "https://your-gitea.example",
           "REVIEW_MCP_GITEA_TOKEN": "{env:REVIEW_MCP_GITEA_TOKEN}"
         }
       }
     }
   }
   ```

   Both providers can be enabled together: add the Bitbucket Server pair next
   to the Gitea one. Each provider is enabled by its base URL; the Bitbucket
   one includes any context path.

   ```sh
   claude mcp add review-mcp \
     --env REVIEW_MCP_LLM_BASE_URL=https://llm.example.com/v1 \
     --env REVIEW_MCP_LLM_MODEL=your-model-name \
     --env REVIEW_MCP_LLM_CONTEXT_WINDOW=32000 \
     --env REVIEW_MCP_GITEA_BASE_URL=https://your-gitea.example \
     --env REVIEW_MCP_BITBUCKET_SERVER_BASE_URL=https://bitbucket.example.com \
     --env REVIEW_MCP_LLM_API_KEY="$REVIEW_MCP_LLM_API_KEY" \
     --env REVIEW_MCP_GITEA_TOKEN="$REVIEW_MCP_GITEA_TOKEN" \
     --env REVIEW_MCP_BITBUCKET_SERVER_TOKEN="$REVIEW_MCP_BITBUCKET_SERVER_TOKEN" \
     -- npx -y @nevzatcirak/review-mcp
   ```

   Release candidates are published under the npm dist-tag `next`; use
   `npx -y @nevzatcirak/review-mcp@next` to run one. Stable releases are
   published as `latest`, which is what the commands above use.

   **Local OpenAI-compatible endpoint.** For a locally hosted server (for
   example `REVIEW_MCP_LLM_BASE_URL=http://localhost:8080/v1`), the API key is
   still required but may be any non-empty placeholder if your server ignores
   it. Set `REVIEW_MCP_LLM_CONTEXT_WINDOW` (`llm.context_window`) to the
   context size your server is actually configured with, not the model's
   maximum.

3. Ask the client to call `server_info`, then to review a pull request without
   publishing it. The [Setup guide](docs/setup.md) walks through the first run,
   keeps tokens out of files, and covers Bitbucket Server, release archives and
   `go install`.

## Tools

| Tool | What it does |
|---|---|
| `server_info` | Version, enabled providers and the effective non-secret configuration; secrets show only as set or unset. |
| `pr_comments` | Lists a pull request's comment threads. |
| `pr_comment_reply` | Replies to a pull request comment (inside the thread on Bitbucket Server; as a quoting PR-level comment on Gitea). |
| `pr_comment_create` | Posts a new comment on a pull request, PR-level or on a changed line (`file` and `line`). A line outside the diff is refused, never posted at PR level instead. |
| `pr_review` | Reviews a pull request with your LLM. The title, description, existing comments and diff are sent to your LLM endpoint. Optionally publishes the review: one overview comment, edited in place on later runs, and inline comments on the changed lines. |
| `pr_ask` | Answers a free-text question about a pull request, grounded in its title, description and diff. Optionally publishes the question and answer as a PR comment. |

## Documentation

- [Setup guide](docs/setup.md): install, token checklist, LLM endpoint, client configuration, first run.
- [Serve mode](docs/serve.md): one shared HTTP server for a team, the header contract, TLS, the container.
- [Reviewing pull requests](docs/review.md): what `pr_review` sends to the LLM, choosing `llm.context_window`, reading coverage and notes, `publish` (the overview and inline comments), discussion awareness, `diag review --dry-run`.
- [Asking questions about a pull request](docs/ask.md): what `pr_ask` sends to the LLM, grounding and honesty, coverage, `publish` and the slash sanitization, `diag ask --dry-run`.
- [Getting started](docs/getting-started.md): the minimal configuration and the diff budget in detail.
- [Troubleshooting](docs/troubleshooting.md): the `diag` commands and every error sentence.
- [Changelog](CHANGELOG.md).

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
