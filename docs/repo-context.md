# Repository context

A diff shows what changed, not who depends on it. With **repository context**
turned on, `pr_review`, `pr_ask` and `pr_improve` also show the model where
the symbols that the pull request changes are used in the rest of the project:
the callers of a changed function, the other implementations of a changed
interface. `pr_improve` gives the block to its suggestion calls only, not to the
self-review calls (see [Suggesting code changes](improve.md#what-is-sent-to-the-llm)).

It is **opt-in** (off by default) and **stdio only**: it is a startup error in
[serve mode](serve.md#repository-context-is-not-available). It needs the
system `git`. Nothing in this guide is needed for a review without it. On top
of it, the model can be allowed to read the head commit itself:
[Repository tools](#repository-tools).

## What it does

For each review (or answer), after the diff is fetched:

1. **Symbols.** review-mcp reads the names the diff changes: the function or
   class named in a hunk header, and definitions on added or removed lines
   (`func`, `fun`, `fn`, `def`, `function`, `class`, `interface`, `type`,
   `struct`, `enum`, `trait`, method signatures, exported constants). Names
   shorter than 4 characters, keywords and ubiquitous names (`main`, `init`,
   `setup`, `toString`, Python dunders) are dropped; prose and data files give
   none. At most `context.repo.max_symbols` are kept, ranked: removed or
   renamed definitions first (the change most likely to break callers), then
   changed signatures, then other changed definitions that already existed,
   then new definitions, and last the symbols of test files.
2. **Fetch.** The pull request head is fetched into a local cache (see
   [What is cached](#what-is-cached-and-where)), once per review, and its
   commit must equal the head commit the provider reported.
3. **Uses.** For each symbol, `git grep -w -F` searches the fetched commit.
   The pull request's own files, ignored and generated files (the same rules
   as the review) and binary files are left out. At most
   `context.repo.max_hits_per_symbol` uses are kept per symbol, preferring
   distinct files and files in the language of the definition, each with three
   lines of context on both sides.
4. **The block.** The uses become one block of the prompt, after the
   discussion block and before the diff:

   ````text
   Related code that uses symbols changed in this pull request (read-only context; it may be incomplete). Entries marked as changed in this pull request are reviewed in another part or not at all. Use this code to judge the effect of the change on callers and implementations. Do not report issues in it unless the pull request causes them.
   ```
   [pkg/client.go:42] uses Fetch
   <the line and its context>

   [cmd/main.go:17] uses Fetch
   <the line and its context>
   ```
   ````

   The snippets are repository text, so they are treated like the comments of
   the [discussion](review.md#discussion-awareness): sanitized, inside a fence
   they cannot close, and never written to the logs.

If the diff defines no symbol to look for, nothing is fetched.

## Enabling it

| Setting | Env | Default | Effect |
|---|---|---|---|
| `context.repo.enabled` | `REVIEW_MCP_CONTEXT_REPO_ENABLED` | `false` | turns repository context on (stdio only) |
| `context.repo.cache_dir` | `REVIEW_MCP_CONTEXT_REPO_CACHE_DIR` | OS user cache dir + `review-mcp/repos` | absolute path of the cache |
| `context.repo.idle_days` | `REVIEW_MCP_CONTEXT_REPO_IDLE_DAYS` | `7` | a repository unused for longer is deleted, 1 to 365 |
| `context.repo.max_cache_mb` | `REVIEW_MCP_CONTEXT_REPO_MAX_CACHE_MB` | `2048` | cache size; least recently used repositories go first, 1 to 1048576 |
| `context.repo.max_repo_mb` | `REVIEW_MCP_CONTEXT_REPO_MAX_REPO_MB` | `500` | one repository, measured after the fetch; at most `max_cache_mb` |
| `context.repo.fetch_timeout_seconds` | `REVIEW_MCP_CONTEXT_REPO_FETCH_TIMEOUT_SECONDS` | `60` | the wait for the repository lock plus the fetch (including an authentication retry), 1 to 600 |
| `context.repo.max_symbols` | `REVIEW_MCP_CONTEXT_REPO_MAX_SYMBOLS` | `20` | symbols searched, 1 to 50 |
| `context.repo.max_hits_per_symbol` | `REVIEW_MCP_CONTEXT_REPO_MAX_HITS_PER_SYMBOL` | `5` | uses kept per symbol, 1 to 20 |
| `context.repo.max_tokens` | `REVIEW_MCP_CONTEXT_REPO_MAX_TOKENS` | `2000` | token budget of the block, 200 to 16000 |

In an MCP client configuration:

```json
"environment": {
  "REVIEW_MCP_CONTEXT_REPO_ENABLED": "true"
}
```

`server_info` shows `context.repo.enabled` as `enabled, git <version>` or
`enabled, unavailable: <reason>`, so you can check that `git` works. To try it
once without changing the configuration, use
[`diag review --repo-context=on`](#measuring-the-gain).

### The budget

`context.repo.max_tokens` is counted inside the prompt tokens and is clipped by
whole entries (the best-ranked uses first); a note says how many uses were
left out. **The diff wins over the block.**

- With `review.max_chunks = 1`, and for `pr_ask`, the block gets only the room
  the diff leaves in the context window. If there is none, the block is left
  out with the reason `budget` and the review is exactly the one without
  context.
- With `review.max_chunks > 1` (the default), `context.repo.max_tokens` is
  reserved up front, when the head was fetched and the diff has symbols, for
  the one call and for every part alike. If the reservation pushes a file out
  of the call, that file goes to a further part: nothing is lost, and the
  context is kept. A review that fits one call without context can therefore
  become **two parts** with it. The extra part is counted in
  `coverage.model_calls` and takes one more model call of time. If the window
  is too small to reserve anything, the reservation is dropped and the review
  is the one without context.

### Reviews in parts

Each part gets the symbols of **its own files** only and its own
`context.repo.max_tokens` block; the head is fetched once for the whole review.
A part does not search its own files, but it does search the other files of the
pull request, because a caller in a file that another part reviews is exactly
what a part cannot see. Such a use is read from the head commit (the new
version of that file) and is marked on its first line:

```text
[src/client.go:42] uses Fetch (changed in this pull request; reviewed in part 3)
[src/other.go:9] uses Fetch (changed in this pull request; not reviewed)
```

"Not reviewed" means no part carries the file: it was left out by the budget
or `review.max_chunks`, was too large for a part, or the provider skipped it.
In a review in one call every file of the pull request is excluded, as it is in
the diff already.

## What is cached and where

The cache is a directory of bare repositories (no working trees):

```text
<cache_dir>/CACHEDIR.TAG                  marks the directory as a review-mcp cache
<cache_dir>/.home/                        an empty HOME for git (mode 0700)
<cache_dir>/<host>/<base>/<namespace>/<repo>/
    git/                                  the bare repository
    last-used                             touched on every use
    .lock                                 held while it is fetched
```

`<host>` is the lower-case host name (`host_port` for an explicit port) and
`<base>` the base URL's path (`_` when there is none), so two instances on one
host never share an entry. The default `cache_dir` is the OS user cache
directory plus `review-mcp/repos`. It is created with mode 0700. A non-empty
directory without review-mcp's `CACHEDIR.TAG` is refused, so a wrong
`cache_dir` cannot make review-mcp delete foreign files.

Only the pull request head is fetched, with `--depth=1 --no-tags` and
`--filter=blob:limit=1m`: **files above 1 MiB are not in the cache**, and a
search never fetches them (it counts them as skipped). The cache is swept at
the start of every use: repositories idle for more than `idle_days` go, then
the least recently used until the cache fits `max_cache_mb`. Several MCP
client sessions may share one cache.

### Clearing it

```sh
review-mcp diag cache            # list the repositories, their sizes and last use
review-mcp diag cache --prune    # run the idle and size sweeps now
```

`diag cache` works whether or not repository context is enabled. To clear the
cache completely, delete the `cache_dir` directory. It holds nothing but
re-fetchable copies. Entries of the pre-release layout
(`<host>/<namespace>/<repo>`, without the `<base>` level) are neither listed
nor swept; delete them by hand.

### Guidelines and the file tree from the cache

`pr_review` and `pr_improve` read the project's guidelines file and the listing
of the touched directories at the target branch
([Project guidelines and file tree](review.md#project-guidelines-and-file-tree)).
When this run has already fetched the head and the cache holds the base commit,
they are read from the cache with offline `git` commands only (no credential, no
network, no filters or hooks), with the same rules and notes as through the API;
otherwise through the provider's API. The base commit is never fetched for this.
The fetch of the head is shallow, so in practice the API answers most of the
time.

## Repository tools

With **repository tools** the model can read the pull request's head commit
itself, while it answers, instead of only seeing the block of uses. It asks for
what it needs through the endpoint's **tool calling**, and review-mcp answers
from the commit that repository context already fetched. It is **opt-in** and
**off by default** (V-8, X-43): nothing in this section happens until you set
`context.repo.max_tool_rounds` above 0.

It works in `pr_review` (a review in one call, full or incremental) and in
`pr_ask`. `pr_describe` and `pr_improve` do not use it yet.

### Enabling it

| Setting | Env | Default | Effect |
|---|---|---|---|
| `context.repo.max_tool_rounds` | `REVIEW_MCP_CONTEXT_REPO_MAX_TOOL_ROUNDS` | `0` | the most rounds in which the model may call tools, 0 to 6; 0 turns the tools off |
| `context.repo.max_tool_tokens` | `REVIEW_MCP_CONTEXT_REPO_MAX_TOOL_TOKENS` | `6000` | token budget of all tool results of one call together, 500 to 40,000 |

Both are operator keys: a [repository config](repo-config.md) cannot set or
raise them (they are not in its allow-list), so a pull request cannot turn the
tools on for itself. They need `context.repo.enabled` (stdio only, as
repository context): with rounds above 0 and repository context off nothing is
read, the result says `repo_context_off`, and the server logs a startup warning
that `server_info` shows under `warnings`. `server_info` also shows both keys,
and, for the primary model and each fallback, whether this process has seen it
accept or refuse tool calls (`repo_tools`: `not known yet`, `accepted` or
`refused`).

```json
"environment": {
  "REVIEW_MCP_CONTEXT_REPO_ENABLED": "true",
  "REVIEW_MCP_CONTEXT_REPO_MAX_TOOL_ROUNDS": "3"
}
```

### What the model can read

Three tools, all on the **head commit** of the pull request (the code as it is
after the change). The base commit is never fetched for this.

| Tool | Arguments | Result |
|---|---|---|
| `read_file` | `path`, optional `start_line`, `end_line` (1-based) | the file with line numbers like the diff, at most 400 lines per call (the first 400 without a range) |
| `list_dir` | `path` (`""` is the root) | the entries of the directory, directories with a trailing `/`, at most 200 and then "and N more" |
| `search` | `text` (2 to 200 characters), optional `path` (a directory) | a fixed-string search (`git grep -F`, case-sensitive, not a pattern): at most 50 matches, each with one line around it |

`search` leaves out the files the review ignores (`ignore.glob`, generated,
lock and minified files, binary files), as the repository-context search does;
`read_file` and `list_dir` read any allowed path the model names.

A path must be relative and `/`-separated, at most 4,096 bytes, valid UTF-8
without control characters, and must have no `..` or `.` segment, no leading
`/`, no `\`, no empty segment and no `.git` segment (in any case). A path
outside these rules is answered "path not allowed" and **nothing is read**; a
directory may end with one `/`. Paths are only ever resolved as
`<head sha>:<path>` through git, never against the file system.

Binary files, files the partial clone lacks (blobs over 1 MiB are not fetched)
and files over `diff.max_file_bytes` give a fixed answer ("not available:
binary", "not available: not fetched", "not available: too large"). Nothing is
fetched to read them.

### How the loop works

1. The system prompt gets one fixed instruction block (absent without tools):
   the tools read the head commit, use them only when the diff and the
   repository context are not enough, at most N rounds and four calls in each,
   and the final answer carries no tool call. `pr_review` answers in the usual
   YAML, `pr_ask` in plain text.
2. Each **round** is one model call. The calls of the answer (at most four;
   further ones are answered "too many calls in one round") run in order and
   their results go back to the model.
3. After `max_tool_rounds` rounds, when the budget is used up, or when the next
   request would not fit, one last call with tool choice `none` asks for the
   answer.
4. The final text goes through the normal parsing. A `pr_review` answer that
   cannot be parsed is re-asked once as a plain call without tools; that is not
   a round. `pr_ask` has no re-ask.

**The diff wins.** The loop never shortens the diff. Every request is measured
before it is sent and must fit the context window beside the output reserve
with the diff whole; a result gets only the room the request has left. If the
tools' instruction and definitions do not fit beside the diff, the run is
single-pass.

Every tool result is fenced as **untrusted data** under a fixed sentence, cut
at a line boundary at 2,000 tokens with a marker, and counted against
`context.repo.max_tool_tokens`. Text in a file, such as "ignore previous
instructions", is data and does not change the task. Logs carry the tool, the
status, the token count and the round, and the repository path of a read; never
file content or search text.

Nothing is built, run or loaded as code (R-1), and nothing touches the network:
the reads run on the cache, offline, with the isolation of repository context
(no credential, no lazy fetch, no hooks, no repository configuration read as
code).

### When the tools are not used

The run is then single-pass, as without the feature, never an error. The
result says why in `coverage.repo_context.tools.reason`:

| Status | Reason | Meaning |
|---|---|---|
| `off` | `off` | `context.repo.max_tool_rounds` is 0. |
| `off` | `repo_context_off` | The rounds are above 0 but `context.repo.enabled` is off (also a startup warning). |
| `skipped` | `repo_context_unavailable` | Repository context was skipped (see the [reasons](#reading-the-result)) or the head could not be read. |
| `skipped` | `review_in_parts` | The review was made in parts; a note says so. |
| `skipped` | `endpoint_no_tools` | The endpoint or model does not accept tool calls; a note says so. |
| `skipped` | `no_room` | The tools' block and definitions do not fit beside the whole diff, or the single-pass diff is trimmed already; a note says so. |
| `skipped` | `no_model_call` | The run made no model call (nothing to review, nothing changed since the last review, a publish of supplied findings). |
| `skipped` | `conversation_failed` | A model call failed while the model was reading the repository in a way that ends the rounds, and the answer came single-pass; a note says so. |

A run in which tools were allowed but the model made no call is `not_used`.

**An endpoint without tool calling.** There is no probe: the first request of a
process to an endpoint and model carries the tools. If the endpoint refuses them
(HTTP 400 or 422 whose text mentions tools or functions), or answers that it
calls a tool without a call that can be read, the run continues single-pass
with the note "The LLM endpoint does not accept tool calls, so the review used
the diff and repository context without reading further files." and the
process remembers "no tools" for that endpoint and model: later runs there send
no tool request. A fallback model is treated the same way, one by one. Restart
the server after you switch the endpoint to a model that supports tools.

A conversation never changes model in the middle. If the primary fails before
the first turn, the run starts on the fallback; if it fails later, the
conversation starts again once on the fallback, or the run ends single-pass
with a note.

### Reading the tools result

`coverage.repo_context.tools` is always present in `pr_review` and `pr_ask`:

```json
{"status": "used", "reason": "", "rounds": 2,
 "calls": [{"tool": "read_file", "target": "store/lookup.go", "status": "ok", "tokens": 210},
           {"tool": "search", "target": "nil, nil", "status": "ok", "tokens": 340}],
 "tokens": 550}
```

- `rounds` counts the rounds whose calls ran; `metadata.tool_rounds` is the
  same number. **Every round is a model call** counted in `metadata.llm_calls`.
- `target` is the path or the search text, cut to 200 characters without
  control characters.
- `tokens` is the estimate of the results, counted against
  `context.repo.max_tool_tokens`.

| Call status | Meaning |
|---|---|
| `ok` | The call ran. |
| `not_allowed` | The path broke the path rules; nothing was read. |
| `not_available` | Binary, not fetched, not found, not a directory, a start past the end of the file, or unreadable. |
| `too_large` | The file is over `diff.max_file_bytes`. |
| `budget_exhausted` | The result did not fit the rest of `context.repo.max_tool_tokens` or the room beside the diff; not run. The loop then ends, with the note "The repository tool budget was used up after N calls." |
| `invalid_arguments` | Not a JSON object of at most 4 KiB, a missing or wrong argument, or a tool that does not exist. |
| `too_many_calls` | Beyond four calls in one round; not run. |

The notes are listed in
[Troubleshooting](troubleshooting.md#repository-tool-notes).

### What it costs

Each round is one model call through the same rate limiter as any call
(`llm.requests_per_minute`, `llm.max_concurrent_requests`, the cool-down after
an HTTP 429, [Rate limits](setup.md#rate-limits)). A review with three rounds
makes up to four calls instead of one, each carrying the whole diff again, so
it takes more time and more tokens, and behind a gateway with a low limit
noticeably more time (`metadata.rate_limit_waits` shows it). Start with 2 or 3
rounds.

### What is never done

- Anything is built, run or loaded as code; hooks or repository configuration
  are executed.
- The network is used while the tools run, or a missing blob is fetched.
- The base commit is read, or any other commit than the pull request head.
- The tools are turned on or raised by a file of the repository.
- File content, search text or a tool result is written to a log.
- The diff is shortened to make room for a tool result.
- A secret-pattern scan of the repository is made (backlog).

## Why not in serve mode

A shared server would hold code fetched with one user's token, and another user
could receive it as context; v1.1 does not solve per-request authorization of
cached code. In `serve` mode, `context.repo.enabled` is a startup error:

```text
context.repo.enabled: repository context is not available in serve mode (cached code fetched with one user's token must not reach another user); unset REVIEW_MCP_CONTEXT_REPO_ENABLED
```

## Requirements

- **git 2.31 or later** on the machine that runs review-mcp (`git --version`;
  the version is checked once per process). With a missing or older git, the
  review runs without context and says
  `repository context skipped: git 2.31 or later is required`.
- Searches also set `GIT_NO_LAZY_FETCH` where git supports it; on older git,
  `protocol.allow=never` alone blocks lazy fetches, which is tested.
- The provider must serve the pull request ref over HTTP(S): Gitea
  `refs/pull/<n>/head`, Bitbucket Server `refs/pull-requests/<n>/from`,
  GitHub `refs/pull/<n>/head`, GitLab `refs/merge-requests/<n>/head`. The
  GitHub clone address is `{github.base_url}/{owner}/{repo}.git` (the web base,
  never the API base). The GitLab one is `{gitlab.base_url}/{full path}.git`:
  the web base with its context path and every group of the project's path.

## Security

- **The token** reaches `git` only through the environment of the one child
  process that fetches (`http.extraHeader` as `GIT_CONFIG_*`): never an
  argument (other local users can read those), never the disk (not in
  `.git/config`, not in the cache), never a log or an error. The first scheme
  is `Bearer` on Bitbucket Server and `token` on Gitea; on an HTTP 401 the
  other, HTTP Basic with the token user's name, is tried once, and the scheme
  that worked is remembered for the process. On GitHub the first scheme is HTTP
  Basic with the fixed user `x-access-token` and the token as the password
  (GitHub does not accept a bare Bearer token for git over HTTPS); `Bearer` is
  tried once after a 401. See [GitHub](github.md#repository-context). On
  GitLab the only scheme is HTTP Basic with the fixed user `oauth2` and the
  token as the password; there is no second attempt and no identity lookup. The
  token needs `read_repository` (a personal access token) or the Reporter role
  (a project or group access token). See [GitLab](gitlab.md#repository-context).
- **A fresh environment.** Every `git` process gets an allowlisted
  environment (`PATH`, `SYSTEMROOT` on Windows, the proxy variables, `LANG=C`,
  git's prompts disabled), never a copy of yours, which may hold other tokens.
- **Your git configuration is not read.** `HOME` and `XDG_CONFIG_HOME` point at
  the empty `<cache_dir>/.home`, and the system configuration is ignored
  (except on Windows, where Git for Windows keeps its TLS settings there).
- **The clone URL is pinned** to the configured provider base URL and the
  resolved repository; nothing from a pull request body or comment ever forms a
  URL. Before any request, git expands the URL it would use (through every
  `url.*.insteadOf`) and anything but the pinned URL stops the fetch with
  reason `redirect`. Redirects are not followed.
- **Proxy and TLS follow the provider client:** the proxy variables are passed
  on, `ca_cert` becomes `http.sslCAInfo` and `insecure_skip_verify` becomes
  `http.sslVerify=false`.
- **Searches are offline:** no credential, every protocol forbidden, no lazy
  fetch. A file whose blob is not in the cache is skipped, never fetched.
- **git's own error text is never passed on**; failures are mapped to the fixed
  reasons below.

## Reading the result

The coverage section gains a line:

```text
- Repository context: 5 symbols, 7 references from 3 files
- Repository context: skipped: `auth`
```

nothing is shown when repository context is off. The structured result has
`coverage.repo_context`:

```json
{"status": "used", "reason": "", "symbols": 5, "references": 7, "files": 3}
```

- `status` is `off` (disabled; nothing was done), `used` (the repository was
  searched) or `skipped` (it is on but not in the prompt; `reason` says why).
- `symbols` is the number of symbols searched; `references` and `files` count
  the uses in the prompt and their distinct files. For a review in parts they
  are summed over the parts. `used` with `0 symbols` means the diff defined
  nothing to look for.
- `reason` is always present and empty unless skipped. A review never fails
  because of repository context: a skip is a note and a reason.

| Reason | Meaning |
|---|---|
| `auth` | The git server did not accept the token. The token needs read access to the repository's code. |
| `not_found` | The repository or the pull request ref was not found by `git`. |
| `timeout` | Fetching took longer than `context.repo.fetch_timeout_seconds`. |
| `too_large` | The repository is larger than `context.repo.max_repo_mb`. |
| `sha_mismatch` | The pull request head changed while it was fetched (a push in between). Run again. |
| `redirect` | The git server answered with a redirect, or the URL differed from the pinned one. |
| `git_failed` | `git` failed for another reason; its output is not shown. |
| `git_unavailable` | `git` is missing or older than 2.31. |
| `busy` | Another process held the cached repository's lock for the whole fetch timeout. |
| `cache_unusable` | The cache directory cannot be created or used, or it is not a review-mcp cache. |
| `unsupported` | The repository cannot be fetched with `git` (no base URL, an unusable name or head). |
| `budget` | The diff needs the room: the context window leaves none for the block. |
| `nothing_to_review` | The diff is empty after filtering, so no model call and no context. |
| `client_findings` | The call was a publish of findings supplied by the client (`pr_review` with `findings`); no prompt is sent, so nothing is read ([Focused reviews](review.md#focused-reviews-and-the-merged-publish)). |

A review in parts is `used` when at least one part searched the repository, and
a note names the parts that lack it; it is `skipped` when none did.

`coverage.repo_context.tools` reports the [repository
tools](#reading-the-tools-result) in `pr_review` and `pr_ask`.

## Measuring the gain

`diag review --repo-context=on|off` overrides `context.repo.enabled` for one
run, so you can compare a review both ways (`--json` prints the structured
result):

```sh
review-mcp diag review <PR_URL> --repo-context=off
review-mcp diag review <PR_URL> --repo-context=on
review-mcp diag review <PR_URL> --repo-context=on --dry-run   # block tokens and the symbol list
```

`tools/evalrepo` does this for a list of pull requests and prepares the rating:

```sh
go build -o review-mcp ./cmd/review-mcp
go run ./tools/evalrepo -out ./eval -bin ./review-mcp -urls prs.txt
```

`prs.txt` holds one pull request URL per line (`#` starts a comment); URLs may
also be given as arguments. For each pull request it runs the review without and
with context (nothing is published) and writes, under `-out` only:

- `pr-NN/off.json` and `pr-NN/on.json`, the structured results;
- `pr-NN/compare.md`, both reviews' findings and coverage one after another;
- `ratings.csv`, one row per finding of both runs with the columns
  `cross_file`, `correct` and `new` left empty for you: is the finding about
  code outside the changed files, is it right, and does it appear only with
  context.

The rating is **blind** by default, so that knowing which review had context
cannot colour it. The rows are shuffled with a seed (`-seed N`; the default is
time-based, and the seed is printed and written to `README.txt`, so a run can
be reproduced), the sheet carries an opaque `sample` id instead of the mode,
and the mapping from sample to pull request, mode and finding is in
`key.csv`, which the sheet does not reference. Rate from `ratings.csv` alone:
opening a file under `pr-NN/` reveals the mode. `-blind=false` writes the
plain layout (mode, finding number and coverage status in columns, no
`key.csv` or `README.txt`).

`-out` is required; evalrepo refuses a directory inside the repository-context
cache (it asks the binary where the cache is, or take `-cache-dir`) and a
directory that already has files. The reviews' text never goes to its standard
output or log.

**Stage 2** (a real code graph instead of name matches) starts only if your
ratings show a clear gain on at least **20 pull requests**.

## Backlog

- Evaluation of `pr_ask` with context (`tools/evalrepo` reviews only in v1.1).
- A real code graph (stage 2), only after the rule above is met.
- Repository tools in a review in parts.
- Repository tools for `pr_describe` and `pr_improve`.
- Deterministic secret-pattern scanning.
- Reading the base commit for "before" comparisons (needs a base fetch decision).
- V-13b uses these tools inside related repositories.
