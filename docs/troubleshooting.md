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
review-mcp diag pr https://github.example.com/octo/demo/pull/7
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
| `github:merge_base` | GitHub's compare endpoint reported the merge base; this is the normal case. |
| `github:base_sha` | The compare endpoint could not be read, so the target branch revision recorded on the pull request (`base.sha`) was used. |

### `skipped` reasons

A skipped file is not part of `files` and is not reviewed.

| Reason | Meaning |
|---|---|
| `binary` | The file is binary (on GitHub: no patch, and its extension is on the binary list). |
| `file_limit` | More files than `diff.max_files_full_content` (Bitbucket Server cannot build a patch without file contents). GitHub's own 3000-file listing limit is a note, not a skipped file (see [GitHub](#github)). |
| `size_limit` | A side of the file is larger than `diff.max_file_bytes` (Bitbucket Server); on GitHub, a file with line changes whose patch GitHub did not send because the diff is too large. |
| `fetch_failed` | The file's contents could not be fetched or it is listed by only one of Gitea's two sources. |
| `filtered` | An ignore rule excluded the file (`diag pr` applies none; `diag diff` applies the file filter and says which rule matched). |

In `files`, `base_status` and `head_status` say whether the full content of
each side was fetched: `full`, `not_fetched_file_limit`,
`not_fetched_size_limit`, `fetch_failed`, or `not_applicable` (the base side
of an added file, the head side of a deleted one). On Gitea a file without
full content keeps its patch. A file that GitHub sends without a patch and
without line changes (an empty file, a pure rename, a mode change) is listed
with an empty patch and shows up as `empty_diff` in the reviews.

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
state the provider does not report (`"resolved": null`) is always shown (on
GitHub that is every thread, and the result carries a note; see
[GitHub](#resolved-state)). At most
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
there. On GitHub a reply to an inline comment lands inside the thread, and a
reply to a general comment or a review body is a new PR-level comment with the
quote line `> Replying to @alice`. This needs write access (see the token
scopes below).

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
| `--prompt-tokens N` | `2205` | The estimated size of the prompt around the diff. The default is the measured maximum of the review prompts (all fields on, a non-English language, extra instructions; see [Reviewing pull requests](review.md#prompt-tokens)), without the PR's own title and description. Raise it to see how a longer prompt squeezes the diff; `diag review --dry-run` reports the exact figure for a PR. |

```json
{
  "budget": { "context_window": 4096, "soft_limit": 1596, "hard_limit": 2096, "prompt_tokens": 1000, "factor": 0.3, "limit": "context_window" },
  "fast_path": false,
  "tokens": 1454,
  "included": ["server/app.go", "server/util.go", "server/fresh.go"],
  "omitted": { "added": [], "modified": ["server/renamed.go", "web/index.ts"], "deleted": [] },
  "clipped": [],
  "deleted_listed": ["server/gone.go"],
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
| `omitted.added`, `omitted.modified`, `omitted.deleted` | The file's diff is not in the text because of the budget (renamed files count as modified). A deleted file is here only when its name is not in the text either. |
| `clipped` | The file is in the text but cut short with `...(truncated)` (see `large_patch_policy` below). |
| `deleted_listed` | A deleted file whose patch the compressed path drops on purpose and whose name is in the "Deleted files" section of the text. It counts as reviewed. |
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

## The review says partial

A review or answer that starts with "Partial review: N of M changed files were
reviewed" (or "Partial answer"; `pr_improve` leads with the same "Partial
review" line) tells you that some changed files were not
fully seen by the model, and that nothing is concluded about them. The
coverage section lists each of them with its reason. What limits coverage
depends on that reason:

| Reason in the coverage section | What limits it | What to do |
|---|---|---|
| "Left out to fit the context window" (added, modified, deleted), or "Included in part only (clipped ...)" | The diff budget: `diff.max_tokens` when it is set and lower, otherwise the context window (`llm.context_window`, or the window the endpoint reports, at 90 %). The note at the end of the result says which one applied: "raise or unset diff.max_tokens" means the cap, "use a model with a larger context window" means the window. | Raise or unset `diff.max_tokens`, raise `llm.context_window` to the real window of the model, or use a model with a larger window. `diag review --dry-run` and `diag diff` report `budget.limit` and the files that would be left out. Narrowing the pull request also works. |
| `size_limit` | The provider's size limits: `diff.max_file_bytes` and `diff.max_diff_bytes`. | Raise those keys, or review the file by hand. A larger context window does not help. |
| `file_limit` | `diff.max_files_full_content`: Bitbucket Server skips the files beyond that count. | Raise the key, or split the pull request. |
| `fetch_failed`, `unparseable_patch` | The provider did not return the file's content or patch, or the patch was not a plain unified diff. | Run `diag pr` and `diag diff` to see the provider's answer; check the token scopes. |
| Left out after `review.max_chunks` parts (the note says "raise review.max_chunks") | The number of parts of a [review in parts](review.md#large-pull-requests). | Raise `review.max_chunks` (up to 32); each part adds one model call. The budget of each part follows the first row. |
| `too_large` | In a review in parts with `diff.large_patch_policy = "skip"`, the file does not fit a part of its own. | Use the `clip` policy (the default) to review its beginning, raise the diff budget as in the first row, or review the file by hand. |
| `model_call_failed` | The model call of the part that held the file failed; see [Part I of N failed](#part-i-of-n-failed). | Run the review again. |

Files in the **Filtered** group (ignore rules, generated files) and binary
files do not make a review partial. If the banner is present although you
expected a complete review, compare `coverage.reviewed_files`,
`coverage.not_reviewed_files` and `coverage.total_files` with the lists in the
coverage section. The same applies to `job_result`: it returns the original
result, banner included.

## The review took several minutes

A large pull request is reviewed in several parts, one model call after
another ([Large pull requests](review.md#large-pull-requests)), so a review in
N parts takes about N times as long as one call. The coverage section says
"Reviewed in N model calls." and `coverage.model_calls` has N.

- **stdio:** the call answers with a `job_id` after `wait_seconds`, and
  `job_result` collects the review; progress shows `calling model (part I of
  N)`. Nothing needs changing unless the total time is too long for you.
- **serve:** the call runs in its request; raise the client's tool timeout
  ([Serve mode](serve.md#long-calls)).
- A `pr_improve` run in N parts makes up to N suggestion calls and up to N
  self-review calls, so it takes up to about twice as long as the same review;
  its coverage section also says "Reviewed in N model calls." (the suggestion
  calls), and `metadata.llm_calls` counts the self-review calls as well.
- To make it shorter: lower `review.max_chunks` (with `1` a review is one call
  again, and the files that do not fit are listed as omitted), or use a faster
  model. `diff.max_tokens` makes each part smaller and faster, but there are
  then more parts, so the review as a whole is not shorter.
- `llm.timeout_seconds` bounds each part's call, not the whole review.

## Part I of N failed

A note such as "Part 2 of 3 failed (llm_timeout); its files were not
reviewed." means the model call of one part of a
[review in parts](review.md#large-pull-requests) failed. The other parts were
reviewed and merged; the failed part's files are listed under Skipped with
reason `model_call_failed`, `coverage.failed_parts` counts the failed parts,
and the review is partial. The class in parentheses is fixed and never contains
the error text:

| Class | Meaning |
|---|---|
| `llm_timeout` | The part's call took longer than `llm.timeout_seconds`. Raise it, or lower `diff.max_tokens` so each part is smaller. |
| `review_unparseable` | The answer could not be parsed as a review, also after one retry. Run again; check that the model follows YAML output instructions. |
| `improve_unparseable` | The answer of a `pr_improve` suggestion call could not be parsed as code suggestions, also after one retry. Run again; check that the model follows YAML output instructions. |
| other `llm_*` classes | The LLM error of [LLM and review errors](#llm-and-review-errors), for example `llm_rate_limited` or `llm_upstream`. |
| `unclassified` | Any other failure; rerun with `REVIEW_MCP_LOG_LEVEL=debug` (the log names the part and the class, never content). |

Run the review again to cover those files. When every part fails, the review
fails with the first part's error sentence, as a review in one call does.

## `pr_info` required_approvals notes

For GitHub see [GitHub](#github) below: the notes differ and the two sources
(rulesets and classic protection) are combined.

On Gitea, `pr_info` reads the repository's branch protection rules and picks
the one for the target branch: a rule named like the branch, else the first
rule whose glob pattern (`release/*`) matches it (patterns are tried by the
rule's `priority` when Gitea sends one, lowest first, otherwise in list order). Reading the rules may need
repository admin. The note in `required_approvals_note` says what happened:

| `required_approvals` and note | Meaning |
|---|---|
| `null`, "not readable with this token" | The list of rules could not be read (the token lacks admin rights, the server does not offer it, or the call failed). Give the token repository admin, or read the number in the Gitea settings. |
| `0`, "no branch protection rule applies to the target branch" | The rules were read and none matches the target branch, so no approvals are required by a rule. Check the rules in the repository settings if you expected one. |
| `null`, "a protection pattern could not be evaluated" | No rule matched, but a rule's pattern is one review-mcp cannot evaluate (a pattern with `**`, or one Go's `path.Match` rejects; Gitea's glob may accept more), so that rule might apply. Read the number in the Gitea settings. |

## GitHub

What to check when a GitHub pull request does not work as expected. The full
behaviour is in [GitHub](github.md); the sentences below are exact.

### Rate limited

```text
the server rate-limited the request (HTTP 403): retry after 2026-10-09 12:30:00 UTC
```

GitHub limits the requests of a token (a primary limit per hour and a secondary
one for bursts). The time is the reset, in UTC. review-mcp itself waits at
most once, only when the reset is 60 seconds or less away, and then repeats the
request one time. **What to do:** wait until the time shown and run the tool
again. On a large pull request fewer parallel calls help (do not start several
reviews at once with one token). When the sentence ends at `(HTTP 403)` with no
time, GitHub sent no usable reset. A `403` without a rate-limit signal is an
authentication problem instead, see below.

### Authentication failed on GitHub

`authentication failed: check the token and its scopes (HTTP 401)` or
`(HTTP 403)`. **What to do:** check that the token is still valid and has not
expired; for a fine-grained token check its resource owner and that the
repository is selected, and the permissions in [GitHub](github.md#token)
(Pull requests, Contents, Metadata); for an organisation with SAML single
sign-on authorise the token for it. A repository the token cannot see answers
`the requested resource was not found` (HTTP 404).

### Ambiguous comment id

```text
the server sent an unexpected response: the comment id is ambiguous on GitHub
```

GitHub numbers issue comments, review comments and review bodies separately,
so one `comment_id` can name more than one comment on the pull request.
Nothing was written. **What to do:** reply to or edit a comment whose id is not
shared (list the threads again and pick another comment of the same
conversation, for example a reply in the thread), or reply with
`pr_comment_create`.

### `pr_info` required approvals (GitHub)

Both the rulesets and the classic branch protection are read, and the larger
readable count wins. `required_approvals_note` is one of:

| `required_approvals` and note | Meaning and what to do |
|---|---|
| a number, no note | At least one source gave a count (the rulesets, the classic protection, or both). |
| a number, "classic branch protection is not readable with this token; the required count may be higher" | The rulesets gave the count, but the classic protection could not be read (it needs repository admin: Administration read on a fine-grained token). A classic rule may ask for more. Give the token that right, or read the number in the branch settings. |
| `null`, "not readable with this token" | Neither source gave a count: no ruleset has a pull request rule that the token can see, and the classic protection is unreadable or the branch is not protected (GitHub answers both alike). `0` is never guessed. |

### `pr_info` merge status (GitHub)

`merge_blockers` are fixed texts from GitHub's `mergeable_state`:

| Blocker | Cause and what to do |
|---|---|
| merge conflict | The branch conflicts with the target. Rebase or merge the target. |
| required reviews or checks are not satisfied | GitHub's `blocked`: a required review or a required check is missing or failing. GitHub does not say which; look at the pull request page. |
| the branch is behind the target branch | The repository requires the branch to be up to date. Update the branch. |
| the pull request is a draft | Mark it ready for review. |
| other merge check | A state review-mcp does not know. |

When `mergeable` is `null` and there are no blockers, GitHub has not computed
the merge state yet; run `pr_info` again in a moment.

The **note** `some checks that are not required are failing or pending` (in
`notes`) is GitHub's `unstable` state. It is not a blocker: the pull request is
mergeable, and only checks that are not required fail or are pending. Failing
required checks show as `required reviews or checks are not satisfied`.

### Limits of what GitHub lists

| Note | Meaning and what to do |
|---|---|
| `GitHub lists at most 3000 files of a pull request: N more changed files were not listed, so they are not reviewed (file_limit).` | The pull request has more than 3000 changed files, which is GitHub's limit for the file list. N files are not reviewed and have no name in the result. Split the pull request or review them by hand. |
| `GitHub lists at most 250 commits of a pull request; the commits past them are not available.` | Only the first 250 commits are listed. `pr_info` shows this note; `pr_describe` has its own, below. |
| `The pull request has N commits, but only M commit messages could be read; the description used those.` | `pr_describe` on a pull request whose commit count is larger than the number of messages the provider returned (on GitHub, past 250). The description is still made, from the messages that were read. Read the rest of the history by hand if it matters. Gitea and Bitbucket Server never show it. |

### Resolved state

```text
Resolved state is not available on GitHub without GraphQL; all threads are shown.
```

This note of `pr_comments` is expected on GitHub. REST cannot show whether a
review thread is resolved, so `include_resolved` changes nothing and every
thread is listed. Check the pull request page for what is resolved.

### Other notes you may see on GitHub

| Note | Meaning |
|---|---|
| `The reviews could not be read, so the reviewers are not listed.` | The pull request's reviews could not be read (token, rate limit, server error). `merge` and approval facts are still returned. |
| `A review by the token's user could not be checked for review-mcp's markers and is not listed.` | A review of the token's user whose comments could not be read; it is left out of the reviewers because it could be review-mcp's own. |

### Configuration sentences

| Sentence | What to do |
|---|---|
| `REVIEW_MCP_GITHUB_TOKEN is required because github.base_url is set` | Stdio: set the token. |
| `REVIEW_MCP_GITHUB_TOKEN is set but github is not enabled (github.base_url is unset); the token is ignored` | Warning: set `REVIEW_MCP_GITHUB_BASE_URL`, or unset the token. |
| `github.api_url is set but github.base_url is not (REVIEW_MCP_GITHUB_BASE_URL)` | `github.api_url` only adjusts an enabled provider; set the base URL as well. |
| `TLS verification disabled for github` | Warning: `REVIEW_MCP_GITHUB_INSECURE_SKIP_VERIFY` is set. Prefer `REVIEW_MCP_GITHUB_CA_CERT`. |

A GitHub URL that reports `url_not_configured` although it looks right: check
that `github.base_url` is the **web** address (`https://github.com` for the
public product, your GHES address otherwise), not the API address, and that
the scheme and port match. A GHES whose API is not at `{base}/api/v3` needs
`github.api_url`.

## Repository context was skipped

The coverage section says `Repository context: skipped: <reason>` (and
`coverage.repo_context.reason` has the same word) when
[repository context](repo-context.md) is on but not in the prompt. The review
itself is unaffected. The reasons you will meet most:

| Reason | What to do |
|---|---|
| `auth` | The git server did not accept the token over HTTP. The token needs read access to the code, not only to pull requests; check the scopes in [Token scopes](#token-scopes). |
| `redirect` | The git server redirected, or the repository URL differs from the configured base URL (a proxy, a `url.*.insteadOf` rule). Redirects are never followed; point the base URL at the real host. |
| `git_unavailable` | `git` is missing or older than 2.31. Install it, or check `server_info` (`context.repo.enabled` shows `enabled, git <version>`). |
| `too_large` | The repository is larger than `context.repo.max_repo_mb`; raise it (and `max_cache_mb`) or leave context off for this repository. |
| `timeout` | The fetch took longer than `context.repo.fetch_timeout_seconds`; raise it for a big repository or a slow link. |
| `busy` | Another review held the repository's lock for the whole timeout; run again. |
| `sha_mismatch` | A push changed the pull request head while it was fetched; run again. |
| `budget` | The diff leaves no room in the context window. Use a larger window, or accept that the diff wins. With `review.max_chunks` above 1 the room is reserved up front instead, and a part may be added. |
| `nothing_to_review` | Nothing was left to review after filtering. |

The full list is in [Repository context](repo-context.md#reading-the-result).
`review-mcp diag review <PR_URL> --repo-context=on --dry-run` shows the
symbols and the block's size without calling the model; `diag cache` lists the
cache. In serve mode the setting is refused at startup: see
[Serve mode](serve.md#repository-context-is-not-available).

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
log); `--json` prints the structured result instead of markdown, and
`--repo-context=on|off` overrides `context.repo.enabled` for the run. See [Reviewing pull requests](review.md) for how to read the result.

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
| the LLM request timed out | `llm_timeout` | The default `llm.timeout_seconds` is 300; raise it, lower `diff.max_tokens`, or use a faster model. Large reviews on slow models take minutes. |
| the LLM endpoint does not list the configured model; check llm.model | `llm_not_found` | The endpoint's model list (`GET {llm.base_url}/models`) has no entry whose `id` equals `llm.model` exactly. Fix `llm.model`, or set `llm.context_window` to skip the lookup. |
| the LLM endpoint does not report the model's context window; set llm.context_window | `llm_protocol` | `llm.context_window` is unset and the endpoint's entry for the model has none of `max_model_len`, `context_length`, `context_window`, `max_context_length` (training-size fields are never used). Set `llm.context_window` to the window your server runs; for Ollama, its `num_ctx`. |
| the LLM endpoint reports a context window below the 4096-token minimum; set llm.context_window if the endpoint is wrong | `llm_protocol` | 90 % of the reported window is below 4096. Use a model with a larger window, or, if the endpoint reports it wrongly, set `llm.context_window` (at least 4096). |
| could not complete the request to the LLM endpoint | `llm_transport` | Network, DNS, TLS or proxy problem between you and the endpoint. |
| the LLM endpoint sent an unexpected response | `llm_protocol` | The endpoint is not OpenAI-compatible at `llm.base_url`, or it answered with an empty message. |
| the pull request diff does not fit the configured context window | `diff_does_not_fit` | Raise `llm.context_window`, or narrow the PR. Nothing was sent to the model. Applies to `pr_review`, `pr_ask`, `pr_describe` and `pr_improve`. |
| the model's answer could not be parsed as a review, also after one retry | `review_unparseable` | Try again, or use a model that follows YAML output instructions. |
| output_language must be a locale code such as en-US or tr-TR / max_findings must be an integer from 1 to 20 / wait_seconds must be an integer from 0 to 600 | (argument) | Fix the argument; nothing was sent anywhere. |

If the configuration is invalid, `pr_review` returns "review-mcp configuration
is invalid; call server_info for the list of problems" and sends nothing to
the provider or the LLM.

## Ask a question with `diag ask`

```sh
review-mcp diag ask <PR_URL> --question "Which files change the request validation?" --dry-run
review-mcp diag ask <PR_URL> --question "Could this break existing callers?"
```

`--question` is required (at most 8000 characters). `--dry-run` runs
everything up to the LLM call and prints a JSON report with the prompt, diff
and request token estimates, the budget and the coverage; the model is not
called. Without `--dry-run` it prints the markdown the `pr_ask` tool returns;
`--publish` also posts the question and answer as a PR comment, and
`--show-prompt` prints the rendered prompts after the output (never to the
log). A missing, empty or over-long question, and `--dry-run` with
`--publish`, are usage errors: exit 2, nothing is sent. See
[Asking questions](ask.md).

The LLM errors above apply to `pr_ask` too. Its own argument errors are:

| Sentence | What to check |
|---|---|
| question must not be empty | Pass a non-blank `question` (`--question`). Nothing was sent anywhere. |
| question is too long: at most 8000 characters are allowed | Shorten the question; it is never truncated for you. Nothing was sent anywhere. |
| output_language must be a locale code such as en-US or tr-TR | Fix `output_language`. Nothing was sent anywhere. |

If the configuration is invalid, `pr_ask` returns the same "review-mcp
configuration is invalid" sentence as `pr_review` and sends nothing. The
"the pull request diff does not fit" sentence also applies; a long question
leaves less room for the diff.

## `pr_describe`

`pr_describe` fails with the provider and LLM sentences above (the diff does
not fit, an LLM error, an unparseable answer) or, for a bad argument, with one
of the argument sentences in [Every error sentence](#every-error-sentence).
Everything that goes wrong while **publishing** is different: the description
is still returned, and the reason is the fixed sentence in `publish.error`
(also in the Publishing section of the text, as "not written: ..."), with
`published: false`. Notes are in the `notes` list and the Notes section. See
[Describing pull requests](describe.md) for how publishing works.

### Publish outcomes

| Sentence | What it means and what to do |
|---|---|
| The pull request description contains a damaged review-mcp region; fix or remove it and run again. | `publish_mode=description` found marker lines that are not exactly one `[//]: # (review-mcp:describe:start)` line followed by one `[//]: # (review-mcp:describe:end)` line: two or more starts or ends, an end before a start, a start without an end, or an end without a start. Nothing was written. Open the description in the web UI and fix it: either delete both marker lines and everything between them (the next run appends a fresh region), or restore the missing marker. A marker is a line that equals the marker text once spaces and tabs at its ends are removed. **Fences are not special:** a marker line inside a fenced code block, for example a pasted copy of an earlier description in a code block, counts too, and also makes the region damaged. Remove or change that line (adding a character to it is enough). |
| The pull request description changed while it was being updated; nothing was written. | Someone edited the description (or, on Bitbucket Server, changed the pull request) between the read and the write, twice in a row. review-mcp never overwrites such an edit. Nothing was written; wait until the editing is done and run again. |
| Nothing was described, so the pull request description was not changed. | `publish_mode=description` with no described file and no summary: an empty region would replace an earlier good one, so nothing was sent. Look at the Not described section and the notes for why (`not_returned`, a failed part, a diff that did not fit), fix that, and run again. `publish_mode=comment` still publishes the coverage and the notes. |
| This provider does not support editing the pull request description; use publish_mode=comment. | The provider has no description edit (the capability `DescriptionEdit`). Gitea, Bitbucket Server and GitHub all have it, so you see this only with a provider that does not. Use `publish_mode=comment`. No request was made. |
| authentication failed: check the token and its scopes (HTTP 403) | The token may read the pull request but not write it. Description mode needs write access to the pull request, comment mode write access to comments; see [Token scopes](#token-scopes). |
| the server sent an unexpected response: a reviewer cannot be named, so the update was not sent | Bitbucket Server only. The update is a full `PUT` that sends the reviewers back by user name, and one reviewer in the server's answer has none. review-mcp refuses rather than send a list that would drop that reviewer. Nothing was written; use `publish_mode=comment`, or edit the description by hand. |
| the description could not be published to the pull request | An error that is not one of the classified provider errors. Rerun with `REVIEW_MCP_LOG_LEVEL=debug`; the log names the failing step, never content. |

Another provider sentence from [Error messages](#error-messages), such as
`not_found` or `upstream`, can also appear as the outcome, with its usual
meaning.

### Notes after publishing

| Note | What it means and what to do |
|---|---|
| Bitbucket Server changed the reviewer list or a review state while the description was updated; check the pull request. | The description was written, but when the reviewers were read after the write one was missing or had another state than before (for example an approval was reset). It cannot be undone by review-mcp. Open the pull request, check the reviewers and ask them to approve again. The update sends the reviewer list back by name and relies on the server to keep their verdicts; if you see this note, please report it with your Bitbucket Server version. |
| The reviewers could not be read around the description update, so a change to the reviewer list or a review state could not be ruled out; check the pull request. | The description was written, but the reviewers could not be read before or after (a failed request, or a server that did not return them), so review-mcp cannot say whether anything changed. Check the reviewers by hand. |
| The title was not generated, so the pull request title was not changed. | `update_title=true`, but no title came back (the summary call of a description in parts failed, or the answer had none). The description was published; run again to try the title. |
| The previous description comment could not be updated; a new one was posted. | Comment mode: the comment of an earlier run could not be edited (for example it was deleted, or the token cannot edit it), so a new comment was posted. The old one stays. |
| The existing description comment could not be looked up; a new one was posted. | Comment mode: the comments could not be read, so an earlier description comment could not be found and a new one was posted. Delete the duplicate by hand if the earlier one is still there. |
| 1 older description comment by the same user was left unchanged. (or "N older description comments by the same user were left unchanged.") | Comment mode found several comments of yours with the description marker. The newest was edited; the others are never deleted by review-mcp. Delete them in the web UI if you do not want them. |

The notes about the description itself (a part that failed, files that were
not returned, the summary that could not be generated) are listed in
[Describing pull requests](describe.md#not-described-and-why).

### The title lost its work-in-progress prefix

On Gitea a pull request is a draft when its title starts with a
work-in-progress prefix. With `update_title=true`, review-mcp keeps that
prefix in front of the generated title, so the draft state does not change, and
removes a `WIP:` or `[WIP]` that the model put at the start of the generated
title. Two limits:

- Only the default prefixes `WIP:` and `[WIP]` (in any case) are known. Gitea
  does not report its `WORK_IN_PROGRESS_PREFIXES` setting, so if your server
  uses other prefixes, a draft that is a draft through one of them loses its
  prefix, and with it the draft state, when the title is replaced. Do not use
  `update_title` on such pull requests, or set the title back by hand.
- The prefix is kept only when the server reports the pull request as a draft.

Bitbucket Server keeps the draft state as a flag, and the update sends it back
unchanged.

### Describe errors and arguments

| Sentence | Class | What to check |
|---|---|---|
| the model's answer could not be parsed as a pull request description, also after one retry; try again, or check that llm.model follows the YAML output instructions | `describe_unparseable` | Try again, or use a model that follows YAML output instructions. For a part of a description in parts it is the class in "Part I of N failed (describe_unparseable)"; for the summary call it makes the fallback in the notes. |
| publish_mode must be comment or description | (argument) | Fix `publish_mode`. Nothing was sent anywhere. |
| update_title needs publish=true and publish_mode=description | (argument) | Set both, or drop `update_title`. Nothing was sent anywhere. |

The description says "Partial description" for the files that were not
described; the causes and fixes are those of [the review says
partial](#the-review-says-partial), with "describe" for "review". Files the
model was shown but did not describe are listed with reason `not_returned`;
that is the model's answer, not a limit: run again or use a model that follows
the output instructions. A description in parts takes one model call per part
and one more for the summary ([The review took several
minutes](#the-review-took-several-minutes) applies).

## Describe a pull request with `diag describe`

```sh
review-mcp diag describe <PR_URL> --dry-run
review-mcp diag describe <PR_URL>
```

`--dry-run` runs everything up to the model calls and prints a JSON report with
the budget, the prompt and diff token estimates, the coverage and the number of
commit messages; the model is not called. Without `--dry-run` it prints the
markdown the `pr_describe` tool returns, or with `--json` the structured
result, and `--show-prompt` prints the rendered prompts of the first call after
the output (never to the log). It never writes to the pull request; there is no
publish option. `--dry-run` with `--json`, a missing PR URL and an unknown flag
are usage errors: exit 2, nothing is sent. See
[Describing pull requests](describe.md#checking-the-budget-with-diag-describe).

## `pr_improve`

`pr_improve` fails with the provider and LLM sentences above (the diff does not
fit, an LLM error) or, for a bad argument, with one of the argument sentences in
[Every error sentence](#every-error-sentence). When the suggestion answer cannot
be parsed, also after the one re-ask, the call fails with "the model's answer
could not be parsed as code suggestions, also after one retry; try again, or
check that llm.model follows the YAML output instructions" (class
`improve_unparseable`). Try again, or use a model that follows YAML output
instructions. For a part of a run in parts it is the class in "Part 2 of 3
failed (improve_unparseable)"; for a self-review call it is not an error at all,
but unscored suggestions (below).

Everything that goes wrong while **publishing** is different: the suggestions are
still returned, and the reason is the fixed sentence in `publish.error` (with
`published: false`) or in the `error` of a suggestion's `anchor`. Notes are in
the `notes` list and the Notes section. See [Suggesting code
changes](improve.md) for how scoring, verification and publishing work.

### Notes about the suggestions

| Note | What it means and what to do |
|---|---|
| No suggestions. | The section of the text view when the result has no suggestion: the model found nothing worth suggesting, or the notes say what happened to them (dropped by the score, for a file it was not shown, with no change, or a failed part). It is not an error. |
| No reviewable changes after filtering. | Every file was filtered, skipped or empty, so the model was not called. Look at the coverage section; check `ignore.glob`, `ignore.regex` and the `skipped` reasons ([`diag diff`](#read-the-prepared-diff-with-diag-diff)). |
| A model answer was cut off by its output limit; the suggestions may be incomplete. | The answer hit `llm.max_output_tokens` (or the endpoint's own limit). Raise `llm.max_output_tokens`, or lower `improve.max_suggestions_per_part` so that each answer is shorter. |
| A model answer could not be parsed as YAML at first; that call's answer comes from a second attempt. | The one re-ask was used for a suggestion or a self-review call. The result is valid, but a model that needs it often does not follow the YAML output instructions well. |
| The diff was shortened to fit the context window; the coverage section lists the files that are incomplete or left out. | The request-size guard cut the diff of a call. The coverage section lists the files that are clipped or left out; see [The review says partial](#the-review-says-partial). |
| N files were included only in part (clipped) to fit the context window. | The model saw only the beginning of these files; suggestions for lines further down cannot exist. Raise the diff budget as in [The review says partial](#the-review-says-partial). |
| The existing PR discussion could not be read; suggestions may repeat it. | The comment threads or the token's own user could not be read (`read:user` on Gitea), so the discussion block is empty. The run went on; a suggestion that people already raised may come again. Check the token scopes ([Token scopes](#token-scopes)). |
| N discussion threads were left out of the prompt to stay within the discussion token budget. (singular: "1 discussion thread was left out of the prompt to stay within the discussion token budget.") | The discussion is bigger than `review.max_discussion_tokens`; the newest threads were kept. Raise the key, or accept that older threads were not shown. |
| N suggestions without a file, a summary, or the existing or improved code were dropped. (singular: "1 suggestion without a file, a summary, or the existing or improved code was dropped.") | The model left a required field out of those entries. Nothing to fix on your side; a model that follows the output format better drops fewer. |
| N suggestions for a file that was not in the diff shown to the model were dropped. (singular: "1 suggestion for a file that was not in the diff shown to the model was dropped.") | The model named a file its call was not shown with content: another part's file, a file left out, a deleted file, or a path it invented. They are dropped, never posted. |
| N suggestions whose improved code is the same as the existing code were dropped (no change). (singular: "1 suggestion whose improved code is the same as the existing code was dropped (no change).") | The improvement changed nothing but white space. |
| Part 2's suggestions were not scored (the self-review call failed). | The self-review call of part 2 returned an error, its answer could not be parsed after the re-ask, or its request did not fit the context window. That part's suggestions are kept **unscored** (`score: null`, "unscored") and are ranked after every scored suggestion. Run again; for a request that did not fit, lower `diff.max_tokens` or use a larger window. Verification still works for them, by search, because they have no line range. |
| The suggestions were not scored (the self-review call failed). | The same, for a run in one call. |
| N suggestions got no usable self-review score and are kept unscored. (singular: "1 suggestion got no usable self-review score and is kept unscored.") | The self-review answered, but not about these suggestions: no entry matched, or the entry had no integer score from 0 to 10. They are kept, unscored. A weak model that ignores `suggestion_number` causes this; run again or use a better model. |
| N self-review entries did not match a suggestion and were ignored. (singular: "1 self-review entry did not match a suggestion and was ignored.") | An entry had a number outside the list, a file or summary that differs from the numbered suggestion's, or two entries claimed the same suggestion. A mismatch costs a suggestion its score, never the suggestion. |
| N suggestions were dropped by the self-review score (below 7). (singular: "1 suggestion was dropped by the self-review score (below 7).") | The self-review scored them below `improve.min_score` (the number in parentheses is your value). To see them, lower `improve.min_score` (0 keeps every scored suggestion); to see fewer weak ones, raise it. |
| N duplicate suggestions were dropped; the first of each is kept. (singular: "1 duplicate suggestion was dropped; the first of each is kept.") | Two suggestions had the same file, summary and existing code; the higher-ranked one was kept. |
| N further suggestions were not shown because of improve.max_suggestions. (singular: "1 further suggestion was not shown because of improve.max_suggestions.") | More suggestions survived than `improve.max_suggestions` (default 8). The lowest-ranked were cut: the highest scores are always kept. Raise the key (up to 30) to see more. |
| N suggestion line ranges were corrected. (singular: "1 suggestion line range was corrected.") | The self-review gave lines that did not hold the quoted code, but the code was found at exactly one other place in the head file, and the range was set there. Nothing to do. |
| Part 2 of 3 failed (llm_timeout); its files were not reviewed. | See [Part I of N failed](#part-i-of-n-failed). |

### What the discussion leaves out

The discussion block of `pr_improve` (and of `pr_review`) leaves out the comments
the token's own user wrote with a marker of **any** review-mcp tool on their
last line (`[//]: # (review-mcp:` ... `)`): the `pr_review` overview and
findings, the `pr_describe` comment, and the `pr_improve` overview and
suggestions. So a `pr_review` finding on the same PR does not stop a concrete
suggestion for it, and one tool's comments are never shown to another tool's
model as something a person said. A marker typed by another user does not count.
If a suggestion repeats a point a person made, the person's comment may have been
left out of the block by the token budget ("N discussion threads were left out
...") or unread ("The existing PR discussion could not be read; ...").

### Suggestions marked "not anchored"

An unverified suggestion stays in the result and the overview, with a mark, and
is never posted inline. `unverified_reason` in the structured result says why.

| Mark in the text and the overview | `unverified_reason` | What it means and what to do |
|---|---|---|
| not anchored: the quoted code was not found at the given lines | `not_found` | The code the model quoted (`existing_code`) is neither at the lines the self-review gave nor anywhere in the head file after normalising trailing white space, line endings and common indentation. The model paraphrased the code or left a line out. The suggestion text is still readable; apply it by hand, or run again. |
| not anchored: the quoted code was not found at the given lines | `ambiguous` | The quoted code is not at the given lines and appears more than once in the file. review-mcp never takes the first match, because it could comment on the wrong place. The lines in the mark are the self-review's. |
| not anchored: the head file was not available to check the quoted code | `head_unavailable` | The head version of the file was not fetched (over `diff.max_file_bytes`, beyond `diff.max_files_full_content`, a failed fetch, or a binary file), and the patch does not confirm the given range. Raise the limits if the file is not too large, or check the suggestion by hand. See [`skipped` reasons](#skipped-reasons). |

### Publishing outcomes and notes

| Sentence | What it means and what to do |
|---|---|
| The previous overview could not be updated; a new one was posted. | The overview of an earlier run could not be edited (it was deleted, or the token cannot edit it), so a new one was posted. The old one stays; delete it by hand. |
| The existing overview could not be looked up; a new one was posted. | The PR's comments or the token's own user could not be read, so an earlier overview could not be found and a new one was posted. Delete the duplicate by hand if the earlier one is still there. Check `read:user` and the read scopes ([Token scopes](#token-scopes)). |
| The overview could not be updated after the inline comments were posted; it links to the changed lines instead. | The inline comments were posted, but the final edit of the overview failed. The overview is complete, but its rows say "checked against the head file" and link to the file lines instead of the comments. Run again. |
| The comments already on the PR could not be read, so no inline suggestion was posted (it could repeat one); the suggestions are listed in the overview only. | The comments or the token's user could not be read, so review-mcp cannot tell which suggestions are already on the PR and posts none inline. The overview was posted. Their `anchor.status` is `failed` with the error "the comments already on the PR could not be read". Fix the read access and run again. (`pr_review` posts anyway in this case; `pr_improve` does not.) |
| 1 older overview comment by the same user was left unchanged. (or "N older overview comments by the same user were left unchanged.") | Several comments of yours carry the `pr_improve` overview marker. The newest was edited; the others are never deleted by review-mcp. Delete them in the web UI if you do not want them. |
| N suggestions could not be placed on changed lines of one hunk and are listed in the overview only. (singular: "1 suggestion could not be placed on changed lines of one hunk and is listed in the overview only.") | A verified suggestion's lines are not all on new-side lines of one hunk of the provider's diff (they are in unchanged code far from the change, span two hunks, or are in a deleted or binary file), or the server refused the position. They are in the overview, with their full text. Nothing to fix. |
| N suggestions were already posted on this PR and were not repeated. (singular: "1 suggestion was already posted on this PR and was not repeated.") | An inline comment with the same key (file, existing code and improved code) by the token's user is on the PR, from an earlier run. This is the expected outcome of a second run. To get a fresh inline comment for a suggestion, delete the old one first. |
| N suggestions could not be posted as inline comments and are listed in the overview only. (singular: "1 suggestion could not be posted as an inline comment and is listed in the overview only.") | The inline post failed; the `error` of each suggestion's `anchor` has the reason (see below). |
| the suggestions could not be posted as a PR comment | `publish.error`: the overview could not be posted and the error is not a classified provider error. Rerun with `REVIEW_MCP_LOG_LEVEL=debug`; the log names the failing step, never content. No inline comment is posted without the overview. |
| authentication failed: check the token and its scopes (HTTP 403) | `publish.error`: the token may read the pull request but not write it. See [Token scopes](#token-scopes). The suggestions are still returned. |

The `error` of a failed `anchor` is a fixed sentence:

| Sentence | What to do |
|---|---|
| the comments already on the PR could not be read | See the note above. |
| the request conflicts with the current state on the server: the token's user has a pending review on this pull request; submit or delete it first | Gitea only. The token's user has a draft review on the PR, and Gitea would submit it together with the comments, so review-mcp refuses to post. Submit or delete the draft in the web UI and run again. |
| a pending draft review could not be removed; the comment was not posted | Gitea only. The post failed and the draft review it left behind could not be deleted. Open the PR's reviews, delete the draft, and run again. |
| the outcome of the review request is unknown; the comment may have been posted and was not posted again | Gitea only. The request ended without an answer (a timeout, a dropped connection). Look at the PR; run again, and a comment that did land is recognised and skipped. |
| the comment could not be posted | An error that is not one of the classified provider errors. Rerun with `REVIEW_MCP_LOG_LEVEL=debug`. |
| Another provider sentence from [Error messages](#error-messages), such as `authentication failed ...` or `the server rate-limited the request` | Fix the cause as described there. |

### `pr_improve` arguments and settings

| Sentence | Class | What to check |
|---|---|---|
| output_language must be a locale code such as en-US or tr-TR | (argument) | Fix `output_language`. Nothing was sent anywhere. |
| wait_seconds must be an integer from 0 to 600 | (argument) | Fix `wait_seconds`. Nothing was sent anywhere. |
| improve.max_suggestions: N is out of range (1-30) / improve.max_suggestions_per_part: N is out of range (1-10) / improve.min_score: N is out of range (0-10) | (configuration) | Listed in `problems` of `server_info`; the server is in degraded mode until the value is fixed ([Every error sentence](#every-error-sentence), the first row). |

The result says "Partial review" for the files that were not reviewed; the
causes and fixes are those of [the review says
partial](#the-review-says-partial). A run in parts makes one suggestion call and
one self-review call per part, so it takes up to about twice as long as the same
review ([The review took several minutes](#the-review-took-several-minutes)
applies).

## Suggest changes with `diag improve`

```sh
review-mcp diag improve <PR_URL> --dry-run
review-mcp diag improve <PR_URL>
```

`--dry-run` runs everything up to the model calls and prints a JSON report with
the budget, the prompt and diff token estimates, the coverage, the notes known
so far and the number of discussion threads in the prompt; the model is not
called. Without `--dry-run` it prints the markdown the `pr_improve` tool
returns, self-review calls included, or with `--json` the structured result, and
`--show-prompt` prints the rendered prompts of the first suggestion call after
the output (never to the log). It never writes to the pull request; there is no
publish option. `--dry-run` with `--json`, a missing PR URL and an unknown flag
are usage errors: exit 2, nothing is sent. See
[Suggesting code changes](improve.md#checking-the-budget-with-diag-improve).

## Error messages

Every failure from a provider is reported as one of these fixed sentences.
The text never contains a response body, a token or a query string. A status
code and a short hint (a config key or a short phrase) may follow the
sentence, for example `authentication failed: check the token and its scopes (HTTP 401)`.

| Sentence | Class | Meaning and what to check |
|---|---|---|
| the pull request URL does not match any configured provider | `url_not_configured` | The URL's scheme, host, port or path prefix matches no configured base URL (or Gitea `web_url`). Compare it with `REVIEW_MCP_GITEA_BASE_URL` / `REVIEW_MCP_BITBUCKET_SERVER_BASE_URL` / `REVIEW_MCP_GITHUB_BASE_URL`, including `http` vs `https`, the port, and a Bitbucket context path. No request was sent. |
| the URL matches a configured provider but is not a pull request URL | `url_malformed` | Use the PR page URL: `.../{owner}/{repo}/pulls/{n}` (Gitea), `.../{owner}/{repo}/pull/{n}` (GitHub) or `.../projects/{KEY}/repos/{slug}/pull-requests/{id}` (Bitbucket Server; `/users/{user}/repos/...` for personal repositories). Do not put credentials in the URL. |
| authentication failed: check the token and its scopes | `auth` (HTTP 401/403) | The token is wrong, expired, or lacks a scope; see the token scopes below. Also check that the token belongs to the provider whose URL you used. |
| the requested resource was not found | `not_found` (HTTP 404) | The repository or PR does not exist, or the token's user cannot see it (many servers answer 404 for a hidden repository). On Bitbucket Server, a wrong context path also gives 404. |
| the server rate-limited the request | `rate_limited` (HTTP 429, or 403 on GitHub) | Wait and retry; check any rate limits or a reverse proxy in front of the server. On GitHub the sentence ends with the reset time when it is known; see [GitHub](#github). |
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

## Every error sentence

Every error review-mcp returns to a client is one of these fixed sentences
(X-6). None contains a token, a response body, a prompt or PR content. Find the
exact sentence you received, read its cause and check the key. The detailed
tables above (providers, LLM, review, ask) say more about each class; this
table also covers the configuration, argument and serve-mode errors.

| Sentence | Class | Cause and what to check |
|---|---|---|
| review-mcp configuration is invalid; call server_info for the list of problems | `config_invalid` | The server started in degraded mode. Call `server_info`: `problems` lists every invalid key. Fix the environment or file and restart the server. Nothing was sent. |
| the pull request URL does not match any configured provider | `url_not_configured` | The URL matches no provider's base URL (or Gitea `web_url`). Check `REVIEW_MCP_GITEA_BASE_URL` / `REVIEW_MCP_BITBUCKET_SERVER_BASE_URL` / `REVIEW_MCP_GITHUB_BASE_URL`: scheme, host, port, context path. |
| the URL matches a configured provider but is not a pull request URL | `url_malformed` | Use the PR page URL; no credentials in the URL. |
| authentication failed: check the token and its scopes | `auth` | Wrong, expired or under-privileged provider token. Check `REVIEW_MCP_GITEA_TOKEN` / `REVIEW_MCP_BITBUCKET_SERVER_TOKEN` / `REVIEW_MCP_GITHUB_TOKEN` (stdio) or the `X-Review-MCP-...-Token` header (serve), and [Token scopes](#token-scopes). |
| the requested resource was not found | `not_found` | Wrong repository or PR, a token that cannot see it, or a wrong Bitbucket context path. |
| the server rate-limited the request | `rate_limited` | Provider or proxy rate limit; wait and retry. GitHub adds `(HTTP 403)` or `(HTTP 429)` and, when known, `: retry after <time> UTC`. |
| the server reported an internal error | `upstream` | Provider (or its proxy) failed; see its logs. |
| a response exceeded its size limit | `too_large` | Check `diff.max_diff_bytes` / `diff.max_file_bytes` (named in the hint). |
| the server version is not supported | `unsupported_version` | Bitbucket Server / Data Center 7.0 or later is required. |
| could not complete the request to the server | `transport` | DNS, timeout, TLS or connection problem; see the hint and [Base URL notes](#base-url-notes). |
| the server sent an unexpected response | `protocol` | Base URL points at something that is not the provider API (for GitHub also `github.api_url`); the hint `the comment id is ambiguous on GitHub` is explained under [GitHub](#github); a hint such as `empty body` or `invalid comment id` means `pr_comment_reply` got a blank `body` or a `comment_id` that is not a positive integer. |
| the LLM endpoint rejected the credentials | `llm_auth` | `REVIEW_MCP_LLM_API_KEY` (stdio, or serve with `llm_key_source = server`) or the `X-Review-MCP-LLM-API-Key` header. |
| the LLM endpoint or model was not found | `llm_not_found` | `llm.base_url` and `llm.model`. |
| the LLM endpoint rate-limited the request | `llm_rate_limited` | Wait; `llm.max_retries`. |
| the request is too long for the model's context window | `llm_context_too_long` | `llm.context_window` is larger than the endpoint really accepts. |
| the LLM endpoint rejected the request | `llm_bad_request` | Unset the sampling keys named in the hint (`llm.temperature`, `llm.seed`, `llm.reasoning_effort`, `llm.max_output_tokens`). |
| the LLM endpoint reported an internal error | `llm_upstream` | The endpoint failed; see its logs. |
| the LLM request timed out | `llm_timeout` | Raise `llm.timeout_seconds` (default 300), or lower `diff.max_tokens`. |
| the LLM endpoint does not list the configured model; check llm.model | `llm_not_found` | The endpoint's model list (`GET {llm.base_url}/models`) has no entry whose `id` equals `llm.model` exactly. Fix `llm.model`, or set `llm.context_window` to skip the lookup. |
| the LLM endpoint does not report the model's context window; set llm.context_window | `llm_protocol` | `llm.context_window` is unset and the endpoint's entry for the model has none of `max_model_len`, `context_length`, `context_window`, `max_context_length` (training-size fields are never used). Set `llm.context_window` to the window your server runs; for Ollama, its `num_ctx`. |
| the LLM endpoint reports a context window below the 4096-token minimum; set llm.context_window if the endpoint is wrong | `llm_protocol` | 90 % of the reported window is below 4096. Use a model with a larger window, or, if the endpoint reports it wrongly, set `llm.context_window` (at least 4096). |
| too many background jobs are running; wait for one to finish | (job limit) | stdio only. Four `pr_review`, `pr_ask`, `pr_describe` or `pr_improve` runs are already going in the background. Collect one with `job_result`, or wait for it to finish; nothing ran for this call. |
| unknown or expired job_id | (job) | `job_result` got an id the server never issued, one older than 30 minutes after it finished, one evicted (only 64 results are kept), or one from before a restart. Jobs live in memory; run the review again. |
| wait_seconds must be an integer from 0 to 600 | (argument) | Fix `wait_seconds` (`pr_review`, `pr_ask`, `pr_describe`, `pr_improve`, `job_result`); the same range applies to `llm.wait_seconds`. |
| review-mcp is shutting down; no new background job can start | (shutdown) | The server received SIGINT or SIGTERM, or its client closed it. Restart it; running jobs were cancelled. |
| could not complete the request to the LLM endpoint | `llm_transport` | Network, DNS, TLS or proxy between you and `llm.base_url`. |
| the LLM endpoint sent an unexpected response | `llm_protocol` | `llm.base_url` is not an OpenAI-compatible endpoint, or the answer was empty. |
| the pull request diff does not fit the configured context window; raise llm.context_window (REVIEW_MCP_LLM_CONTEXT_WINDOW) or narrow the pull request | `diff_does_not_fit` | `llm.context_window`, `llm.max_output_tokens`, `diff.large_patch_policy`; or narrow the PR. Nothing was sent to the model. |
| the model's answer could not be parsed as a review, also after one retry | `review_unparseable` | Retry, or use a model that follows YAML output instructions. |
| the model's answer could not be parsed as a pull request description, also after one retry; try again, or check that llm.model follows the YAML output instructions | `describe_unparseable` | Retry, or use a model that follows YAML output instructions (`pr_describe`). |
| the model's answer could not be parsed as code suggestions, also after one retry; try again, or check that llm.model follows the YAML output instructions | `improve_unparseable` | Retry, or use a model that follows YAML output instructions (`pr_improve`). A failed self-review call is not this error; it leaves the suggestions unscored ([`pr_improve`](#pr_improve)). |
| output_language must be a locale code such as en-US or tr-TR | argument | Fix `output_language` (`pr_review`, `pr_ask`, `pr_describe`, `pr_improve`). |
| max_findings must be an integer from 1 to 20 | argument | Fix `max_findings` (`pr_review`). |
| question must not be empty | argument | Pass a non-blank `question` (`pr_ask`). |
| question is too long: at most 8000 characters are allowed | argument | Shorten the question. |
| publish_mode must be comment or description | argument | Fix `publish_mode` (`pr_describe`). |
| update_title needs publish=true and publish_mode=description | argument | Set both, or drop `update_title` (`pr_describe`). |
| no Gitea token in this request: set the X-Review-MCP-Gitea-Token header in your MCP client configuration | `credentials_missing` | Serve mode: the request carried no Gitea token. Add the header to the client configuration ([Serve mode](serve.md#client-configuration-for-a-remote-server)); `server_info` shows which headers arrived. No outbound request was made. |
| no Bitbucket Server token in this request: set the X-Review-MCP-Bitbucket-Server-Token header in your MCP client configuration | `credentials_missing` | Same, for a Bitbucket Server URL. |
| no GitHub token in this request: set the X-Review-MCP-GitHub-Token header in your MCP client configuration | `credentials_missing` | Same, for a GitHub URL. |
| no LLM API key in this request: set the X-Review-MCP-LLM-API-Key header in your MCP client configuration | `credentials_missing` | Same, for the LLM key. Only when `serve.llm_key_source = header`; with `server` the server's own key is used. |
| malformed credential header: <header name> | (HTTP 400) | A credential header is longer than 4096 bytes, contains anything but visible ASCII (a stray newline or a space inside the value) or was sent twice. Fix the value in the client configuration. The value is never echoed. |
| the server is busy: retry shortly | `server_busy` | All `serve.max_concurrent_calls` slots are in use. Retry, or raise the key (1 to 64). |
| unauthorized (HTTP 401, `WWW-Authenticate: Bearer`) | (HTTP) | The server has an access token and the request lacks `Authorization: Bearer <token>` or carries the wrong one: `REVIEW_MCP_SERVE_ACCESS_TOKEN`. |
| forbidden: invalid Host header / forbidden: origin not allowed (HTTP 403) | (HTTP) | Serve mode: a loopback listener got a `Host` that is not a loopback name with the configured port, or a request with an `Origin` that is not in `serve.allowed_origins` (exact, lower case). |
| request body too large (HTTP 413) | (HTTP) | The request body is over 1 MiB. |
| unexpected error; rerun with REVIEW_MCP_LOG_LEVEL=debug for details | (generic) | Anything not classified; debug logs say more. A `(class: timeout)` or `(class: canceled)` suffix means the call ran out of time or was canceled. |

## A client reports `-32001 Request timed out`

The MCP client gave up waiting for the tool call; the server did not fail.

- **stdio:** `pr_review`, `pr_ask`, `pr_describe` and `pr_improve` answer within `wait_seconds` (default 45)
  with a result or a `job_id`, so a call stays under a typical 60-second client
  timeout whatever the model speed. If you still see `-32001`, your client
  timeout is shorter than that: lower `llm.wait_seconds` (or pass a smaller
  `wait_seconds`), then call `job_result` with the `job_id`. A client that
  resets its timeout on progress notifications may never see the running
  status; that is expected ([Slow endpoints](review.md#slow-endpoints)).
- **serve:** there are no background jobs, so the call runs until the review is
  done. The client sets its own tool timeout: raise it to fit your model, and
  set `llm.timeout_seconds` to match ([Serve mode](serve.md#long-calls)).
- Either way, a faster model shortens the run. A smaller `diff.max_tokens`
  shortens one model call; a large pull request then takes more parts
  ([The review took several minutes](#the-review-took-several-minutes)).

## Token scopes

Scopes are named differently across versions; grant the least that works. The
[Setup guide](setup.md#2-create-tokens) says where to create each token and
lists every API endpoint review-mcp calls with the scope it needs; this section
is the short version and the place to look when a call fails with
`authentication failed`. All scopes are unconfirmed until V1 acceptance (item
A3) has verified them.

- **Gitea:** read access to the repository (`read:repository`), to issues
  (`read:issue`, for PR-level comments) and to your user (`read:user`, to
  identify the token's own comments), plus write access to issues
  (`write:issue`, for posting and editing PR comments) and to the repository
  (`write:repository`, for inline comments, which are posted as a review, and
  for `pr_describe` with `publish_mode=description`, which edits the pull
  request). `pr_improve` with `publish=true` needs the same as `pr_review` with
  `publish=true`: `write:issue` for the overview and `write:repository` for the
  inline comments. The write part is needed only for publishing (`publish`),
  `pr_comment_reply`, `pr_comment_create` and `diag comment`; reading a PR needs
  only read access.
- **GitHub:** a fine-grained personal access token with Pull requests
  (read; read and write for publishing, `pr_comment_reply`, `pr_comment_create`
  and the description edit), Contents (read) and Metadata (read), or a classic
  token with `repo` (`public_repo` for public repositories). Administration
  (read) is optional and only lets `pr_info` read classic branch protection.
  See [GitHub](github.md#token).
- **Bitbucket Server / Data Center:** an HTTP access token with repository
  read permission, plus write permission only for comments and for
  `pr_describe` with `publish_mode=description`, which updates the pull
  request (`pr_improve` with `publish=true` needs comment write, like
  `pr_review`). The token is sent
  as `Authorization: Bearer ...`; basic authentication is not supported.

Which call fails tells you which scope is missing: if `diag pr` works but
`diag comment` fails with `authentication failed`, the token is valid but
lacks the write scope; if `diag pr` itself fails, the token is wrong or
cannot read the repository (a hidden repository may also answer 404). Pass
tokens only through the environment (`REVIEW_MCP_GITEA_TOKEN`,
`REVIEW_MCP_BITBUCKET_SERVER_TOKEN`, `REVIEW_MCP_GITHUB_TOKEN`) or, in serve mode, the request headers
([Serve mode](serve.md)); never put them in a URL or a config file that is
checked in.

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
- **GitHub base and API URL.** `REVIEW_MCP_GITHUB_BASE_URL` is the web
  address pull request URLs start with and has no default. The API address
  is derived from it (`https://api.github.com` for exactly `https://github.com`,
  `{base}/api/v3` otherwise) unless `REVIEW_MCP_GITHUB_API_URL` is set;
  `server_info` shows the one in use. If calls fail with `not_found` or
  `protocol` on a GHES, check that `{base}/api/v3` is really the API.
- **Scheme, host and port must match exactly** (case-insensitive host; default
  ports 80/443 are implied). `https://your-gitea.example.evil.example` or a
  sibling path such as `/bitbucket-old` does not match `/bitbucket`.
- **CA certificates.** For a server with a private CA, set
  `REVIEW_MCP_GITEA_CA_CERT`, `REVIEW_MCP_BITBUCKET_SERVER_CA_CERT` or
  `REVIEW_MCP_GITHUB_CA_CERT` to the
  path of a PEM bundle; it is used in addition to the system roots. A
  `transport` error with the hint `TLS verification failed` usually means the
  CA is missing or the certificate does not cover the host name.
- **`insecure_skip_verify`.** `REVIEW_MCP_GITEA_INSECURE_SKIP_VERIFY=true` (and
  the Bitbucket Server and GitHub equivalents) turns off certificate verification. Use it
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

### Capturing the log in Windows PowerShell

In Windows PowerShell, `review-mcp diag pr <PR_URL> 2> debug.log` wraps the
first stderr line of the command in a `NativeCommandError` record, so the file
starts with PowerShell's own error text (the command name, `At line:...`,
`CategoryInfo`, `FullyQualifiedErrorId : NativeCommandError`) before the log
lines. This is how PowerShell treats the stderr of a native command; it is
harmless, the log lines follow it, and review-mcp has not failed. To get a
clean file, let `cmd.exe` do the redirection:

```powershell
$env:REVIEW_MCP_LOG_LEVEL = "debug"
cmd /c "review-mcp diag pr <PR_URL> 2> debug.log"
```

or turn each stderr record back into its text before writing it:

```powershell
review-mcp diag pr <PR_URL> 2>&1 | ForEach-Object { "$_" } | Out-File debug.log
```
