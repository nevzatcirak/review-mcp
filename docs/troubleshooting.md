# Troubleshooting

When something does not work, first check whether review-mcp can reach your
pull request at all. The `diag` command does exactly that, without involving
an LLM or an MCP client.

## Check connectivity with `diag pr`

Set the same environment you give the MCP server (provider base URL and token;
see [Getting started](getting-started.md)), then run:

```sh
review-mcp diag pr https://your-gitea.example/octo/demo/pulls/7
review-mcp diag pr https://bitbucket.example.com/projects/PROJ/repos/demo/pull-requests/7
```

The command resolves the provider from the URL, fetches the pull request, its
commit messages and its diff, and prints one JSON report on stdout. Logs and
errors go to stderr; the exit code is 0 on success, 1 on a failure and 2 on a
usage error. `diag` never prints a token, an `Authorization` header, the PR
description or commit bodies.

```json
{
  "kind": "gitea",
  "ref": { "namespace": "octo", "repo": "demo", "number": 7, "url": "https://your-gitea.example/octo/demo/pulls/7" },
  "title": "Add feature",
  "source_branch": "feature",
  "target_branch": "main",
  "head_sha": "headsha",
  "base_sha": "mergesha",
  "base_strategy": "gitea:merge_base",
  "commit_count": 2,
  "commits": ["First commit", "Second commit"],
  "files": [
    { "path": "src/renamed.go", "old_path": "src/old_name.go", "type": "renamed", "additions": 1, "deletions": 1,
      "patch_bytes": 52, "base_status": "full", "head_status": "full", "binary": false }
  ],
  "skipped": [ { "path": "assets/logo.png", "reason": "binary" } ],
  "totals": { "files": 1, "skipped": 1, "additions": 1, "deletions": 1, "patch_bytes": 52 },
  "elapsed_ms": 41
}
```

Compare `files`, their `type`, `old_path` and the `additions`/`deletions`
counts with the provider's web UI. To look at one file's patch, add
`--show-patch <path>` (the `path` as listed in `files`); the hunk-only patch
is printed after the JSON, preceded by a line `--- patch: <path> ---`:

```sh
review-mcp diag pr <PR_URL> --show-patch src/renamed.go
```

### `base_strategy`

It tells you which revision the diff was computed against (`base_sha`).

| Value | Meaning |
|---|---|
| `gitea:merge_base` | Gitea reported the PR's merge base; this is the normal case. |
| `gitea:base_sha` | Gitea reported no merge base, so the target branch's tip was used. |
| `bbs:merge_base_endpoint` | Bitbucket Server reported the merge base; this is the normal case. |
| `bbs:ancestor_walk` | The merge-base endpoint answered 404, so the common ancestor was found by walking the target branch history. |

### `skipped` reasons

A skipped file is not part of `files` and is not reviewed.

| Reason | Meaning |
|---|---|
| `binary` | The file is binary. |
| `file_limit` | More files than `diff.max_files_full_content` (Bitbucket Server cannot build a patch without file contents). |
| `size_limit` | A side of the file is larger than `diff.max_file_bytes` (Bitbucket Server). |
| `fetch_failed` | The file's contents could not be fetched or it is listed by only one of Gitea's two sources. |
| `filtered` | An ignore rule excluded the file (`diag pr` applies none; `diag diff` applies the file filter and says which rule matched). |

In `files`, `base_status` and `head_status` say whether the full content of
each side was fetched: `full`, `not_fetched_file_limit`,
`not_fetched_size_limit`, `fetch_failed`, or `not_applicable` (the base side
of an added file, the head side of a deleted one). On Gitea a file without
full content keeps its patch.

### Posting a test comment

```sh
review-mcp diag comment <PR_URL> --body "review-mcp connectivity check"
```

Posts one PR-level comment with exactly that text and prints
`{"id": ..., "url": ...}`. `--body` is required. This needs write access (see
the token scopes below).

### Reading comment threads

```sh
review-mcp diag comments <PR_URL> [--include-resolved]
```

Prints the result of the `pr_comments` tool as JSON: `pr`, `threads` and
`truncated`. General threads come first, then inline threads by path and line.
Resolved threads are hidden unless `--include-resolved` is given;
`truncated.resolved_hidden` says how many were hidden. A thread whose resolved
state the provider does not report (`"resolved": null`) is always shown. At most
100 threads are listed (the oldest root comments are dropped first) and each
body is cut at 4000 characters; `truncated` counts both. Unlike the other `diag`
output, this prints comment text and author names, which are untrusted content
written by third parties; they are never written to the logs.

### Replying to a comment

```sh
review-mcp diag reply <PR_URL> --comment-id <ID> --body <TEXT>
```

Replies to the comment `--comment-id` (an id from `diag comments`) and prints
`{"id": ..., "url": ..., "in_thread": ...}`. Both flags are required and the
body is posted verbatim. On Bitbucket Server the reply lands inside the thread
(`"in_thread": true`). Gitea has no usable reply endpoint, so the command posts
a new PR-level comment that starts with a quote line such as
`> Replying to @alice on src/app.go:10`, and `"in_thread": false` is expected
there. This needs write access (see the token scopes below).

## Read the prepared diff with `diag diff`

```sh
review-mcp diag diff https://your-gitea.example/octo/demo/pulls/7
review-mcp diag diff <PR_URL> --mode numbered --prompt-tokens 2500
```

`diag diff` runs the same steps a review does before it calls the LLM: fetch
the pull request, drop the files the file filter excludes, and fit the rest
into the token budget. It prints a JSON header, then the line
`--- prepared diff ---`, then the exact diff text, byte for byte, so you can
see what the model would be given. Everything goes to stdout; the diff text is
never written to the logs. Exit codes are as for `diag pr` (0, 1, 2).

Flags (they may come before or after the URL):

| Flag | Default | Meaning |
|---|---|---|
| `--mode plain\|numbered` | `plain` | `plain` is the format for questions; `numbered` is the line-numbered format for reviews (`__new hunk__` / `__old hunk__` blocks). |
| `--prompt-tokens N` | `2056` | The estimated size of the prompt around the diff. The default is the measured maximum of the review prompts (all fields on, a non-English language, extra instructions; see [Reviewing pull requests](review.md#prompt-tokens)), without the PR's own title and description. Raise it to see how a longer prompt squeezes the diff; `diag review --dry-run` reports the exact figure for a PR. |

```json
{
  "budget": { "context_window": 4096, "soft_limit": 1596, "hard_limit": 2096, "prompt_tokens": 1000, "factor": 0.3 },
  "fast_path": false,
  "tokens": 1454,
  "included": ["server/app.go", "server/util.go", "server/fresh.go"],
  "omitted": { "added": [], "modified": ["server/renamed.go", "web/index.ts"], "deleted": ["server/gone.go"] },
  "clipped": [],
  "skipped": [],
  "filtered": [
    { "path": "package-lock.json", "reason": "lockfile_or_minified" },
    { "path": "vendor/lib/dep.go", "reason": "ignore_glob" },
    { "path": "assets/logo.png", "reason": "bad_extension" }
  ],
  "elapsed_ms": 32
}
```

### Fast path or compressed path

`fast_path: true` means the whole diff, with extra context lines around each
hunk (`diff.extra_lines_before` / `diff.extra_lines_after`), fits under
`budget.soft_limit`. Nothing is omitted and every file is in `included`.

`fast_path: false` means it did not fit. The diff is then rebuilt without extra
context, deleted files lose their patches, files are grouped by language
(largest group first) and, within a group, sorted largest first. Files are
admitted until the soft limit is reached; the rest are listed in `omitted` and,
when there is room, named in "Additional ... files (insufficient token budget
to process)" sections at the end of the text.

`budget.soft_limit` and `budget.hard_limit` are the room for diff content:
the context window minus the reserve for the answer minus `prompt_tokens`. See
[Getting started](getting-started.md) for how the reserves are derived.
`tokens` is the estimated size of the printed text, including the safety
factor.

### What each list means

Every changed file appears in exactly one of these lists:

| List | Meaning |
|---|---|
| `included` | The file's full diff is in the text, in output order. |
| `omitted.added`, `omitted.modified`, `omitted.deleted` | The file's diff is not in the text because of the budget (renamed files count as modified). On the compressed path every deleted file is listed here, because its patch is dropped on purpose. |
| `clipped` | The file is in the text but cut short with `...(truncated)` (see `large_patch_policy` below). |
| `skipped` | The file was not processed for a reason other than the budget (table below). |
| `filtered` | The file filter excluded it before it was fetched. `reason` says which rule matched. |

`filtered` and `skipped` never overlap: `filtered` holds the files the provider
skipped as `filtered`, with the filter's own reason, and `skipped` holds all
other skips.

Filter reasons (`filtered`):

| Reason | Meaning |
|---|---|
| `lockfile_or_minified` | A known lockfile (`package-lock.json`, `go.sum`, `Cargo.lock` and others) or a minified or source-map file (`.min.js`, `.min.css`, `.js.map`). |
| `bad_extension` | The extension is on the built-in list of non-source file types (images, archives, fonts and so on). |
| `generated:<framework>` | The path matches a generated-code pattern of a framework listed in `diff.ignore_generated_frameworks`, for example `generated:protobuf`. |
| `ignore_glob` | The path matches an `ignore.glob` pattern (the default is `vendor/**`). |
| `ignore_regex` | The path matches an `ignore.regex` pattern. |
| `empty_path` | The provider reported a file without a path. |

Skip reasons (`skipped`): the provider reasons from the `diag pr` table above
(`binary`, `file_limit`, `size_limit`, `fetch_failed`), plus two added when the
diff is assembled:

| Reason | Meaning |
|---|---|
| `empty_diff` | The file renders to nothing, for example a pure rename or a permission change without hunks. |
| `unparseable_patch` | The file's patch is not a unified diff that review-mcp can read. |

### `ignore.glob` is not Python's `fnmatch`

Patterns are doublestar globs with one rule that depends on whether the
pattern contains a `/`:

- A pattern **without `/`** matches file names at any depth, as if it had an
  implicit `**/` prefix. `*.golden` excludes `x.golden` and `a/b/x.golden`, so
  patterns copied from the upstream tool, such as `*.min.js`, work as expected.
- A pattern **with `/`** is matched against the full path from the repository
  root. `*` stays within one path segment and `**` spans directories.
  `docs/*.md` excludes `docs/a.md` but not `x/docs/a.md` or `docs/sub/a.md`;
  `vendor/**` excludes everything under a top-level `vendor/`.

Check the result with `diag diff`: the pattern's files must
show up under `filtered` with reason `ignore_glob`.

### "The pull request diff does not fit"

```text
the pull request diff does not fit the configured context window; raise llm.context_window (REVIEW_MCP_LLM_CONTEXT_WINDOW) or narrow the pull request
```

`diag diff` prints this sentence and exits 1 when the budget has no room for
diff content (the soft limit is zero or negative: the window is too small for
the reserve plus `--prompt-tokens`), or when not even one file fits and
`diff.large_patch_policy` is `skip`. Raise `llm.context_window` to the real
size of your model's window, lower `llm.max_output_tokens`, or split the pull
request.

### `diff.large_patch_policy`

It applies only when no file at all fits the budget:

| Value | Result |
|---|---|
| `clip` (default) | The largest file of the top language group is cut to fit the soft limit and ends with `...(truncated)`. It is listed in `clipped`. |
| `skip` | Nothing is included and `diag diff` reports the "does not fit" sentence above. |

If at least one file fits, the policy has no effect: the files that do not fit
are listed in `omitted`.

## Review a pull request with `diag review`

```sh
review-mcp diag review <PR_URL> --dry-run
review-mcp diag review <PR_URL>
```

`--dry-run` runs everything a review does up to the LLM call and prints a JSON
report with the prompt, diff and request token estimates, the budget and the
coverage; the model is not called. Use it to tune `llm.context_window` and the
`ignore.*` rules. Without `--dry-run` it runs the full review and prints the
markdown the `pr_review` tool returns; `--publish` also posts it, and
`--show-prompt` prints the rendered prompts after the output (never to the
log). See [Reviewing pull requests](review.md) for how to read the result.

### LLM and review errors

A failed review reports one of these fixed sentences. The text never contains
a response body, a prompt or the key. A status code and a hint naming the
setting to check may follow.

| Sentence | Class | What to check |
|---|---|---|
| the LLM endpoint rejected the credentials | `llm_auth` (HTTP 401/403) | `REVIEW_MCP_LLM_API_KEY`. |
| the LLM endpoint or model was not found | `llm_not_found` (HTTP 404) | `llm.base_url` (it must end where `/chat/completions` is appended) and `llm.model`. |
| the LLM endpoint rate-limited the request | `llm_rate_limited` (HTTP 429) | Retried up to `llm.max_retries` times; wait, or raise it. |
| the request is too long for the model's context window | `llm_context_too_long` | `llm.context_window` is larger than the model really accepts; lower it. |
| the LLM endpoint rejected the request | `llm_bad_request` | Often an unsupported sampling setting; the hint names the keys you set (`llm.temperature`, `llm.seed`, `llm.reasoning_effort`, `llm.max_output_tokens`). Unset them. |
| the LLM endpoint reported an internal error | `llm_upstream` (HTTP 5xx) | The endpoint failed; see its logs. Retried like 429. |
| the LLM request timed out | `llm_timeout` | Raise `llm.timeout_seconds`; large reviews on slow models take minutes. |
| could not complete the request to the LLM endpoint | `llm_transport` | Network, DNS, TLS or proxy problem between you and the endpoint. |
| the LLM endpoint sent an unexpected response | `llm_protocol` | The endpoint is not OpenAI-compatible at `llm.base_url`, or it answered with an empty message. |
| the pull request diff does not fit the configured context window | `review_does_not_fit` | Raise `llm.context_window`, or narrow the PR. Nothing was sent to the model. |
| the model's answer could not be parsed as a review, also after one retry | `review_unparseable` | Try again, or use a model that follows YAML output instructions. |
| output_language must be a locale code such as en-US or tr-TR / max_findings must be an integer from 1 to 20 | (argument) | Fix the argument; nothing was sent anywhere. |

If the configuration is invalid, `pr_review` returns "review-mcp configuration
is invalid; call server_info for the list of problems" and sends nothing to
the provider or the LLM.

## Error messages

Every failure from a provider is reported as one of these fixed sentences.
The text never contains a response body, a token or a query string. A status
code and a short hint (a config key or a short phrase) may follow the
sentence, for example `authentication failed: check the token and its scopes (HTTP 401)`.

| Sentence | Class | Meaning and what to check |
|---|---|---|
| the pull request URL does not match any configured provider | `url_not_configured` | The URL's scheme, host, port or path prefix matches no configured base URL (or Gitea `web_url`). Compare it with `REVIEW_MCP_GITEA_BASE_URL` / `REVIEW_MCP_BITBUCKET_SERVER_BASE_URL`, including `http` vs `https`, the port, and a Bitbucket context path. No request was sent. |
| the URL matches a configured provider but is not a pull request URL | `url_malformed` | Use the PR page URL: `.../{owner}/{repo}/pulls/{n}` (Gitea) or `.../projects/{KEY}/repos/{slug}/pull-requests/{id}` (Bitbucket Server; `/users/{user}/repos/...` for personal repositories). Do not put credentials in the URL. |
| authentication failed: check the token and its scopes | `auth` (HTTP 401/403) | The token is wrong, expired, or lacks a scope; see the token scopes below. Also check that the token belongs to the provider whose URL you used. |
| the requested resource was not found | `not_found` (HTTP 404) | The repository or PR does not exist, or the token's user cannot see it (many servers answer 404 for a hidden repository). On Bitbucket Server, a wrong context path also gives 404. |
| the server rate-limited the request | `rate_limited` (HTTP 429) | Wait and retry; check any rate limits or a reverse proxy in front of the server. |
| the server reported an internal error | `upstream` (HTTP 5xx) | The provider (or a proxy in front of it) failed. Look at the server's own logs. |
| a response exceeded its size limit | `too_large` | The hint names the limit: `diff.max_diff_bytes` or `diff.max_file_bytes`; raise it, or review a smaller PR. The hint `(json response limit)` is a fixed 10 MiB safety cap on JSON responses and is not configurable; if you hit it, please report it. |
| the server version is not supported | `unsupported_version` | Bitbucket Server / Data Center 7.0 or later is required. |
| could not complete the request to the server | `transport` | The hint says why: `DNS lookup failed` (check the host name), `timeout` (check the network, proxy and server load), `TLS verification failed` (see CA certificates below), `connection failed` (check the port, firewall and scheme). |
| the server sent an unexpected response | `protocol` | The server answered, but not in the expected shape. Check that the base URL points at the real API host and context path, not at a login page or a proxy; a redirect to another host is refused. A 4xx other than 401/403/404/429 also lands here. |

Anything else is reported as `unexpected error; rerun with
REVIEW_MCP_LOG_LEVEL=debug for details`, because arbitrary error text could
contain URLs with query strings.

If the configuration itself is invalid, `diag` prints every problem, one per
line, exits 1 and makes no network request.

## Token scopes

Scopes are named differently across versions; grant the least that works.

- **Gitea:** read access to the repository, plus write access to issues and
  pull requests. The write part is needed only for publishing (`publish`) and
  `diag comment`; reading a PR needs only read access.
- **Bitbucket Server / Data Center:** an HTTP access token with repository
  read permission, plus write permission only for comments. The token is sent
  as `Authorization: Bearer ...`; basic authentication is not supported.

Pass tokens only through the environment (`REVIEW_MCP_GITEA_TOKEN`,
`REVIEW_MCP_BITBUCKET_SERVER_TOKEN`); never put them in a URL or a config file
that is checked in.

## Base URL notes

- **Bitbucket Server context path.** `REVIEW_MCP_BITBUCKET_SERVER_BASE_URL`
  must include the context path if the server has one, for example
  `https://bitbucket.example.com/bitbucket`. The context path always comes
  from the configuration, never from the PR URL. If `diag` reports
  `url_not_configured` for a URL that looks right, or `not_found` for a PR
  that exists, check this first.
- **Gitea `web_url`.** `REVIEW_MCP_GITEA_BASE_URL` is where the API is
  reached. If users open PRs under a different public address (for example a
  reverse proxy), set `REVIEW_MCP_GITEA_WEB_URL` to that address: PR URLs
  under it are accepted too. API requests still go to the base URL.
- **Scheme, host and port must match exactly** (case-insensitive host; default
  ports 80/443 are implied). `https://your-gitea.example.evil.example` or a
  sibling path such as `/bitbucket-old` does not match `/bitbucket`.
- **CA certificates.** For a server with a private CA, set
  `REVIEW_MCP_GITEA_CA_CERT` or `REVIEW_MCP_BITBUCKET_SERVER_CA_CERT` to the
  path of a PEM bundle; it is used in addition to the system roots. A
  `transport` error with the hint `TLS verification failed` usually means the
  CA is missing or the certificate does not cover the host name.
- **`insecure_skip_verify`.** `REVIEW_MCP_GITEA_INSECURE_SKIP_VERIFY=true` (and
  the Bitbucket Server equivalent) turns off certificate verification. Use it
  only to confirm that TLS is the problem on a throwaway setup; prefer a CA
  certificate. review-mcp warns at startup when it is set.

## Debug logging

```sh
REVIEW_MCP_LOG_LEVEL=debug review-mcp diag pr <PR_URL>
```

Logs go to stderr only (stdout belongs to the MCP protocol and, for `diag`, to
the report). At debug level each request is logged with its method, the URL
with query values replaced by `REDACTED`, the status and the duration, and the
provider notes things such as a failed version probe or a file present in only
one source.

Never logged, at any level: tokens and other secrets (including the LLM API
key), `Authorization` headers, request and response bodies, tool arguments and
results, diff content, PR titles and descriptions, and prompts or model
responses. Still, skim a log before pasting it into a public issue.
