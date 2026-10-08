# Serve mode

`review-mcp serve` runs the same tools over HTTP (six: everything stdio has except `job_result`, see [Long calls](#long-calls)), for a team that shares one
server instead of everyone running a local process. The default transport,
stdio, is described in the [Setup guide](setup.md).

## When to use which

| | stdio (default) | `serve` |
|---|---|---|
| Who runs it | the MCP client, as a local process, per user | you, once, as a service |
| Where the tokens live | in the client's environment on the user's machine | in each user's client configuration, sent as request headers |
| Best for | one person, one machine | a team that wants one install, one place for base URLs and CA certificates, and nothing to install on laptops |
| Needs | the binary on each machine (or `npx`) | a host, TLS, and a client that can send HTTP headers |

Use stdio unless you have a reason not to. It is simpler and has nothing to
secure. Use serve when many people should share one deployment but each must
still act under their own provider identity.

## Trust model

- **Headers carry identity.** Every request brings the caller's own Gitea token,
  Bitbucket Server token and (by default) LLM API key in headers. The server
  has no identity of its own toward your providers. Provider tokens in the
  server's environment are a startup error in serve mode
  (`serve mode takes provider tokens from request headers; unset REVIEW_MCP_GITEA_TOKEN`),
  so a shared server can never act under one person's identity for everyone.
- **Nothing is kept after a request.** The transport is stateless: there are no
  sessions. Credentials live only in the configuration built for one tool call
  and are gone when it returns. They are not cached, stored or logged. Access
  logs hold the method, path, status, duration and size only; never headers,
  query strings or bodies.
- **TLS is required off loopback.** Tokens in headers are only as private as
  the connection. The server refuses to start on a non-loopback address
  without TLS (`serve.tls_cert` and `serve.tls_key`) unless you set
  `serve.allow_insecure_http` for a TLS-terminating proxy. Loopback means
  `127.0.0.0/8`, `::1` or `localhost`; an empty host or `0.0.0.0` is not
  loopback.
- **The access token protects the server, not the providers.** When
  `REVIEW_MCP_SERVE_ACCESS_TOKEN` is set, every request to `/mcp` must carry
  `Authorization: Bearer <access token>`, or it is refused with 401. This keeps
  strangers from using your server and, with `llm_key_source = server`, from
  spending your LLM key. It is not a user identity: it is one shared secret.
  Provider access is still decided by each person's own provider token.
- **Pull request content goes to your LLM endpoint**, as in stdio mode.

## Start the server

```sh
export REVIEW_MCP_LLM_BASE_URL=https://llm.example.com/v1
export REVIEW_MCP_LLM_MODEL=your-model-name
export REVIEW_MCP_LLM_CONTEXT_WINDOW=32000
export REVIEW_MCP_GITEA_BASE_URL=https://your-gitea.example
export REVIEW_MCP_SERVE_TLS_CERT=/etc/review-mcp/tls.crt
export REVIEW_MCP_SERVE_TLS_KEY=/etc/review-mcp/tls.key
review-mcp serve --listen 0.0.0.0:8787
```

Provider base URLs still come from the server's configuration (a provider
needs its base URL to be enabled); only the tokens move to the headers.
Unlike stdio there is no degraded start: an invalid configuration is logged
(token-free) and the command exits with code 2 before it listens.

Stop it with SIGINT or SIGTERM: it stops accepting connections, drains
in-flight requests for up to 30 seconds, and exits 0.

### Configuration

These keys apply in serve mode only; stdio ignores them. Each can be a TOML key
or an environment variable. The `--listen` flag overrides `serve.listen`.

| Key | Environment variable | Default | Notes |
|---|---|---|---|
| `serve.listen` | `REVIEW_MCP_SERVE_LISTEN` | `127.0.0.1:8787` | `host:port`; the `--listen` flag overrides it |
| `serve.tls_cert` | `REVIEW_MCP_SERVE_TLS_CERT` | unset | PEM path; set together with `tls_key`; TLS 1.2 or later |
| `serve.tls_key` | `REVIEW_MCP_SERVE_TLS_KEY` | unset | PEM path |
| `serve.allow_insecure_http` | `REVIEW_MCP_SERVE_ALLOW_INSECURE_HTTP` | `false` | needed to bind a non-loopback address without TLS, for example behind a TLS-terminating proxy; a warning is logged at startup |
| `serve.llm_key_source` | `REVIEW_MCP_SERVE_LLM_KEY_SOURCE` | `header` | `header`: each caller sends the LLM key; `server`: the server's own `REVIEW_MCP_LLM_API_KEY` is used |
| `serve.allowed_origins` | `REVIEW_MCP_SERVE_ALLOWED_ORIGINS` | empty | comma-separated exact origins, lower case |
| `serve.max_concurrent_calls` | `REVIEW_MCP_SERVE_MAX_CONCURRENT_CALLS` | `4` | 1 to 64 |
| (secret, no file key) | `REVIEW_MCP_SERVE_ACCESS_TOKEN` | unset | required when `llm_key_source = server`; enforced in either mode when set |

Validation rules in serve mode:

- `REVIEW_MCP_GITEA_TOKEN` and `REVIEW_MCP_BITBUCKET_SERVER_TOKEN` must be unset.
- With `llm_key_source = header`, `REVIEW_MCP_LLM_API_KEY` must be unset.
- With `llm_key_source = server`, both `REVIEW_MCP_LLM_API_KEY` and
  `REVIEW_MCP_SERVE_ACCESS_TOKEN` are required (the access token keeps the
  server's LLM key from being used by anyone who finds the URL).
- A non-loopback `listen` host needs both TLS files (readable at startup) or
  `allow_insecure_http = true`.
- All problems are reported together.

## The header contract

| Header | Carries |
|---|---|
| `X-Review-MCP-Gitea-Token` | the caller's Gitea token |
| `X-Review-MCP-Bitbucket-Server-Token` | the caller's Bitbucket Server token |
| `X-Review-MCP-LLM-API-Key` | the LLM key; used only when `llm_key_source = header` (ignored when `server`) |
| `Authorization: Bearer <access token>` | the server access token, when one is configured |

Rules for the three `X-Review-MCP-` headers:

- Values are trimmed (spaces and tabs); an empty value counts as absent.
- A value longer than 4096 bytes, or containing anything other than visible
  ASCII, is rejected with HTTP 400 and `malformed credential header: <header name>`.
  The value is never echoed.
- A header sent twice is malformed too: picking one of two values silently
  could act under the wrong identity.
- A call that needs a credential the request lacks fails with
  `no Gitea token in this request: set the X-Review-MCP-Gitea-Token header in your MCP client configuration`
  (or the Bitbucket Server or LLM equivalent), before any request to the
  provider or the LLM is made.

`server_info` in serve mode adds `transport: serve`, the listen address, the
`llm_key_source`, and, for the request that called it, each credential header
as `set`, `unset` or `malformed`, never its value. Use it to check that your
client really sends the headers.

## Client configuration for a remote server

The server's MCP endpoint is `https://review-mcp.example.com/mcp`. Put each
person's own tokens in their own client configuration, and prefer the client's
environment-variable references so that the tokens stay out of files.

<!--
  Verified against each client's own documentation on 2026-10-06, by fetching
  the pages:
    Claude Code: https://code.claude.com/docs/en/mcp
      - `claude mcp add --transport http <name> <url> --header "KEY: value"`
        (short forms -t, -H);
      - .mcp.json: {"type": "http", "url": ..., "headers": {...}} with ${VAR} expansion in url and headers.
    opencode: https://opencode.ai/docs/mcp-servers/
      - {"type": "remote", "url": ..., "enabled": true, "headers": {...}};
      - {env:VAR} substitution in config values (the page shows it for whole values; embedding in a longer string is NOT verified).
-->

### Claude Code

```sh
claude mcp add --transport http review-mcp https://review-mcp.example.com/mcp \
  --header "X-Review-MCP-Gitea-Token: $REVIEW_MCP_GITEA_TOKEN" \
  --header "X-Review-MCP-LLM-API-Key: $REVIEW_MCP_LLM_API_KEY" \
  --header "Authorization: Bearer $REVIEW_MCP_SERVE_ACCESS_TOKEN"
```

The shell expands the variables when you run the command, so the values land in
Claude Code's configuration. To keep them out, use a project `.mcp.json`, where
Claude Code expands `${VAR}` when it starts:

```json
{
  "mcpServers": {
    "review-mcp": {
      "type": "http",
      "url": "https://review-mcp.example.com/mcp",
      "headers": {
        "X-Review-MCP-Gitea-Token": "${REVIEW_MCP_GITEA_TOKEN}",
        "X-Review-MCP-LLM-API-Key": "${REVIEW_MCP_LLM_API_KEY}",
        "Authorization": "Bearer ${REVIEW_MCP_SERVE_ACCESS_TOKEN}"
      }
    }
  }
}
```

Leave out the headers you do not need: the LLM key header when the server
supplies the key (`llm_key_source = server`), the `Authorization` header when no
access token is configured, and the Gitea or Bitbucket header for a provider
you do not use (`X-Review-MCP-Bitbucket-Server-Token`).

### opencode

A `remote` server with `headers`; `{env:NAME}` reads a variable from the
environment opencode runs in:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "review-mcp": {
      "type": "remote",
      "url": "https://review-mcp.example.com/mcp",
      "enabled": true,
      "headers": {
        "X-Review-MCP-Gitea-Token": "{env:REVIEW_MCP_GITEA_TOKEN}",
        "X-Review-MCP-LLM-API-Key": "{env:REVIEW_MCP_LLM_API_KEY}",
        "Authorization": "Bearer {env:REVIEW_MCP_SERVE_ACCESS_TOKEN}"
      }
    }
  }
}
```

opencode's documentation shows `{env:NAME}` for whole values; whether it is
expanded inside a longer string, as in the `Bearer` line, is not verified. If
it is not, the server will answer 401 and `server_info` will help you see what
arrived.

## Behind a reverse proxy

Terminate TLS at the proxy and run review-mcp on a private address, with
`serve.allow_insecure_http = true`:

```sh
REVIEW_MCP_SERVE_ALLOW_INSECURE_HTTP=true review-mcp serve --listen 0.0.0.0:8787
```

The proxy must forward the request headers (the three `X-Review-MCP-` headers
and `Authorization`) unchanged, must not log them, and must not cache. Do not
expose the plain-HTTP port to anything but the proxy. The startup warning that
plain HTTP is in use is expected.

## What the server checks, in order

On `POST /mcp`, each check has a fixed response body and no tool code runs
until all of them pass:

1. **Body size:** over 1 MiB gets 413.
2. **Host (loopback listeners only):** when you listen on a loopback address,
   the `Host` header must be a loopback name or address with the configured
   port, or the request gets 403. This blocks DNS-rebinding attacks from a
   web page against a local server.
3. **Origin:** a request with an `Origin` header (a browser) gets 403 unless
   that origin is listed in `serve.allowed_origins`. The comparison is exact,
   so write each origin as a browser sends it: lower case, scheme and host
   and optional port, no path and no trailing slash
   (`https://app.example.com`). Requests without `Origin`, which is how MCP
   clients normally connect, pass.
4. **Access token:** when configured, a missing or wrong bearer token gets 401
   with `WWW-Authenticate: Bearer`.
5. **Credential headers:** a malformed value gets 400 (see above).
6. **Concurrency:** tool calls (not the protocol handshake) take one of
   `serve.max_concurrent_calls` slots. When all are taken, the call fails at
   once with `the server is busy: retry shortly` (class `server_busy`)
   instead of queuing. Retry shortly, or raise `serve.max_concurrent_calls`
   (1 to 64; each running call can hold a long LLM request).

### Endpoints

| Path | Response |
|---|---|
| `POST /mcp` | the MCP streamable HTTP endpoint (stateless) |
| `GET /healthz` | `200 ok`, unauthenticated, no other information; use it for load balancer and container health checks |
| anything else | 404 |

## Container

The repository has a `Dockerfile` for serve mode. v1 does not publish an
image: build your own.

```sh
docker build -t review-mcp:local \
  --build-arg VERSION=1.0.0-rc.1 --build-arg COMMIT=$(git rev-parse HEAD) .
```

The image is a static binary on `gcr.io/distroless/static-debian12:nonroot`
(runs as a non-root user, no shell), and starts
`review-mcp serve --listen 0.0.0.0:8787`. That address is not loopback, so
**the container will not start without either TLS files or
`REVIEW_MCP_SERVE_ALLOW_INSECURE_HTTP=true`**. Choose one:

```sh
# TLS in the container: mount the certificate and key
docker run --rm -p 8787:8787 \
  -v /etc/review-mcp:/tls:ro \
  -e REVIEW_MCP_SERVE_TLS_CERT=/tls/tls.crt \
  -e REVIEW_MCP_SERVE_TLS_KEY=/tls/tls.key \
  -e REVIEW_MCP_LLM_BASE_URL=https://llm.example.com/v1 \
  -e REVIEW_MCP_LLM_MODEL=your-model-name \
  -e REVIEW_MCP_LLM_CONTEXT_WINDOW=32000 \
  -e REVIEW_MCP_GITEA_BASE_URL=https://your-gitea.example \
  review-mcp:local

# or plain HTTP behind a TLS-terminating proxy; publish the port to the
# proxy only (here: loopback of the Docker host)
docker run --rm -p 127.0.0.1:8787:8787 \
  -e REVIEW_MCP_SERVE_ALLOW_INSECURE_HTTP=true \
  -e REVIEW_MCP_LLM_BASE_URL=https://llm.example.com/v1 \
  -e REVIEW_MCP_LLM_MODEL=your-model-name \
  -e REVIEW_MCP_LLM_CONTEXT_WINDOW=32000 \
  -e REVIEW_MCP_GITEA_BASE_URL=https://your-gitea.example \
  review-mcp:local
```

The key files must be readable by the image's non-root user. A private CA for
your provider needs `REVIEW_MCP_GITEA_CA_CERT` (or the Bitbucket one) pointing
at a mounted PEM file. Because the image has no shell, use `GET /healthz` from
the orchestrator for health checks.

## Long calls

In stdio mode a slow `pr_review` or `pr_ask` answers with a `job_id` after
`wait_seconds` and finishes in the background
([Slow endpoints](review.md#slow-endpoints)). Serve mode does not do this:

- every call runs in its own request, start to finish;
- the `wait_seconds` argument and `llm.wait_seconds` are ignored;
- there is no `job_result` tool.

A background job would outlive its request, and with it the credentials the
request carried. Serve mode never keeps those beyond the request. Give your
client a tool timeout that fits your model, and set `llm.timeout_seconds` to
match. A client that reports `-32001 Request timed out` gave up on its own
timeout: raise that timeout in the client.

A large pull request is reviewed in several parts, one model call after
another ([Large pull requests](review.md#large-pull-requests)), and in serve
mode all of them run inside the one request. A review in N parts takes about N
times as long as a review in one call. For large pull requests, raise the
client's tool timeout accordingly, or set `review.max_chunks = 1`
(`REVIEW_MCP_REVIEW_MAX_CHUNKS=1`) on the server to review in one call, as
before, with the files that do not fit listed as omitted.

## Errors specific to serve mode

`credentials_missing`, `server_busy`, the malformed-header sentence and the
401 and 403 responses are in
[Troubleshooting: every error sentence](troubleshooting.md#every-error-sentence).
