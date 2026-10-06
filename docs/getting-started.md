# Getting started

review-mcp is an MCP server that runs as a local process and talks to your MCP
client over stdio. This guide builds it, sets the minimal configuration, and
registers it with a client.

## Build from source

Requires Go 1.25 or newer.

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
| `REVIEW_MCP_LLM_CONTEXT_WINDOW` | `32000` | at least 4096 |
| `REVIEW_MCP_LLM_API_KEY` | (secret) | required |
| `REVIEW_MCP_GITEA_BASE_URL` | `https://your-gitea.example` | enables the Gitea provider |
| `REVIEW_MCP_GITEA_TOKEN` | (secret) | required when Gitea is enabled |
| `REVIEW_MCP_BITBUCKET_SERVER_BASE_URL` | `https://bitbucket.example.com` | enables Bitbucket Server; include any context path |
| `REVIEW_MCP_BITBUCKET_SERVER_TOKEN` | (secret) | required when Bitbucket Server is enabled |

At least one provider must be enabled. Set only the provider you use. Never
commit tokens or put them in a file that is checked in; keep them in your shell
profile, a secret manager, or the client's own secret mechanism.

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

## Verify

Ask your client to call the `server_info` tool. It reports the version, the
enabled providers and the effective non-secret configuration. Secrets are shown
only as `set` or `unset`.

If the configuration is invalid, the server still starts (degraded mode) so you
can see what to fix: `server_info` returns `status: "config_invalid"` with the
full list of problems, and the same list is logged to stderr. Correct the
environment and restart the server (usually by reloading the MCP client).

If something does not work, see the [troubleshooting guide](troubleshooting.md); `review-mcp diag pr <PR_URL>` checks that your provider and token can reach a pull request.
