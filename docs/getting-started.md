# Getting started

review-mcp is an MCP server that runs as a local process and talks to your MCP
client over stdio. This guide builds it, sets the minimal configuration, and
registers it with a client. For the full path from nothing to a first review
(install options, token checklist, client snippets), start with the
[Setup guide](setup.md); to share one server across a team, see
[Serve mode](serve.md).

## Build from source

Requires Go 1.26 or newer.

```sh
go install github.com/nevzatcirak/review-mcp/cmd/review-mcp@<ref>
```

Replace `<ref>` with a tag, branch or commit. The binary is placed in
`$(go env GOBIN)` (or `$(go env GOPATH)/bin`); make sure that directory is on
your `PATH`. To build from a checkout instead, run `make build`.

Check the installation:

```sh
review-mcp version
```

## Minimal configuration

Configuration comes from environment variables (and, optionally, a TOML file
named by `REVIEW_MCP_CONFIG`). Secrets are read from the environment only.

| Variable | Example value | Notes |
|---|---|---|
| `REVIEW_MCP_LLM_BASE_URL` | `https://llm.example.com/v1` | OpenAI-compatible endpoint |
| `REVIEW_MCP_LLM_MODEL` | `your-model-name` | |
| `REVIEW_MCP_LLM_CONTEXT_WINDOW` | `32000` | optional, at least 4096; when unset, read from the endpoint |
| `REVIEW_MCP_LLM_API_KEY` | (secret) | required |
| `REVIEW_MCP_GITEA_BASE_URL` | `https://your-gitea.example` | enables the Gitea provider |
| `REVIEW_MCP_GITEA_TOKEN` | (secret) | required when Gitea is enabled |
| `REVIEW_MCP_BITBUCKET_SERVER_BASE_URL` | `https://bitbucket.example.com` | enables Bitbucket Server; include any context path |
| `REVIEW_MCP_BITBUCKET_SERVER_TOKEN` | (secret) | required when Bitbucket Server is enabled |

At least one provider must be enabled. Set only the provider you use. Never
commit tokens or put them in a file that is checked in; keep them in your shell
profile, a secret manager, or the client's own secret mechanism.

## How the context window shapes the diff budget

review-mcp has no model registry. It reads the window from your endpoint
(`GET {llm.base_url}/models`, 90 % of the value the entry for `llm.model`
reports), or you set it with `llm.context_window` (`REVIEW_MCP_LLM_CONTEXT_WINDOW`,
at least 4096), which always wins. If the endpoint does not report a window,
a call fails and asks you to set `llm.context_window`. Optionally, `llm.max_output_tokens`
(`REVIEW_MCP_LLM_MAX_OUTPUT_TOKENS`) says how many tokens you allow the answer.
The room left for the pull request's diff is computed from them:

| Value | Formula |
|---|---|
| hard reserve | `max(max_output_tokens, 1000)` (1000 when unset) |
| soft reserve | hard reserve + 500 |
| soft limit | `context_window` - soft reserve - prompt tokens |
| hard limit | `context_window` - hard reserve - prompt tokens |
| cap (optional) | `diff.max_tokens`: when set, the soft limit is the smaller of the formula above and this value, and the hard limit is at most the cap + 500 |

"Prompt tokens" is the size of the review instructions, title and
description around the diff (about 1600 to 2200 tokens for the instructions
alone; see [Reviewing pull requests](review.md#prompt-tokens)). The soft limit is what the diff is fitted into (a diff that
fits is sent whole, with extra context around each change; otherwise files are
admitted largest-first until it is reached). The hard limit is a ceiling that
stops further additions. For example, with a 32000-token window, no
`max_output_tokens` and 2205 prompt tokens, the soft limit is
32000 - 1500 - 2205 = 28295 tokens.

`llm.timeout_seconds` (default 300) bounds one request to the model. A large
context window does not make a request faster: a local model that takes minutes
on a 100000-token prompt answers sooner on a smaller one. Set `diff.max_tokens`
(`REVIEW_MCP_DIFF_MAX_TOKENS`, at least 1000, unset by default) to cap the diff
below what the window allows, for example `24000`. The prompt scaffolding and
the reserves are unchanged. The files that no longer fit are reviewed in
further model calls ("parts", up to `review.max_chunks`, default 8; see
[Large pull requests](review.md#large-pull-requests)), so the cap makes each
call smaller, not the review shorter; files left after the last part are
listed as omitted in the coverage section, as with a small window. `diag diff` and
`diag review --dry-run` print `budget.limit`, which is `context_window` or
`diff.max_tokens`, whichever bounds the soft limit, and `budget.max_diff_tokens`
when the cap is set.

Token counts come from a built-in estimator that works offline. It is exact
only for OpenAI-style tokenizers, so every count is multiplied by
`1 + llm.token_estimate_factor` (default `0.3`, allowed 0 to 2). Raise the
factor if your model's tokenizer produces more tokens than the estimate;
lower it if you want to use more of the window.

Choosing the window:

- Use the model's real context window, in tokens, as your server runs it. Some
  servers are started with a smaller limit than the model supports; use that
  limit.
- Set `llm.max_output_tokens` to what you want the answer to be allowed, not
  to the window. It must be smaller than the window.
- A small window is not an error until the reserves leave nothing for the diff.
  Large pull requests are then reviewed in several parts, and files left after
  `review.max_chunks` parts are listed as omitted.

Check the result before you rely on it: `review-mcp diag diff <PR_URL>` prints
the limits, the estimated size and which files made it in. To simulate a
smaller window, set `REVIEW_MCP_LLM_CONTEXT_WINDOW=8000` for one run. See
[Troubleshooting](troubleshooting.md) for how to read the output.

## Register with an MCP client

Most clients accept a JSON `mcpServers` object. The example below passes the
secrets through environment-variable references so no token appears in the
file:

```json
{
  "mcpServers": {
    "review-mcp": {
      "command": "review-mcp",
      "args": ["stdio"],
      "env": {
        "REVIEW_MCP_LLM_BASE_URL": "https://llm.example.com/v1",
        "REVIEW_MCP_LLM_MODEL": "your-model-name",
        "REVIEW_MCP_LLM_CONTEXT_WINDOW": "32000",
        "REVIEW_MCP_LLM_API_KEY": "${REVIEW_MCP_LLM_API_KEY}",
        "REVIEW_MCP_GITEA_BASE_URL": "https://your-gitea.example",
        "REVIEW_MCP_GITEA_TOKEN": "${REVIEW_MCP_GITEA_TOKEN}"
      }
    }
  }
}
```

Clients differ in syntax: the file location, the top-level key (`mcpServers`,
`servers`, `mcp`), the way `command` and `args` are written, and whether and how
environment references such as `${NAME}` are expanded all vary. Check your
client's documentation and adapt the example; if it does not expand
references, export the variables in the environment the client is started from.
If `command` is not found, use the absolute path to the binary.

## Optional: repository context

Set `REVIEW_MCP_CONTEXT_REPO_ENABLED=true` (stdio only, needs `git`) to add the
uses of the changed symbols from the rest of the repository to the prompt; see
[Repository context](repo-context.md).

## Tools

Ten tools in stdio mode, nine in `serve` mode (no `job_result`).

| Tool | What it does |
|---|---|
| `server_info` | Version, enabled providers and the effective non-secret configuration. |
| `pr_comments` | Lists a pull request's comment threads. |
| `pr_info` | Target branch, human reviewers, approval counts and merge status of a pull request; read-only, no LLM call. See [Pull request status](pr-info.md). |
| `pr_comment_reply` | Replies to a pull request comment. |
| `pr_comment_create` | Posts a new comment on a pull request, PR-level or on a changed line (`file` and `line`). |
| `pr_review` | Reviews a pull request with your LLM; see [Reviewing pull requests](review.md). The PR's title, description, existing comments and diff are sent to `llm.base_url`. |
| `pr_ask` | Answers a question about a pull request with your LLM, grounded in its title, description and diff; see [Asking questions](ask.md). The PR content and the question are sent to `llm.base_url`. |
| `pr_describe` | Describes a pull request with your LLM (title, change types, summary, files walkthrough); see [Describing pull requests](describe.md). By default it only reads; `publish=true` writes to the pull request. The PR content is sent to `llm.base_url`. |
| `pr_improve` | Suggests code changes for a pull request with your LLM: existing and improved code, a label and a self-review score, each checked against the head file; see [Suggesting code changes](improve.md). By default it only reads; `publish=true` writes to the pull request. The PR content is sent to `llm.base_url`. |
| `job_result` | stdio only. Returns the result of a `pr_review`, `pr_ask`, `pr_describe` or `pr_improve` call that answered with a `job_id` because it took longer than `wait_seconds`; see [Slow endpoints](review.md#slow-endpoints). |

For reviews, the recommended sampling setting is `REVIEW_MCP_LLM_TEMPERATURE=0.2`
(it is not sent unless you set it).

## Verify

Ask your client to call the `server_info` tool. It reports the version, the
enabled providers and the effective non-secret configuration. Secrets are shown
only as `set` or `unset`.

If the configuration is invalid, the server still starts (degraded mode) so you
can see what to fix: `server_info` returns `status: "config_invalid"` with the
full list of problems, and the same list is logged to stderr. Correct the
environment and restart the server (usually by reloading the MCP client).

If something does not work, see the [troubleshooting guide](troubleshooting.md); `review-mcp diag pr <PR_URL>` checks that your provider and token can reach a pull request, and `review-mcp diag review <PR_URL> --dry-run` and `review-mcp diag ask <PR_URL> --question <TEXT> --dry-run` check the review and question budgets without calling the LLM.
