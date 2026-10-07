# Setup guide

This guide takes you from nothing to a first review: install review-mcp, create
tokens, point it at an LLM, register it with an MCP client, and run it once.
It covers the default transport, stdio, where the MCP client starts review-mcp
as a local process. For a shared server see [Serve mode](serve.md).

Every host name below is a placeholder (`your-gitea.example`,
`bitbucket.example.com`, `llm.example.com`). Replace them with your own.

1. [Install](#1-install)
2. [Create tokens](#2-create-tokens)
3. [Choose the LLM endpoint](#3-choose-the-llm-endpoint)
4. [Register with an MCP client](#4-register-with-an-mcp-client)
5. [First run](#5-first-run)
6. [When something fails](#6-when-something-fails)

## 1. Install

Pick one. All three give you the same binary.

### npm

```sh
npx -y @nevzatcirak/review-mcp version
```

The package downloads nothing at install time and runs no install script: the
binary for your platform comes from an optional dependency
(`@nevzatcirak/review-mcp-<os>-<cpu>`). In an MCP client you use the same
`npx -y @nevzatcirak/review-mcp` as the server command (see
[step 4](#4-register-with-an-mcp-client)). While the project is in release
candidates, install the candidate with the `next` tag:
`npx -y @nevzatcirak/review-mcp@next`.

### Release archive

Download the archive for your platform and `checksums.txt` from the
[GitHub Releases page](https://github.com/nevzatcirak/review-mcp/releases).
Archives are named `review-mcp_<version>_<os>_<arch>.tar.gz` (`.zip` on
Windows), for `linux`, `darwin` and `windows` on `amd64` and `arm64`. Each one
contains the binary, `LICENSE`, `NOTICE`, `THIRD_PARTY_LICENSES` and the
README.

Verify the SHA-256 checksum before you run anything from it:

```sh
# Linux
sha256sum --ignore-missing -c checksums.txt
# macOS
shasum -a 256 --ignore-missing -c checksums.txt
```

```powershell
# Windows PowerShell: compare the printed hash with the line in checksums.txt
Get-FileHash .\review-mcp_<version>_windows_amd64.zip -Algorithm SHA256
```

The output must say `OK` for your archive. Then unpack it and put `review-mcp`
on your `PATH`:

```sh
tar -xzf review-mcp_<version>_linux_amd64.tar.gz
./review-mcp version
```

### go install

Requires the Go version named in the `go` line of
[`go.mod`](../go.mod) (Go 1.26 or newer at the time of writing).

```sh
go install github.com/nevzatcirak/review-mcp/cmd/review-mcp@<tag>
```

Replace `<tag>` with a release tag such as `v1.0.0-rc.1`. The binary lands in
`$(go env GOBIN)` or `$(go env GOPATH)/bin`; put that directory on your `PATH`.
A binary installed this way reports the version Go embeds for the tag, and no
commit unless you build from a checkout with `make build`.

## 2. Create tokens

review-mcp acts as you: it uses your tokens, never a service account of its
own. Create one token per provider you use, give it the least access that
works, and keep it in a secret manager or your shell profile, never in a file
that is checked in.

Use a read-only token to start. `pr_review` and `pr_ask` need write access only
when you pass `publish=true`, and `pr_comment_reply` and `pr_comment_create`
always write.

### What each token needs

| Use | Needs |
|---|---|
| Read only: `pr_comments`, `pr_review` and `pr_ask` with `publish=false` | the read scopes below |
| Read and write: `publish=true`, `pr_comment_reply`, `pr_comment_create` | the read scopes plus the write scope |

> **Every scope in this guide is unconfirmed.** They are derived from the API
> calls the code makes, in the tables below, and each is marked "verify at V1
> acceptance (item A3)": the acceptance run creates tokens with exactly these
> scopes and records every one that was wrong. If a call fails with
> `authentication failed` and you followed the table, you may have found one:
> see [Token scopes](troubleshooting.md#token-scopes).

### Gitea

Create a token under **Settings, Applications, Generate New Token** in your
Gitea (`https://your-gitea.example/user/settings/applications`). Gitea names
the scope categories `repository` and `issue`, each with `read` and `write`.

| Token | Scopes | Status |
|---|---|---|
| Read only | `read:repository`, `read:issue`, `read:user` | verify at V1 acceptance (item A3) |
| Read and write | `write:repository` (posting inline comments as a review), `write:issue` (posting and editing PR comments), `read:user`; each write scope covers reading as well (add `read:issue` if your Gitea version lists them separately) | verify at V1 acceptance (item A3) |

`read:user` lets review-mcp identify the token's own user (`GET /api/v1/user`),
so that it only ever edits or skips its own comments.

Endpoints the Gitea provider calls (under `{base_url}/api/v1/repos/{owner}/{repo}`, except `GET /api/v1/user`):

| Method and endpoint | Purpose | Scope | Code |
|---|---|---|---|
| `GET /pulls/{n}` | the pull request: title, description, branches, head and merge base | `read:repository` | `internal/provider/gitea/gitea.go` (`GetPullRequest`) |
| `GET /pulls/{n}/commits` | commit messages | `read:repository` | `internal/provider/gitea/gitea.go` (`GetCommitMessages`) |
| `GET /pulls/{n}.diff` | the unified diff | `read:repository` | `internal/provider/gitea/diff.go` (`GetDiff`) |
| `GET /pulls/{n}/files` | the list of changed files | `read:repository` | `internal/provider/gitea/diff.go` (`GetDiff`) |
| `GET /raw/{path}?ref={sha}` | full file contents on both sides, for extra context | `read:repository` | `internal/provider/gitea/diff.go` (`rawPath`, `fetchSide`) |
| `GET /pulls/{n}/reviews` and `GET /pulls/{n}/reviews/{id}/comments` | inline review comments (`pr_comments`, reply lookup); before posting inline comments, a check for a pending review of the token's user; after posting, the posted comments' links | `read:repository` | `internal/provider/gitea/comments.go` (`reviewComments`), `internal/provider/gitea/write.go` (`PostInlineComments`) |
| `POST /pulls/{n}/reviews` | inline comments (`publish=true`, and `pr_comment_create` with `file` and `line`), posted as one review with event `COMMENT` on the head commit (one review per comment if that fails) | `write:repository` | `internal/provider/gitea/write.go` (`PostInlineComments`) |
| `DELETE /pulls/{n}/reviews/{id}` | delete a pending review of the token's user that a failed post left behind | `write:repository` | `internal/provider/gitea/write.go` (`deletePending`) |
| `GET /issues/{n}/comments` | PR-level comments (`pr_comments`) | `read:issue` | `internal/provider/gitea/comments.go` (`ListThreads`) |
| `GET /issues/comments/{id}` | find the comment a reply refers to (`pr_comment_reply`); re-read a comment and check its author before editing it | `read:issue` | `internal/provider/gitea/comments.go` (`ReplyToComment`), `internal/provider/gitea/write.go` (`EditComment`) |
| `PATCH /issues/comments/{id}` | edit a PR comment the token's user wrote (the review overview) | `write:issue` | `internal/provider/gitea/write.go` (`EditComment`) |
| `GET /api/v1/user` | the token's own user | `read:user` | `internal/provider/gitea/write.go` (`CurrentUser`) |
| `POST /issues/{n}/comments` | post a comment: `publish=true` (the overview), `pr_comment_create` without a file, and `pr_comment_reply` (Gitea has no thread reply, so a reply is a new PR-level comment with a quote line) | `write:issue` | `internal/provider/gitea/gitea.go` (`PostComment`) |

`pr_comment_create` adds no endpoint of its own: a PR-level comment needs the
`write:issue` rows above, and an inline one also reads the diff (the
`read:repository` rows) to validate the line and needs `write:repository`.
Every row's scope is "verify at V1 acceptance (item A3)".

### Bitbucket Server / Data Center

review-mcp needs Bitbucket Server or Data Center 7.0 or later and an HTTP
access token. Create it under **Profile picture, Manage account, HTTP access
tokens, Create token** (a personal token), or on the project or repository
under **Settings, Access tokens** (a project or repository token). The token
is sent as `Authorization: Bearer ...`; basic authentication is not supported.

The permission level is chosen when you create the token. Bitbucket tokens have
no per-endpoint scopes.

| Token | Permission | Status |
|---|---|---|
| Read only | Project read or Repository read | verify at V1 acceptance (item A3) |
| Read and write | Repository write | verify at V1 acceptance (item A3) |

Endpoints the Bitbucket Server provider calls (under `{base_url}`, which
includes your context path):

| Method and endpoint | Purpose | Permission | Code |
|---|---|---|---|
| `GET /rest/api/1.0/application-properties` | version probe (7.0 or later), where a failure is ignored; and the token's own user, read from the `X-AUSERNAME` and `X-AUSERID` response headers, where a failure is an error | read | `internal/provider/bitbucketserver/base.go` (`ensureSupported`), `internal/provider/bitbucketserver/write.go` (`CurrentUser`) |
| `GET /rest/api/1.0/projects/{key}/repos/{slug}/pull-requests/{id}` | the pull request | read | `internal/provider/bitbucketserver/bitbucketserver.go` (`GetPullRequest`) |
| `GET .../pull-requests/{id}/commits` | commit messages, and the ancestor walk | read | `bitbucketserver.go`, `base.go` (`ancestorWalk`) |
| `GET /rest/api/latest/projects/{key}/repos/{slug}/pull-requests/{id}/merge-base` | the merge base; a 404 falls back to the ancestor walk | read | `internal/provider/bitbucketserver/base.go` |
| `GET /rest/api/1.0/projects/{key}/repos/{slug}/commits?since=..&until=..` | ancestor walk fallback | read | `internal/provider/bitbucketserver/base.go` (`ancestorWalk`) |
| `GET .../pull-requests/{id}/changes` | the list of changed files | read | `internal/provider/bitbucketserver/diff.go` (`GetDiff`) |
| `GET /rest/api/1.0/projects/{key}/repos/{slug}/raw/{path}?at={sha}` | file contents on both sides; the patch is built from them | read | `internal/provider/bitbucketserver/diff.go` (`rawPath`, `fetchSide`) |
| `GET .../pull-requests/{id}/activities` | comment threads (`pr_comments`, reply lookup) | read | `internal/provider/bitbucketserver/comments.go` (`ListThreads`) |
| `POST .../pull-requests/{id}/comments` | PR-level comment (`publish=true`, `pr_comment_create` without a file) | write | `internal/provider/bitbucketserver/bitbucketserver.go` (`PostComment`) |
| `POST .../pull-requests/{id}/comments` with `parent` | reply inside a thread (`pr_comment_reply`) | write | `internal/provider/bitbucketserver/comments.go` (`ReplyToComment`) |
| `POST .../pull-requests/{id}/comments` with `anchor` | inline comment on a changed or context line, one request per comment (`publish=true`, and `pr_comment_create` with `file` and `line`) | read (to be confirmed at A3) | `internal/provider/bitbucketserver/write.go` (`PostInlineComments`) |
| `GET .../pull-requests/{id}/comments/{commentId}` | a comment's version and author, read before editing it | read | `internal/provider/bitbucketserver/write.go` (`EditComment`) |
| `PUT .../pull-requests/{id}/comments/{commentId}` | edit a comment the token's user wrote (the review overview) | read (to be confirmed at A3) | `internal/provider/bitbucketserver/write.go` (`EditComment`) |

`pr_comment_create` uses the comment rows above plus the read rows for the
pull request, its changes and its file contents, which validate the line
against the diff. The permission column says what the code needs; whether Bitbucket accepts a
read-level token for commenting is exactly what item A3 checks. Bitbucket lets
users with repository read permission comment on pull requests, so Repository
read may be enough for posting, inline comments and editing your own comments;
this is to be confirmed at A3. Every row is
"verify at V1 acceptance (item A3)". Personal-repository URLs
(`/users/{user}/repos/...`) use `/users/{user}` in place of `/projects/{key}`
in the endpoints above.

### Give review-mcp the tokens

Pass tokens through the environment only:

| Variable | Provider |
|---|---|
| `REVIEW_MCP_GITEA_TOKEN` | Gitea |
| `REVIEW_MCP_BITBUCKET_SERVER_TOKEN` | Bitbucket Server |

Also set the base URL of each provider you use (`REVIEW_MCP_GITEA_BASE_URL`,
`REVIEW_MCP_BITBUCKET_SERVER_BASE_URL`; the Bitbucket one includes any context
path). A provider without a base URL is disabled. At least one must be
enabled. More about base URLs, CA certificates and the context path is in
[Troubleshooting](troubleshooting.md#base-url-notes).

## 3. Choose the LLM endpoint

review-mcp works with any OpenAI-compatible chat-completions endpoint. It has
no built-in default for the URL, the model or the window size.

| Variable | Example | Notes |
|---|---|---|
| `REVIEW_MCP_LLM_BASE_URL` | `https://llm.example.com/v1` | `/chat/completions` is appended to it |
| `REVIEW_MCP_LLM_MODEL` | `your-model-name` | the model name your endpoint expects |
| `REVIEW_MCP_LLM_API_KEY` | (secret) | sent as a bearer token |
| `REVIEW_MCP_LLM_CONTEXT_WINDOW` | `32000` | the real size of the window, in tokens (at least 4096) |
| `REVIEW_MCP_LLM_MAX_OUTPUT_TOKENS` | `2000` | optional; how long an answer you allow |

The pull request's title, description and diff are sent to this endpoint when
you call `pr_review` or `pr_ask`. Use an endpoint you trust with that code.

### `context_window` and `max_output_tokens`

- Set `context_window` to the window your server really runs, not the largest
  the model supports. An endpoint started with a smaller limit answers
  `the request is too long for the model's context window`.
- Set `max_output_tokens` to what you want an answer to be allowed, well below
  the window. It reserves room for the answer: the diff gets
  `context_window - (max(max_output_tokens, 1000) + 500) - prompt tokens`.
  When you set it, it is also sent to the endpoint as the completion limit.
- Too small a window is not an error until nothing is left for the diff; large
  pull requests then lose files, which the review lists as omitted.

The derivation is in [Getting started](getting-started.md#how-the-context-window-shapes-the-diff-budget)
and [Reviewing pull requests](review.md).

### `diag review --dry-run`

With the environment set, check a real pull request without calling the model:

```sh
review-mcp diag review https://your-gitea.example/octo/demo/pulls/7 --dry-run
```

It prints a JSON report with the prompt, diff and request token estimates, the
budget (soft and hard limits), and the coverage: which files made it in and
which were omitted or skipped, and why. Nothing is sent to the LLM. If files
you care about are omitted, raise `context_window` (if your model really has
room), add `ignore.*` rules for generated files, or review a smaller pull
request. `diag diff` shows the exact diff text. See
[Reviewing pull requests](review.md).

## 4. Register with an MCP client

Each client below starts review-mcp as a local process over stdio. The server
command is `npx -y @nevzatcirak/review-mcp`; if you installed a binary, use its
path (and the argument `stdio` is optional, since it is the default).

The configuration needs the LLM and provider variables from steps 2 and 3. To
keep tokens out of files, prefer the client's environment-variable references
where it has them; the examples say where it does.

<!--
  Client syntax verified against each client's own documentation on 2026-10-06,
  by fetching the pages:
    Claude Code: https://code.claude.com/docs/en/mcp
      - `claude mcp add [options] <name> -- <command> [args...]`; `--env KEY=value`
        must come before the server name; `--` is mandatory before the command.
      - .mcp.json supports ${VAR} and ${VAR:-default} in command, args, env, url, headers.
    opencode: https://opencode.ai/docs/mcp-servers/
      - top-level key "mcp"; a "local" server has "command" (an array), "environment", "enabled";
      - a "remote" server has "url" and "headers";
      - {env:VAR} substitution in config values.
  The generic mcpServers block is the common convention and is NOT verified
  against any one client; clients differ (see getting-started.md).
-->

### Claude Code

Add the server from the command line. `--env` flags go before the name, and
`--` separates the command:

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

The shell expands `"$REVIEW_MCP_LLM_API_KEY"` when you run the command, so the
value is written into Claude Code's configuration file. To keep the secret out
of it, put the server in a project `.mcp.json`, where Claude Code expands
`${VAR}` itself when it starts:

```json
{
  "mcpServers": {
    "review-mcp": {
      "command": "npx",
      "args": ["-y", "@nevzatcirak/review-mcp"],
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

Export the two variables in the environment Claude Code starts from.

### opencode

In `opencode.json` (project) or your global opencode config. A `local` server
takes the command as an array and its variables under `environment`;
`{env:NAME}` reads a variable from the environment opencode runs in:

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

### Any other client

Most clients accept a JSON object like this one. File location, the top-level
key (`mcpServers`, `servers`, `mcp`) and whether `${NAME}` references are
expanded differ per client (not verified for any client other than the two
above); check yours, and if it does not expand references, export the variables
in the environment the client starts from instead of writing tokens in the
file.

```json
{
  "mcpServers": {
    "review-mcp": {
      "command": "npx",
      "args": ["-y", "@nevzatcirak/review-mcp"],
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

For Bitbucket Server, use `REVIEW_MCP_BITBUCKET_SERVER_BASE_URL` and
`REVIEW_MCP_BITBUCKET_SERVER_TOKEN` instead of (or next to) the Gitea pair.

## 5. First run

Do these in order. Each step checks one more layer, and each stops at the
layer that is broken.

1. **`server_info`.** Ask your client to call the `server_info` tool. It
   reports the version, the commit, the enabled providers and the effective
   non-secret configuration; secrets show only as `set` or `unset`. If the
   status is `config_invalid`, the `problems` list says exactly what to fix;
   correct the environment and restart the server (reload the client).
2. **`diag pr`.** In a terminal with the same environment, check that your
   provider and token can reach a real pull request, without an LLM or a
   client:

   ```sh
   review-mcp diag pr https://your-gitea.example/octo/demo/pulls/7
   ```

   It prints the pull request, its files and the base strategy as JSON. Compare
   the files with the provider's web page. See
   [Troubleshooting](troubleshooting.md#check-connectivity-with-diag-pr).
3. **`pr_review` with `publish=false`.** Ask the client to review the pull
   request and not to publish: call `pr_review` with `pr_url` set and
   `publish` left at its default `false`. You get the review as markdown in
   the client; nothing is written to the pull request. Read the coverage
   section to see what was and was not reviewed. Only when you are happy with
   the result, use `publish=true` (needs the write scope).

Ask a question the same way with `pr_ask` ([Asking questions](ask.md)), and
read comment threads with `pr_comments`.

## 6. When something fails

Every error review-mcp returns is one of a fixed set of sentences, each tied to
a cause and a configuration key to check. The complete table, including the
serve-mode errors, is in
[Troubleshooting: every error sentence](troubleshooting.md#every-error-sentence).
Start there with the exact sentence you received. For anything about tokens and
scopes, see [Token scopes](troubleshooting.md#token-scopes).
