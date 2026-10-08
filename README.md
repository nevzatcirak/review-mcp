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

**Status: stable (1.0.0).** Stable releases are published under the npm
dist-tag `latest`; release candidates of the next version under `next`. See the
[changelog](CHANGELOG.md).

## Quick start

1. Create a token on your provider and note your LLM endpoint
   ([Setup guide, steps 2 and 3](docs/setup.md#2-create-tokens)). A read-only
   token is enough for reviews and questions that are not published.
2. Register the server with your client. With npm there is nothing to install.
   Pin the exact version (for example `@nevzatcirak/review-mcp@1.0.0`) so that
   `npx` does not reuse an older cached copy; update the pin to upgrade.

   **Claude Code**

   ```sh
   claude mcp add review-mcp \
     --env REVIEW_MCP_LLM_BASE_URL=https://llm.example.com/v1 \
     --env REVIEW_MCP_LLM_MODEL=your-model-name \
     --env REVIEW_MCP_GITEA_BASE_URL=https://your-gitea.example \
     --env REVIEW_MCP_LLM_API_KEY="$REVIEW_MCP_LLM_API_KEY" \
     --env REVIEW_MCP_GITEA_TOKEN="$REVIEW_MCP_GITEA_TOKEN" \
     -- npx -y @nevzatcirak/review-mcp@1.0.0
   ```

   **opencode** (`opencode.json`)

   ```json
   {
     "$schema": "https://opencode.ai/config.json",
     "mcp": {
       "review-mcp": {
         "type": "local",
         "command": ["npx", "-y", "@nevzatcirak/review-mcp@1.0.0"],
         "enabled": true,
         "environment": {
           "REVIEW_MCP_LLM_BASE_URL": "https://llm.example.com/v1",
           "REVIEW_MCP_LLM_MODEL": "your-model-name",
           "REVIEW_MCP_LLM_API_KEY": "{env:REVIEW_MCP_LLM_API_KEY}",
           "REVIEW_MCP_GITEA_BASE_URL": "https://your-gitea.example",
           "REVIEW_MCP_GITEA_TOKEN": "{env:REVIEW_MCP_GITEA_TOKEN}"
         }
       }
     }
   }
   ```

   **Bitbucket Server.** Each provider is enabled by its base URL (the
   Bitbucket one includes any context path), and both can be enabled
   together. With Bitbucket Server only, replace the Gitea pair:

   ```json
   "environment": {
     "REVIEW_MCP_LLM_BASE_URL": "https://llm.example.com/v1",
     "REVIEW_MCP_LLM_MODEL": "your-model-name",
     "REVIEW_MCP_LLM_API_KEY": "{env:REVIEW_MCP_LLM_API_KEY}",
     "REVIEW_MCP_BITBUCKET_SERVER_BASE_URL": "https://bitbucket.example.com",
     "REVIEW_MCP_BITBUCKET_SERVER_TOKEN": "{env:REVIEW_MCP_BITBUCKET_SERVER_TOKEN}"
   }
   ```

   **Local or slow model.** For a locally hosted OpenAI-compatible server
   (for example `http://localhost:8080/v1`):

   ```json
   "environment": {
     "REVIEW_MCP_LLM_BASE_URL": "http://localhost:8080/v1",
     "REVIEW_MCP_LLM_MODEL": "your-model-name",
     "REVIEW_MCP_LLM_API_KEY": "local",
     "REVIEW_MCP_DIFF_MAX_TOKENS": "24000",
     "REVIEW_MCP_BITBUCKET_SERVER_BASE_URL": "https://bitbucket.example.com",
     "REVIEW_MCP_BITBUCKET_SERVER_TOKEN": "{env:REVIEW_MCP_BITBUCKET_SERVER_TOKEN}"
   }
   ```

   - The API key is required but may be any non-empty placeholder when the
     server ignores it.
   - **Context window:** `REVIEW_MCP_LLM_CONTEXT_WINDOW` is optional. When it
     is unset, review-mcp reads the served context size from the endpoint's
     model list and budgets 90 % of it. If the endpoint does not report it
     (Ollama, for example), set it to the context size your server actually
     runs with, not the model's maximum.
   - **`REVIEW_MCP_DIFF_MAX_TOKENS`** (optional) caps how much of the diff is
     sent in one model call, independently of the context window. A smaller
     prompt means a faster answer from a slow model. A pull request that does
     not fit is reviewed in several parts, one call each
     (`REVIEW_MCP_REVIEW_MAX_CHUNKS`, default 8, 1 to 32), so the cap now makes
     each part smaller, not the review shorter. Files left after the last part
     are listed in the review's coverage section instead of being reviewed.
     The merged review shows at most `REVIEW_MCP_REVIEW_MAX_TOTAL_FINDINGS`
     findings (default 10, up to 50; when you set it, at least
     `review.max_findings`).
   - **Long calls (stdio):** a `pr_review` or `pr_ask` call that is still
     running after `REVIEW_MCP_LLM_WAIT_SECONDS` (default 45) answers with a
     `job_id`; ask the client to fetch the result and it calls `job_result`.
     This keeps every call under typical client timeouts.

   All settings, their defaults and the TOML equivalents are in the
   [Setup guide](docs/setup.md) and [Reviewing pull requests](docs/review.md).

3. Ask the client to call `server_info`, then to review a pull request without
   publishing it. The [Setup guide](docs/setup.md) walks through the first run,
   keeps tokens out of files, and covers release archives and `go install`.

## Repository context (opt-in)

A diff does not show who depends on what it changes. With
`REVIEW_MCP_CONTEXT_REPO_ENABLED=true` (stdio only, needs `git` 2.31 or later),
`pr_review` and `pr_ask` also fetch the pull request head into a local,
self-pruning cache, search where the changed symbols are used, and add the
best uses to the prompt within a token budget. Off by default; the token never
touches disk or logs. Settings:

| Key | Env | Default |
|---|---|---|
| `context.repo.enabled` | `REVIEW_MCP_CONTEXT_REPO_ENABLED` | `false` |
| `context.repo.cache_dir` | `REVIEW_MCP_CONTEXT_REPO_CACHE_DIR` | OS user cache dir + `review-mcp/repos` |
| `context.repo.idle_days` | `REVIEW_MCP_CONTEXT_REPO_IDLE_DAYS` | `7` |
| `context.repo.max_cache_mb` | `REVIEW_MCP_CONTEXT_REPO_MAX_CACHE_MB` | `2048` |
| `context.repo.max_repo_mb` | `REVIEW_MCP_CONTEXT_REPO_MAX_REPO_MB` | `500` |
| `context.repo.fetch_timeout_seconds` | `REVIEW_MCP_CONTEXT_REPO_FETCH_TIMEOUT_SECONDS` | `60` |
| `context.repo.max_symbols` | `REVIEW_MCP_CONTEXT_REPO_MAX_SYMBOLS` | `20` |
| `context.repo.max_hits_per_symbol` | `REVIEW_MCP_CONTEXT_REPO_MAX_HITS_PER_SYMBOL` | `5` |
| `context.repo.max_tokens` | `REVIEW_MCP_CONTEXT_REPO_MAX_TOKENS` | `2000` |

See [Repository context](docs/repo-context.md) for the details. `review-mcp
diag cache [--prune]` lists and sweeps the cache.

## Tools

| Tool | What it does |
|---|---|
| `server_info` | Version, enabled providers and the effective non-secret configuration; secrets show only as set or unset. |
| `pr_comments` | Lists a pull request's comment threads. |
| `pr_info` | Which branch a pull request merges into and who has reviewed or approved it: human reviewers with their states, approval counts, required approvals and merge status where the provider exposes them. review-mcp's own reviews and comments are reported separately and never count as approvals. Read-only, no LLM call; see [Pull request status](docs/pr-info.md). |
| `pr_comment_reply` | Replies to a pull request comment (inside the thread on Bitbucket Server; as a quoting PR-level comment on Gitea). |
| `pr_comment_create` | Posts a new comment on a pull request, PR-level or on a changed line (`file` and `line`). A line outside the diff is refused, never posted at PR level instead. |
| `pr_review` | Reviews a pull request with your LLM. The title, description, existing comments and diff are sent to your LLM endpoint. Optionally publishes the review: one overview comment, edited in place on later runs, and inline comments on the changed lines. |
| `pr_ask` | Answers a free-text question about a pull request, grounded in its title, description and diff. Optionally publishes the question and answer as a PR comment. |
| `job_result` | Returns the result of a `pr_review` or `pr_ask` call that took longer than `wait_seconds` and answered with a `job_id` instead. stdio only; see [Slow endpoints](docs/review.md#slow-endpoints). |

## Documentation

- [Setup guide](docs/setup.md): install, token checklist, LLM endpoint, client configuration, first run.
- [Serve mode](docs/serve.md): one shared HTTP server for a team, the header contract, TLS, the container.
- [Reviewing pull requests](docs/review.md): what `pr_review` sends to the LLM, choosing `llm.context_window`, reading coverage and notes, large pull requests reviewed in parts, `publish` (the overview and inline comments), discussion awareness, slow endpoints and `job_result`, `diag review --dry-run`.
- [Asking questions about a pull request](docs/ask.md): what `pr_ask` sends to the LLM, files the question names, grounding and honesty, coverage, `publish` and the slash sanitization, `diag ask --dry-run`.
- [Pull request status](docs/pr-info.md): what `pr_info` reports (target branch, human reviewers, approvals, merge status), where each fact comes from, and why review-mcp's own reviews are not reviewers.
- [Repository context](docs/repo-context.md): opt-in, stdio only: shows the model where the symbols a pull request changes are used elsewhere in the project (a cached `git` fetch of the head, `git grep`, a budgeted prompt block), its configuration, the cache, the security properties, the coverage line and the evaluation harness.
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
