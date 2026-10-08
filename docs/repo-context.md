# Repository context

A diff shows what changed, not who depends on it. With **repository context**
turned on, `pr_review` and `pr_ask` also show the model where the symbols that
the pull request changes are used in the rest of the project: the callers of a
changed function, the other implementations of a changed interface.

It is **opt-in** (off by default) and **stdio only**: it is a startup error in
[serve mode](serve.md#repository-context-is-not-available). It needs the
system `git`. Nothing in this guide is needed for a review without it.

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
   Related code outside this pull request (read-only context; it may be incomplete). Use it to judge the effect of the change on callers and implementations. Do not report issues in this code unless the pull request causes them.
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
- git 2.44 or later also sets `GIT_NO_LAZY_FETCH` for the searches. Older git
  relies on `protocol.allow=never` alone, which stops the same fetch.
- The provider must serve the pull request ref over HTTP(S): Gitea
  `refs/pull/<n>/head`, Bitbucket Server `refs/pull-requests/<n>/from`.

## Security

- **The token** reaches `git` only through the environment of the one child
  process that fetches (`http.extraHeader` as `GIT_CONFIG_*`): never an
  argument (other local users can read those), never the disk (not in
  `.git/config`, not in the cache), never a log or an error. The first scheme
  is `Bearer` on Bitbucket Server and `token` on Gitea; on an HTTP 401 the
  other, HTTP Basic with the token user's name, is tried once, and the scheme
  that worked is remembered for the process.
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

A review in parts is `used` when at least one part searched the repository, and
a note names the parts that lack it; it is `skipped` when none did.

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
- `ratings.csv`, one row per finding with the columns `cross_file`, `correct`
  and `new` left empty for you: is the finding about code outside the changed
  files, is it right, and does it appear only with context.

`-out` is required; evalrepo refuses a directory inside the repository-context
cache (it asks the binary where the cache is, or take `-cache-dir`) and a
directory that already has files. The reviews' text never goes to its standard
output or log.

**Stage 2** (a real code graph instead of name matches) starts only if your
ratings show a clear gain on at least **20 pull requests**.
