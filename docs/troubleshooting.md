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
review-mcp diag pr https://gitlab.example.com/group/sub/project/-/merge_requests/7
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
| `gitlab:diff_refs` | GitLab reported the diff base (`diff_refs.base_sha`); this is the only value on GitLab. |

### `skipped` reasons

A skipped file is not part of `files` and is not reviewed.

| Reason | Meaning |
|---|---|
| `binary` | The file is binary (on GitHub: no patch, and its extension is on the binary list; on GitLab: an empty diff and an extension on the binary list, or a diff that starts with `Binary files `). |
| `file_limit` | More files than `diff.max_files_full_content` (Bitbucket Server cannot build a patch without file contents). The result then has the note "N files skipped because of diff.max_files_full_content; raise it to review them." GitHub's own 3000-file listing limit is a note, not a skipped file (see [GitHub](#github)). |
| `size_limit` | A side of the file is larger than `diff.max_file_bytes` (Bitbucket Server); on GitHub, a file with line changes whose patch GitHub did not send because the diff is too large; on GitLab, a file GitLab marked `too_large` or `collapsed`. |
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
quote line `> Replying to @alice`. On GitLab a reply always lands inside the
thread (`"in_thread": true`), for general and inline discussions alike. This needs write access (see the token
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
| `file_limit` | `diff.max_files_full_content`: Bitbucket Server skips the files beyond that count. The note says "N files skipped because of diff.max_files_full_content; raise it to review them." and replaces the generic provider note when this is the only provider reason. | Raise the key (default 300, see [Diff limits](setup.md#diff-limits)), or split the pull request. |
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

## Findings and suggestions skipped as duplicates

| You see | Meaning and what to do |
|---|---|
| `pr_review`: `inline_status` `skipped_duplicate`, the note "1 finding was already posted on this PR and was not repeated." (plural: "N findings were already posted on this PR and were not repeated.") | An inline finding comment by the token's own user is already on the PR at the same place (the lines overlap, on the same file), or has the same fingerprint. This is the expected outcome of a second run. `inline_url` and the overview link to the earlier comment. |
| `pr_improve`: `anchor.status` `skipped_duplicate`, "already posted on this PR" in the overview, the note "1 suggestion was already posted on this PR and was not repeated." | An inline suggestion comment with the same key (file, existing code and improved code) by the token's own user is on the PR. `anchor.url` and the overview link to the earlier comment. |
| The finding or suggestion is not linked | The server did not report the earlier comment's URL; the overview then links to the file line. |
| A finding was posted again at a place that has a comment | That comment's thread is **outdated** (the code under it changed), or it is not by the token's user, or it has no finding marker, or it sits on the old path of a renamed file. These never suppress a finding. On Gitea and Bitbucket Server only the first line of an earlier range comment is known. |
| A finding was not posted, and you want it | Delete the earlier comment and run again. Resolving it is not enough, a resolved thread still counts; only an outdated thread lets a finding at the same place through. |

## Paths not seen in this review

| You see | Meaning and what to do |
|---|---|
| A finding ends with "Path not seen in this review: `p`" (or "Paths not seen in this review: `p`, `q`", at most 5, then "and N more"); `unverified_paths` lists them | The finding's title or body names `p` in a code span, and the review never showed the model that file: it is not in the diff, the repository context, a related section or hit, a file a repository tool read, the guidelines file or the file tree. The model may have made the path up from a similar one; look the file up before acting on it. A path the model wrote with `R<n>:` was checked against that related item only. See [Paths the review never saw](review.md#paths-the-review-never-saw). |
| A `pr_ask` answer ends with the same line; `answer_unverified_paths` lists them | The same check on the answer ([Paths the run never saw](ask.md#paths-the-run-never-saw)). |
| A path you know exists is marked | The run did not show it to the model, so the model named it from memory or by guess. Nothing in the text was changed; the mark says only that the review cannot confirm it. A path in a fenced code block is never checked. |
| A finding that carries the mark was posted again inline next to the same finding of 2.5.0-rc.1 | The added line is part of the finding's text, so a short finding gets another fingerprint than the comment posted without it. Delete one of the two comments. |

## CI, guidelines and file-tree notes

`pr_review` (and `pr_improve` for the guidelines and the tree) never fails
because the CI results, the guidelines file or the directory listing cannot be
used: each is a fixed note, a `coverage` status and the run goes on. The
sentences are fixed and never carry the cause; the cause is only in the debug
log as an error class (`REVIEW_MCP_LOG_LEVEL=debug`). See
[CI results](review.md#ci-results) and
[Project guidelines and file tree](review.md#project-guidelines-and-file-tree).

### CI

| Sentence | Where | Cause and what to do |
|---|---|---|
| The CI results of the head commit could not be read. | `ci.note` (`ci.state` is `unreadable`); the overview line is "CI status could not be read" | Reading the head commit's statuses or checks failed, for any reason. On GitHub the token needs **Commit statuses: Read** and **Checks: Read**; with only one of them the read is partial (next row), with neither it is `unreadable`. On GitLab the `api` scope (or the Reporter role) is enough; the pipelines of merged results and of a fork's project are not read, see [GitLab](#gitlab). On Gitea and Bitbucket Server the read access to the repository is enough, so check the token and the server ([Token scopes](#token-scopes)). `pr_info` shows the same sentence. The debug log line `ci: checks not read` names the error class. |
| Only part of the CI results could be read with this token (check runs or commit statuses). | `ci.note`; the overview line ends with "; only part of the CI results could be read with this token" | GitHub only: the token may read the check runs but not the commit statuses, or the reverse (one list answered 401, 403 or 404). `state` and the counts cover the list that was read, so a failing check in the other list is not seen. Give a fine-grained token both **Checks: Read** and **Commit statuses: Read** ([Token scopes](#token-scopes)); the debug log line `github checks: ... not readable` names which list and the error class. |
| The host reported more CI checks than could be read; the counts and the list are incomplete. | `ci.note`; the overview line ends with "; more checks exist than were read" | The commit has more than 500 checks. The counts and `state` cover what was read, so a failing check among the unread ones is not seen. Nothing to change in review-mcp; look at the CI page. |
| The CI results were left out of the prompt to stay within the CI token budget. | `notes` | The CI block did not fit `review.max_ci_tokens`, or the context window left no room for the diff beside it (the CI block is the last optional block to go). The `ci` object and the overview line are unaffected. Raise `review.max_ci_tokens`, or use a larger `llm.context_window`. |
| `CI: no checks reported` (overview line, `ci.state` `none`) | overview | The host has no statuses or checks for the head commit yet (CI not started, not configured, or reporting on another commit). Not an error. |

### Guidelines

| Sentence | Cause and what to do |
|---|---|
| None of the guidelines files in review.guidelines_paths exists at the target branch; the review ran without project guidelines. | You set `review.guidelines_paths` and none of the paths exists at the revision the diff is computed against (the target branch side, never the pull request's head; a file that only the pull request adds is not used). Merge the file to the target branch first, or fix the path. With the default paths this case is silent. |
| The project guidelines file at the target branch is larger than diff.max_file_bytes and was not used. | The first path that exists is too large; the next path is not tried. Shorten the file, or raise `diff.max_file_bytes`. |
| The project guidelines file could not be read from the target branch and was not used. | The read failed (a token without read access to the repository contents, a server error), or the file is not text (it holds a NUL byte), or no base revision was available. Check the token ([Token scopes](#token-scopes)); the debug log names the error class. |
| The project guidelines were cut at a line boundary to stay within review.max_guidelines_tokens. | The file is longer than the budget; the model got the first lines and was told that the rules go on. Raise `review.max_guidelines_tokens`, or move the important rules to the top. |
| The project guidelines were left out: not even their first line fits review.max_guidelines_tokens. | The budget is smaller than the header and one line. Raise `review.max_guidelines_tokens`. |
| The project guidelines were cut further to make room for the diff. | The diff did not fit beside the guidelines; they were cut (halved) until it did. Use a larger `llm.context_window`, or accept it: the diff wins. |
| The project guidelines were left out of the prompt to make room for the diff. | As above, and nothing of the guidelines was left. |

### File tree

| Sentence | Cause and what to do |
|---|---|
| The directories this change touches could not be listed at the target branch. | No touched directory could be listed (token without read access to the contents, a server error, or no base revision). `coverage.file_tree.status` is `unreadable`. Check the token ([Token scopes](#token-scopes)). |
| Some directories this change touches could not be listed at the target branch. | Some directories could not be listed; the block marks each with `(could not be listed)`, and after the first failure no further directory is requested (the same cause would fail them). Check the token and the debug log. |
| The file tree lists the 20 directories with the most changed files; 1 more directory was not listed. (plural: "...; N more directories were not listed.") | More than 20 directories hold changed files; only the 20 with the most changed files are listed. Not an error. |
| The listing of the directories this change touches was left out of the prompt to make room for the diff. | The diff did not fit beside the listing; the tree goes first (before the guidelines). Use a larger `llm.context_window`. |

A listing cut to `review.max_tree_tokens` has no note: `coverage.file_tree.status`
is `cut` and the block ends with "(The listing is cut here to fit the prompt.)".
`coverage.guidelines.status` and `coverage.file_tree.status` list the outcomes
([Coverage of the guidelines and the tree](review.md#coverage-of-the-guidelines-and-the-tree)).

### Configuration problems

An invalid value makes the configuration invalid at startup; `server_info` lists
the problems (the position, never the value of a path):

| Problem | What to do |
|---|---|
| review.max_ci_tokens: -1 must not be negative (0 turns the CI block off) | Use 0 or a positive number (the number shown is yours). |
| review.max_guidelines_tokens: -1 must not be negative (0 turns the guidelines off) | Same. |
| review.max_tree_tokens: -1 must not be negative (0 turns the file tree off) | Same. |
| review.guidelines_paths: 11 paths, at most 10 are allowed | List at most 10 paths. |
| review.guidelines_paths[0]: must not be empty / longer than 512 bytes / must be relative to the repository root (no leading "/") / must use "/" as the separator / must not contain control characters / must not contain an empty segment ("//" or a trailing "/") / must not contain "." or ".." segments | `[0]` is the position of the path in the list (counting from 0). Fix that path. In the environment the list is comma-separated, so an empty item between commas is an empty path. |

## Repository config notes

`pr_review`, `pr_improve`, `pr_describe` and `pr_ask` read the
[repository config](repo-config.md) (`.review-mcp.toml` at the target branch)
and add these fixed sentences first among the notes. The path in them is
`repo_config.path`; no value of the file is ever quoted. `repo_config.status`
in the result says what happened: `applied`, `nothing_applied`, `absent`,
`invalid`, `off` or `unreadable`. A missing file is the normal case and makes
no note.

| Sentence | Cause and what to do |
|---|---|
| Settings from `.review-mcp.toml` on the target branch were applied: output.language, ignore.glob. | The keys listed were applied, in allow-list order. Not a problem. A number listed may have been capped by the operator's value: numbers only tighten. |
| Settings in `.review-mcp.toml` that a repository may not change were ignored: llm.model, serve.listen. | The file sets keys outside the [allow-list](repo-config.md#allowed-keys), such as models, hosts, limits or secrets. They were not applied. Remove them from the file; an operator sets them in the server's own configuration. At most 10 names are shown, then "and N more". |
| The value of review.max_findings in `.review-mcp.toml` is not valid and was ignored. | The key is allowed, but its value has the wrong type (a string for a number, a single string for a list) or is out of the operator key's range. Also: a regex that does not compile, an unknown framework name, more than 5 guidelines paths, a `focuses` that is not a list of short words, a `docs_style` over 1,000 bytes. Fix the value; the other keys were applied. |
| The value of review.extra_instructions in `.review-mcp.toml` is longer than 2,000 bytes and was cut. | The text was cut at a character boundary to 2,000 bytes (the same for `ask.extra_instructions`). Shorten it. |
| `.review-mcp.toml` on the target branch could not be read as TOML (line N) and was ignored. | A syntax error at line N (an unclosed table header, a missing quote, a duplicate key). Nothing in the file was used (`invalid`). Fix the file on the target branch; a fix on the pull request's branch is not read. |
| `.review-mcp.toml` on the target branch is larger than 16 KiB and was ignored. | The file is over the 16 KiB cap (or over `diff.max_file_bytes`). Shorten it (`invalid`). |
| `.review-mcp.toml` on the target branch could not be read and was ignored. | The read failed: the token cannot read the repository's files, the host was unreachable or rate limited, or the pull request reports no base revision (`unreadable`). Check [connectivity](#check-connectivity-with-diag-pr); the run used the operator's configuration. |
| None of the guidelines files in review.guidelines_paths exists at the target branch; the review ran without project guidelines. | Also given when `review.guidelines_paths` came from the repository config and none of its paths exists. Fix the path in the file. |
| Repository context is off, so documentation was checked against the code in this pull request only. | A `focus=docs` review of a diff that changes documentation files only, with `context.repo.enabled` off. The model could not see the code the documentation describes, so it reported only what the pull request shows. Turn on [repository context](repo-context.md) (stdio) to let it look up the names the documentation mentions. |

**Settings changed only on the pull request have no effect.** The file is read
at the target branch, so a pull request that edits `.review-mcp.toml` is
reviewed with the version on the target branch. That is intended.

Configuration problems of the operator keys (at startup, in `server_info`):

| Problem | What to do |
|---|---|
| repo_config.path: must not be empty / longer than 512 bytes / must be relative to the repository root (no leading "/") / must use "/" as the separator / must not contain control characters / must not contain an empty segment ("//" or a trailing "/") / must not contain "." or ".." segments | Use a clean path relative to the repository root, such as `.review-mcp.toml` or `config/review.toml`. |
| review.docs_style: N bytes, at most 1000 are allowed | Shorten `review.docs_style` to 1,000 bytes. |

## Issue compliance notes

`pr_review` and `pr_describe` add these fixed sentences when they use the
issue a pull request implements ([Issue compliance](review.md#issue-compliance)).
`issue.refs` in the result gives each reference's outcome. Issue text is never
quoted in a note or a log. The table gives `pr_review`'s sentences;
`pr_describe`, which uses the issue as context and checks nothing, words three
of them for a description (the cut note is the same):

| `pr_review` | `pr_describe` |
|---|---|
| Issue #12 could not be read and was not used for the compliance check. | Issue #12 could not be read and was not used as context. |
| The issue did not fit the context window next to the diff; issue compliance was not checked. | The issue did not fit the context window next to the diff and was not used as context. |
| The pull request names PROJ-123; pass it as issue_context to check the change against it. | The pull request names PROJ-123; pass it as issue_context to use it as context. |

The causes and fixes are the same for both tools. In `pr_describe` an issue
that did not fit may also have been left out of the summary call of a
description in parts, where the files' summaries win over it.

| Sentence | Cause and what to do |
|---|---|
| Issue #12 could not be read and was not used for the compliance check. | The provider answered 404 or 403, or another error, for an issue the pull request names (outcome `unreadable`). A 404 does not mean the issue does not exist: the token may not read it (another repository, a private or confidential issue). Give the token read access to issues, or pass the issue as `issue_context`. The reference is shown as `#12` or `owner/repo#12`. |
| The issue text was longer than review.max_issue_tokens allows and was cut. | The issues together were over the budget; each was cut at a line boundary to its share (outcome `cut`). Raise `review.max_issue_tokens` (at most 20,000), or pass a shorter `issue_context`. |
| The issue did not fit the context window next to the diff; issue compliance was not checked. | The issue block would have made the diff shorter, so it was left out and no verdict was asked for. Lower `review.max_issue_tokens`, raise `llm.context_window`, or narrow the diff (`ignore.glob`). In `pr_describe` the issue was then not used as context. |
| The pull request was reviewed in parts; issue compliance needs the whole change at once and was not checked. | The diff did not fit one model call, so the review was made in parts, and no part sees the whole change. The issue was read but not shown to any part. Review a smaller change, raise `llm.context_window`, or narrow the diff. |
| The pull request names PROJ-123; pass it as issue_context to check the change against it. | The title, branch name or description names a tracker key, and no `issue_context` was given. review-mcp never reads trackers: fetch the issue in your client (for example through a tracker MCP server) and pass it as `issue_context`. One note per key, at most 5. Names of standards and encodings (UTF-8, SHA-256, ISO-8601, RFC, CVE, ...) are not keys. |
| The model's issue compliance answer was missing or could not be read; issue compliance was not reported. | The model left out `issue_compliance`, or gave it in another shape (an unknown verdict, a list that is not a list). The review itself is kept. Try again, or use a model that follows the schema more closely. |
| Issue compliance was judged on the changed files only. | An incremental review with an issue, when the last overview recorded no verdict (it had no issue, or it is older than 2.2.0-rc.2). The next incremental run has a recorded verdict. Run a full review (`incremental: false`) for a verdict on the whole pull request. |

A number that names a pull request (`#7` in the title of a follow-up, for
example) is skipped without a note (outcome `not_an_issue`). Bitbucket Server
has no issue tracker: nothing is read and no note is added.

`issue_context` errors (refused before anything is sent; `N` is the item,
counting from 1):

| Sentence | What to do |
|---|---|
| issue_context must hold 1 to 3 issues | Pass at most 3 items. |
| issue_context[N].key is required / issue_context[N].title is required | Every item needs a key and a title. |
| issue_context[N].key must be at most 64 characters | Shorten the key. |
| issue_context[N].title must be at most 300 characters | Shorten the title. |
| issue_context[N].description must be at most 20000 characters | Shorten the description. |
| issue_context[N].acceptance_criteria must be a string or a list of strings | Pass the criteria as one string or a list of strings. |
| issue_context[N].acceptance_criteria must be at most 10000 characters in total | Shorten the criteria. |
| issue_context[N].url must be an http or https URL | Pass the issue's web page, without credentials, or leave `url` out. |

Configuration problem (at startup, in `server_info`):

| Problem | What to do |
|---|---|
| review.max_issue_tokens: N is out of range (0-20000; 0 turns issue compliance off) | Use 0 to 20,000. |

## Related pull request notes

`pr_review` and `pr_ask` add these fixed sentences about the pull requests (and
[repositories](#related-repository-notes)) of the `related` argument ([Related pull requests](review.md#related-pull-requests-and-repositories));
`coverage.related` gives each one's status and reason. `<url>` is the URL you
gave, without its query and fragment. A related pull request's title, text or
diff is never quoted in a note or a log.

| Sentence | Cause and what to do |
|---|---|
| Related pull request <url> could not be read (<reason>) and was not used. | See the reasons below. The review ran without it. In an incremental review this also makes the run a full review (see below). |
| Related pull request <url> was ignored: it is the reviewed pull request itself or a duplicate. | The URL is the reviewed pull request (`primary`), or names the same pull request as an earlier item (`duplicate`), also through another path such as `.../pulls/7/files`. Nothing to do; leave it out. |
| Related pull request <url> did not fit in full; <n> of its files are listed by path only. | Its share of `related.max_tokens` held its contract files first and listed the rest by path (`partial`). Raise `related.max_tokens` (at most 40,000), name fewer related pull requests, or add the files that matter to `related.contract_globs`. |
| The related pull requests did not fit the context window next to the diff and were not used. | The block would have made the reviewed diff shorter, even with half the shares, so it was left out (`not_placed`, `did_not_fit`). Lower `related.max_tokens`, raise `llm.context_window`, or narrow the reviewed diff (`ignore.glob`). |
| The pull request was reviewed in parts; related pull requests need the whole change at once and were not used. | The reviewed diff did not fit one model call (`not_placed`, `review_in_parts`). Review a smaller change or raise `llm.context_window`. |
| related.max_tokens is 0, so related pull requests were not read. | The operator turned related pull requests off (`not_placed`, `off`). Set `related.max_tokens` above 0. |
| The model named a related pull request that was not in the review; the finding is treated as one on this pull request. | The model answered with an `R` number that no placed related pull request has. The finding is kept on the reviewed pull request, once per run whatever the number of such findings. |
| A related pull request changed since the last review, so the whole pull request was reviewed. | An incremental review whose related pull requests differ from the ones the last overview recorded: another set, a new head commit on one of them, `related` added or dropped, or one that could not be read now. **A related pull request that keeps failing makes every incremental run a full review**: fix its token or URL, or leave it out. **Dropping `related` from a later call does the same once**; the run after it is incremental again. |

A review with nothing to review gets no related note: its related pull
requests are `not_placed` with `nothing_to_review`, and the note of the empty
review says why.

Reasons of a related pull request that could not be read (`failed`):

| Reason | Cause and what to do |
|---|---|
| `url_not_configured` | The URL matches no configured provider. Nothing was sent to that host. Configure the provider, or correct the URL. |
| `url_malformed` | The URL is on a configured host but is not a pull request URL the provider understands. Use the pull request's web URL. |
| `credentials_missing` | Serve mode: the request carries no token for that provider. Add its `X-Review-MCP-…-Token` header ([Serve](serve.md#related-pull-requests)). |
| `not_found` | The provider answered 404. This does not mean the pull request does not exist: the token may not see that repository. |
| `forbidden` | The provider answered 403: the token may not read that repository. |
| `unauthorized` | The provider refused the token (401). |
| `unreadable` | Any other error (a server error, a timeout, an answer that could not be read). Try again. |

`related` errors (refused before anything is sent; `N` counts from 0):

| Sentence | What to do |
|---|---|
| related must have at most 4 items | Name at most 4 related pull requests. |
| related[N] must be an http or https pull request or repository URL without credentials | Pass the pull request's or repository's web URL, at most 2,048 bytes, without a user or password. |
| related cannot be combined with focus or findings | Run focused reviews and the merged publish without `related`. |

`pr_info` adds two information notes, not blockers: "Related pull request
<url> is merged while this pull request is open." and "Related pull request
<url> is closed without being merged."

`review_linked_prs` argument errors (`InvalidParams`): "pr_urls must hold 2 to
5 different http or https pull request URLs without credentials, separated by
commas, spaces or new lines", "repo_urls must hold different http or https
repository URLs without credentials, separated by commas, spaces or new lines,
and with pr_urls at most 5 in all" and "publish must be true or false".

Configuration problems (at startup, in `server_info`):

| Problem | What to do |
|---|---|
| related.max_tokens: N is out of range (0-40000; 0 turns related pull requests off) | Use 0 to 40,000. |
| related.contract_globs[N]: ... | Fix that glob as you would an `ignore.glob` entry. |
| related.max_identifiers: N is out of range (1-50) | Use 1 to 50. |

### Related repository notes

A repository in `related` ([Repository items and consumer
impact](review.md#repository-items-and-consumer-impact)) adds these fixed
sentences. `<repo>` is `namespace/name`, `<commit>` the first 12 digits of the
pinned commit; no path, branch, identifier or hit is ever quoted.

| Sentence | Cause and what to do |
|---|---|
| Related repository <url> could not be read (<reason>) and was not used. | See the reasons below. The review ran without it. |
| Related repository <url> was ignored: it is the reviewed pull request's own repository or a duplicate. | `primary_repository` (its contents are already repository context) or `duplicate` (same repository and branch). Leave it out. |
| No use of the changed identifiers was found in <repo> at <commit>. Text search does not see dynamic or generated uses, so this is not a statement that nothing is affected. | The search ran and found nothing (`no_use_found`). Read it as a limit of text search, not as "no impact": generated or dynamic uses are invisible to it. Check the removed names the section lists, and the stop-listed ones (below). |
| No removed or changed name was found in the contract files or exported symbols of this pull request, so the related repositories were not searched. | The reviewed diff gave no identifier (`not_placed`, `no_contract_change`), so nothing was fetched. This is true also when a contract file changed with added lines only (a new field breaks no existing consumer by name). If a contract file is not recognised, add it to `related.contract_globs`. The sentence may continue " Removed names too common to search: <names>." (at most 20): the only removed names were on the stop list (for example `name` or `type`), which is not searched; with other names to search, the repository section lists such names under `Removed but not searched (too common)` instead. |
| <n> of the changed identifiers were not searched (limit <max>). | More identifiers than `related.max_identifiers`. Raise it (at most 50), or review a smaller change. |
| Hits in <repo> were cut at <k> (limit). | The caps (5 hits per identifier, 30 per repository) kept <k> hits (`partial`). |
| Hits in <repo> did not fit in full; <s> of <m> are shown. | The repository's share of `related.max_tokens` held only <s> of <m> hits (`partial`). The section then ends with "<k> more hits were found but do not fit here." so the missing hits are not read as "no use". Raise `related.max_tokens`, or name fewer related items. |
| The repository config names related repositories, but reading them is turned off by the operator (related.from_repo_config). | The file declares `[[related.repos]]`, and a `pr_review` or `pr_ask` that would have read them did not, because the operator switch is off. Set the operator key `related.from_repo_config` to true to read them, or remove the declarations. There is no note when the switch changes nothing: a call with its own `related`, a focused `pr_review`, a merged publish, `pr_describe` and `pr_improve`. `related.repos` is still listed in `repo_config.ignored` in every case. |
| A consumer citation `R<n>:path:L-M` or `R<n>:path:L,M` is one link | The model cited several lines. The whole location is the link text and the link goes to the first line. With "(location not verified)" the run never saw that file; the text is kept whole. |

Reasons of a related repository that could not be read (`failed`), besides
`url_not_configured`, `url_malformed`, `credentials_missing` and `unreadable`
above:

| Reason | Cause and what to do |
|---|---|
| `repo_context_off` | `context.repo.enabled` is off, or the server runs in serve mode (stdio only). No git command ran. Turn repository context on. |
| `branch_not_found` | The remote has no such branch. A tree URL that names a path inside the branch is read as a longer branch name; use the branch root URL. |
| `unauthorized` | The token was refused (git cannot tell 401 from 403 once both authentication schemes failed). |
| `not_found` | The repository is not there, or the token cannot see it. |
| `timeout`, `too_large`, `redirect`, `git_unavailable`, `git_failed`, `busy`, `cache_unusable`, `unsupported` | The same fixed reasons as [repository context](troubleshooting.md#repository-context-was-skipped). |

For the `not_placed` reasons of a repository: `did_not_fit` (the diff is
already trimmed, or the repository's share does not hold its section),
`review_in_parts` and `nothing_to_review` as for pull requests, `off` for
`related.max_tokens` 0, and `no_contract_change` above. A repository that is
not placed is not fetched, so it has no `commit`.

Repository config problems: an invalid `[[related.repos]]` entry is named
`related.repos[i]` (`i` counts from 0) in `repo_config.ignored`, with the usual
invalid-key note; entries from the fifth on are invalid; a declared URL that is
malformed, names a pull request, or has a `branch` that differs from the
branch of its URL is `url_malformed` when read.

## Focus and findings refusals

`pr_review` checks the combination of `focus`, `findings`, `publish`,
`security_concerns` and `performance_concerns` before it builds anything. A
refusal is one of these fixed sentences (class `invalid_argument`); nothing was
sent to the provider or to the model. They are checked in this order.

| Sentence | Cause and what to do |
|---|---|
| focus must be one of security, architecture, performance, ux, tests or docs, or a description of 1 to 200 characters without line breaks or control characters | `focus` is blank after trimming, longer than 200 characters, or holds a line break, a control character or a bidirectional formatting character. Use a known value, or a one-line description. |
| findings cannot be combined with focus; call pr_review with findings and publish=true, without focus | `findings` is the merged result of focused runs, not a focused run. Drop `focus`. |
| a focused review does not publish; merge the findings and call pr_review with findings and publish=true | `focus` with `publish=true`. Run the focused reviews without `publish`, merge their findings, then publish once with `findings` ([Focused reviews](review.md#focused-reviews-and-the-merged-publish)). |
| findings are only used to publish; call pr_review with findings and publish=true | `findings` (also an empty list) without `publish=true`. Add `publish=true`, or drop `findings` for a normal review. |
| security_concerns and performance_concerns are only used with findings and publish=true | A concerns text without `findings`. Drop it, or send it with `findings`. |
| security_concerns and performance_concerns must be at most 5000 characters each | Shorten the text. |
| findings must hold at most 20 items (twice review.max_total_findings); merge repeats before publishing | More items than twice the call's effective `review.max_total_findings` (20 with the defaults; the number in the sentence is the limit of your call). Merge duplicates in the client, or raise `review.max_total_findings`. |

### Notes of a findings publish

| Sentence | Cause and what to do |
|---|---|
| These findings were supplied by the client (merged from focused reviews); no model was called for this publish. | Always present on a findings publish. Not a problem. |
| 1 supplied finding was dropped: a finding needs a changed file of the diff, positive lines that do not end before they start, a header and a description that are neither empty nor too long, and a valid focus when it has one. (plural: "N supplied findings were dropped: ...") | The item's `relevant_file` is not the new path of a file in the diff (or the file is filtered by your ignore rules), `start_line` is not positive or `end_line` is before it, `issue_header` is empty or over 200 characters, `issue_content` is empty or over 5000, or the item's `focus` is invalid. Check the paths against the diff (`pr_review` results carry them in `relevant_file`) and send the findings as the focused runs returned them. |
| 1 supplied finding repeated an earlier one at the same place with the same header and was merged into it. (plural: "N supplied findings repeated an earlier one at the same place with the same header and were merged into it.") | Two items had the same file, the same lines and the same header apart from case and white space; the first was kept. Not a problem. |
| N further findings were not shown because of review.max_total_findings. | More findings than the cap; the rest were not published ([Large pull requests](review.md#large-pull-requests)). Raise `review.max_total_findings`. |

A finding that is already on the pull request is skipped as a duplicate as for
any publish (see [Findings and suggestions skipped as
duplicates](#findings-and-suggestions-skipped-as-duplicates)); a second merged
publish adds nothing new.

## Incremental review notes

`pr_review` with [incremental review](review.md#incremental-review)
(`review.incremental` or `incremental=true`) adds one of these fixed sentences
to the notes when it does not do what you asked. `incremental.mode` in the
result says what happened: `full`, `incremental` or `unchanged`.

| Sentence | Cause and what to do |
|---|---|
| No earlier review by review-mcp was found on this pull request, so the whole pull request was reviewed. | You passed `incremental=true`, but the pull request has no overview of review-mcp with a recorded commit: no review was published yet (publish one with `publish=true`), the overview was written by an earlier version, it was deleted, or the token user is not the one who published it (only the token's own overview is read). The review is full (`mode` `full`). Not added when only `review.incremental` is set, because a first review is then the normal case. A [partial review](review.md#partial-reviews) records no commit, so after one the next run says this too. |
| Nothing changed since the last review (commit `abc1234`); nothing was reviewed. | The recorded commit is the current head. The model was not called and nothing was posted or edited, even with `publish=true` (`mode` `unchanged`, `metadata.llm_calls` 0). Not a problem. Push a commit, or run without `incremental` to review the whole pull request again. |
| No file of the pull request changed since the last review (commit `abc1234`); nothing was reviewed. | The head moved, but every file has the content it had at the recorded commit (a rebase, a force-push, an amended message, a reverted change). The model was not called and nothing was posted (`mode` `unchanged`). Not a problem. |
| The commit of the last review is no longer available, so the whole pull request was reviewed. | The recorded commit cannot be found any more, typically after a force-push and garbage collection on the host. The review is full (`mode` `full`) and its overview records the new head, so the next incremental run works. |
| The last review could not be compared with the current head, so the whole pull request was reviewed. | The comparison could not be made: the overview or the token's user could not be read, the recorded commit could not be read for another reason (a provider error, a rate limit, a timeout), or the head commit id was not a full id. The review is full. Check the provider's reachability ([Check connectivity](#check-connectivity-with-diag-pr)) and run again. |
| N findings of earlier reviews on unchanged files are carried forward; they were not reviewed again. | The last overview listed findings without an inline comment on files that did not change since, so this incremental run lists them again, marked "(from the review of `abc1234`, listed here only)" (`incremental.carried_findings`, `carried_from` on each). Not a problem. Run without `incremental` to review those files again ([What is carried forward](review.md#what-is-carried-forward)). |
| N findings listed only in the earlier overview were not carried into this publish. | A merged `findings` publish replaces the findings of the overview it edits with the client's; the earlier overview listed these without an inline comment (in hidden held lines), and they are not in the new overview. Run `pr_review` with `publish` (incremental or full) instead of a merged publish to keep them, or include them in the merged `findings` ([What is carried forward](review.md#what-is-carried-forward)). |
| N findings listed in this overview only will not be carried forward to a later incremental review. | A published overview records every finding without an inline comment in a hidden held line, at most 12,000 bytes in all; these did not fit, or their file or lines could not be recorded (for example a path the model wrote as `./a.go`). A later incremental run will not list them while their files stay unchanged. Turn inline findings on (`inline_findings`), lower `max_findings`, or review in full later. |
| The pull request has more than N files, so it was not compared with the last review and was reviewed in full. | The pull request lists more than `review.incremental_max_files` files (default 300; N is your value) and the API strategy compares at most that many. Raise `review.incremental_max_files` (up to 5000), or accept the full review. The limit does not apply when `git:diff` finds the changes ([the strategies](review.md#how-the-changed-files-are-found)). |

Two things are not notes. The result's `incremental.strategy` is `git:diff`
only when [repository context](repo-context.md) is on, this run fetched the
head and the cache still holds the recorded commit; otherwise it is
`api:contents`, which gives the same answer with more requests. And a review
that is partial has no "Reviewed commit" line: it records nothing, so the next
incremental run is a full review ([why](review.md#the-record-of-the-last-review)).

## Rate-limit and fallback notes

`pr_review`, `pr_improve`, `pr_describe` and `pr_ask` add these fixed
sentences to the notes of a result. `metadata.rate_limit_waits` and
`metadata.rate_limit_wait_ms` (always set) count the requests that waited for
the LLM endpoint's rate limit and their total wait; `metadata.model`,
`metadata.models_used` and `metadata.fallback_calls` (always set) say which
models served the run. Setup: [Rate limits](setup.md#rate-limits) and
[Fallback models](setup.md#fallback-models). The notes are in the tool result,
not in a comment the run published.

| Sentence | Cause and what to do |
|---|---|
| The LLM endpoint's rate limit delayed this run by N s. | The run waited at least one second in total for the endpoint's rate limit: for `llm.requests_per_minute` or `llm.max_concurrent_requests`, or for a cool-down after an HTTP 429. Not a problem. If it is too long for you, raise the gateway's limit, lower the number of runs at once, or add a [fallback model](setup.md#fallback-models). |
| N model calls were served by a fallback model (`second-model`) because the primary model was unavailable (rate limited). (singular: "1 model call was served by ...") | A fallback served N calls of the run. The model is the first fallback that served a call, and the reason in the brackets is that of the first move: `rate limited`, `server error`, `timed out`, `unreachable` or `unusable answer` (its answer stayed unusable after the re-ask). Check the primary model's endpoint for that reason. |
| The fallback model `x` was skipped for N calls because the prompt does not fit its context window. (singular: "... for 1 call ...") | The prompt plus the fallback's maximum output is larger than its context window, so it was not asked for that call and the next fallback was tried. Set `context_window` of that fallback to its real window, use a larger model, or accept the skip. |

| Error sentence | Cause and what to do |
|---|---|
| the LLM endpoint rate-limited the request. The endpoint asked for a wait longer than llm.max_rate_wait_seconds (N s). | The gateway's 429 (or its reset header) asked for a wait longer than `llm.max_rate_wait_seconds`, N being that key's value; the call is not delayed that long. This is the only rate-limit sentence with a wait: review-mcp's own queue (`llm.requests_per_minute`) is never refused, it just waits, and the delay note above reports it. Raise `llm.max_rate_wait_seconds` (up to 600), or configure a [fallback model](setup.md#fallback-models), which takes over in this case. No header value and no URL are in the sentence. |
| (the sentence of the first model's error) All N fallback models failed as well. | Every model tried failed. The sentence before is the **first** model's error (its class and HTTP status), and N is the number of fallbacks that were tried and failed (a fallback skipped for size is not counted). For example a fallback on another host without its own key answers 401; see [Fallback models](setup.md#fallback-models). |

**These failures do not move to a fallback.** The call ends with its own error
and no fallback is asked: authentication (`llm_auth`, HTTP 401 or 403), a refused
request (`llm_bad_request`), not found (`llm_not_found`; a wrong model name is a
configuration error and must surface, not hide behind a fallback), a request too
long for the model's context window (`llm_context_too_long`), an unexpected
response (`llm_protocol`) and a canceled call or an expired deadline. Fix the
configuration the sentence names.

### 429 from the gateway

| Symptom | What to do |
|---|---|
| The review fails with "the LLM endpoint rate-limited the request" (HTTP 429), mostly when several reviews run at once, for example the parallel focused runs of a multi-perspective review. | Set `llm.requests_per_minute` (`REVIEW_MCP_LLM_REQUESTS_PER_MINUTE`) to your gateway's per-minute limit, a little below it when other clients share the key. The calls then queue instead of failing. Optionally set `llm.max_concurrent_requests` too. See [Rate limits](setup.md#rate-limits). |
| It still happens with a limit set. | The limit covers one review-mcp process. Another process or client against the same gateway has its own; lower the value, or add a [fallback model](setup.md#fallback-models). |

## `multi_perspective_review` argument errors

The MCP prompt answers a bad argument with a JSON-RPC `InvalidParams` error and
one of these fixed messages (the value is never echoed):

| Message | Cause and what to do |
|---|---|
| pr_url must be the http or https URL of a pull request, without credentials | `pr_url` is missing, longer than 2048 bytes, not an absolute `http` or `https` URL with a host, or it carries credentials (`user:password@`). Pass the pull request's page URL. |
| focuses must be a comma-separated list of security, architecture, performance, ux, tests and docs | `focuses` holds a value that is not one of the six (free text is not accepted here; use it with `pr_review`'s `focus`), or no value at all. Leave `focuses` out for the default: `review.focuses` of the [repository config](repo-config.md#focus-defaults), else `security,architecture,performance,tests`, with `docs` added when the pull request changes a documentation file. |

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
| branch rules are not satisfied (for example required reviews, required checks or other restrictions) | GitHub's `blocked`: a branch rule is not satisfied, for example a required review or check is missing or failing, or another restriction applies (a ruleset's update restriction, say). GitHub does not say which; look at the pull request page. |
| the branch is behind the target branch | The repository requires the branch to be up to date. Update the branch. |
| the pull request is a draft | Mark it ready for review. |
| other merge check | A state review-mcp does not know. |

When `mergeable` is `null` and there are no blockers, GitHub has not computed
the merge state yet; run `pr_info` again in a moment.

The **note** `some checks that are not required are failing or pending` (in
`notes`) is GitHub's `unstable` state. It is not a blocker: the pull request is
mergeable, and only checks that are not required fail or are pending. Failing
required checks show as `branch rules are not satisfied (for example required reviews, required checks or other restrictions)`.

### Limits of what GitHub lists

| Note | Meaning and what to do |
|---|---|
| `GitHub lists at most 3000 files of a pull request: N more changed files were not listed, so they are not reviewed (file_limit).` (for one file: `... 1 more changed file was not listed, so it is not reviewed (file_limit).`) | The pull request has more than 3000 changed files, which is GitHub's limit for the file list. N files are not reviewed and have no name in the result. Split the pull request or review them by hand. |
| `GitHub lists at most 250 commits of a pull request; the commits past them are not available.` | Only the first 250 commits are listed. `pr_info` shows this note; `pr_describe` has its own, below. |
| `The pull request has N commits, but only M commit messages could be read; the description used those.` (M is "1 commit message" for one) | `pr_describe` on a pull request whose commit count is larger than the number of messages the provider returned (on GitHub, past 250). The description is still made, from the messages that were read. Read the rest of the history by hand if it matters. Gitea and Bitbucket Server never show it. |

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

## GitLab

What to check when a GitLab merge request does not work as expected. The full
behaviour is in [GitLab](gitlab.md); the sentences below are exact.

### Rate limited

```text
the server rate-limited the request (HTTP 429): retry after 2026-10-09 12:30:00 UTC
```

GitLab answers `429`, or `403` with `RateLimit-Remaining: 0`, when a token
makes too many requests. The time is the reset, in UTC. review-mcp itself
waits at most once, only when the reset is 60 seconds or less away, and then
repeats the request one time. **What to do:** wait until the time shown and run
the tool again, and do not start several reviews at once with one token. When
the sentence ends at `(HTTP 429)` or `(HTTP 403)` with no time, GitLab sent no
usable reset. A `403` without `RateLimit-Remaining: 0` is an authentication
problem instead, see below.

### Authentication failed on GitLab

`authentication failed: check the token and its scopes (HTTP 401)` or
`(HTTP 403)`. **What to do:** check that the token is valid and has not
expired. A personal access token must be a legacy one with the scopes `api`
(read, publish, reply, create comments, edit the description) and
`read_repository` (repository context); a project or group access token needs
the role Reporter (read) or Developer (publish) and belongs to one project or
group only. A
project or group the token cannot see answers `the requested resource was not
found` (HTTP 404). See [GitLab](gitlab.md#token).

**A fine-grained personal access token, or `read_api`.** In testing GitLab
refused both with this same 401 or 403, although the token was valid. Create a
**legacy personal access token** with the scopes `api` and `read_repository`
([Setup, GitLab](setup.md#gitlab)) and set it as `REVIEW_MCP_GITLAB_TOKEN`.

### The diff is not ready

```text
the server reported an internal error: the merge request's diff is not ready yet; try again in a moment
```

GitLab fills the diff of a merge request asynchronously: right after it is
created, or after a push, `diff_refs` is empty or still names an older head. The
read is retried once after 2 seconds; this sentence means it was still not
ready. **What to do:** run the tool again in a moment. `pr_comments` can fail the
same way for a few seconds after a push; posting a comment does not wait for the
diff. If it never clears, open the merge request in the browser and check that GitLab shows its changes.

### Version

```text
the server version is not supported: GitLab 16.0 or newer is required
```

An endpoint answered `404` although the merge request itself could be read, so
the instance is older than GitLab 16.0 (review-mcp reads the diff from
`GET .../diffs` and needs `detailed_merge_status`). **What to do:** upgrade
GitLab; there is no fallback to the older `/changes` endpoint.

### A link points outside the API base

```text
the server sent an unexpected response: a link in the response points outside the configured API base
```

Paging follows the `Link` header, and only a next link on the configured API
base (`{gitlab.base_url}/api/v4`) is followed; any other link is refused so
that the token is never sent to another host. This appears on a list with more
than one page (the changed files, the discussions, the commit statuses) when a
reverse proxy sits in front of GitLab, or when the instance's `external_url`
differs from `gitlab.base_url` in scheme, host, port or context path: GitLab
builds the `Link` header from its own `external_url`. **What to do:** set
`REVIEW_MCP_GITLAB_BASE_URL` to the address in GitLab's `external_url`, or
change `external_url` to the address review-mcp uses. `server_info` shows the
API base in use. The links to comments (`web_url`) come from GitLab too, so
behind such a proxy they can differ from the URL you typed; see
[GitLab](gitlab.md#a-proxy-or-an-external_url-that-differs-from-the-base-url).

### Limits of what GitLab lists

| Note | Meaning and what to do |
|---|---|
| `GitLab did not list every changed file (its diff limit was reached); the files it left out are not reviewed.` | The merge request is so large that GitLab stopped listing changed files (`changes_count` ends in `+`). The listed files are reviewed; the others are not, have no name in the result and are not counted in the coverage. Split the merge request or review them by hand. |
| `review-mcp reads at most 4000 changed files of a merge request; the files GitLab listed after them are not reviewed.` | GitLab listed more than 40 pages of 100 files. The files past the cap are not reviewed. Split the merge request. |

### Inline comments that GitLab could not place

These are the `error` of an inline comment (`pr_review`, `pr_improve`,
`pr_comment_create` with a file and a line) in the publish result.

| Sentence | Meaning and what to do |
|---|---|
| `GitLab could not place the inline comment on its line; the general comment it created instead was removed` | Reason `unanchorable`. GitLab answered `201` with a general note instead of a diff note, and review-mcp deleted that note at once. The line may not be on the merge request's diff (the head moved on, or the line is outside the changes). Run again after reading the merge request again; the finding stays in the overview. |
| `an inline comment GitLab could not place was posted as a general comment and could not be removed` | Reason `failed`. The same, but the note could not be deleted (the token may not delete its own notes, or the request failed). **A general comment is still on the merge request:** delete it by hand. |
| `the server sent an unexpected response: the created discussion's note was not reported` | GitLab answered `201` for the discussion but named no note, so the outcome cannot be checked. Look at the merge request for the comment. |
| `the request conflicts with the current state on the server: the merge request's head changed; read it again` | The merge request's head moved between the read and the post. Run the tool again. |

### `pr_info` on GitLab

| Sentence | Meaning and what to do |
|---|---|
| `no approval rule applies to this merge request` (`required_approvals` is `0`) | GitLab reported `approvals_required` of 0: no approval rule applies. Not an error. |
| `not readable with this token` (`required_approvals` is `null`) | `/approvals` could not be read or has no `approvals_required`. The reviewers from `/reviewers` are still listed, without approvers who are not reviewers. Check the token's scope (`api`) and the role. |
| `The reviews could not be read, so the reviewers are not listed.` | `/reviewers` failed with something other than a `404`. The merge and approval facts are still returned. |
| `The merge status could not be read.` | An open merge request came without `detailed_merge_status`. `mergeable` is `null`; run again. |

`merge_blockers` are fixed texts from `detailed_merge_status`:

| Blocker | Cause and what to do |
|---|---|
| merge conflict | `conflict` or `broken_status`. Rebase or merge the target. |
| required builds are missing or failing | `ci_must_pass`, `ci_still_running`, `external_status_checks` or `security_policy_pipeline_check`. Look at the pipeline page. |
| unresolved discussions must be resolved | `discussions_not_resolved`. The project requires every thread to be resolved ("All threads must be resolved") and one is open. |
| the pull request is a draft | `draft_status`. Mark it ready. |
| the branch is behind the target branch | `need_rebase`. Rebase the branch. |
| required approvals missing | `not_approved`. |
| a reviewer marked the pull request as needs work | `requested_changes`. |
| other merge check | Any other value (`not_open`, `blocked_status`, `merge_request_blocked`, `jira_association_missing`, `title_regex`, `merge_time`, `locked_paths`, or one review-mcp does not know). GitLab names a single reason; look at the merge request page. |

When `mergeable` is `null` and there are no blockers, GitLab has not computed the
merge status yet (`unchecked`, `checking`, `approvals_syncing`, `preparing`);
run `pr_info` again in a moment.

### The description has a line GitLab could run as a quick action

```text
the description has a line GitLab could run as a quick action; publish in comment mode instead
```

`pr_describe` with `publish_mode=description` was refused before any write.
GitLab runs a quick action (a line that starts with `/` followed by a letter, a
digit or an underscore, after up to three spaces: `/label ~x`, `/close`) when a
description is updated, and review-mcp never changes the author's text, so it
cannot make that line harmless. **What to do:** publish in comment mode
(`publish_mode=comment`), or edit the description so that no line outside
review-mcp's region starts that way. The refusal also applies to a run that
would only change the title. See
[GitLab](gitlab.md#quick-action-safety).

### A suggestion is shown as a diff, not as a block

```text
Note: a line of the suggested code starts with a slash command, so it is shown as a diff.
```

This paragraph is in an inline comment of `pr_improve` that has no "Apply
suggestion" button. A line of the suggested code starts with `/` followed by a
letter, a digit or an underscore, which GitLab could run as a quick action if it
were published, so the suggestion is shown as the fenced `diff` block instead. It
is a reading aid: copy the change by hand. A code line such as `// comment` does
not cause this.

### Configuration sentences (GitLab)

| Sentence | What to do |
|---|---|
| `REVIEW_MCP_GITLAB_TOKEN is required because gitlab.base_url is set` | Stdio: set the token. |
| `REVIEW_MCP_GITLAB_TOKEN is set but gitlab is not enabled (gitlab.base_url is unset); the token is ignored` | Warning: set `REVIEW_MCP_GITLAB_BASE_URL`, or unset the token. |
| `TLS verification disabled for gitlab` | Warning: `REVIEW_MCP_GITLAB_INSECURE_SKIP_VERIFY` is set. Prefer `REVIEW_MCP_GITLAB_CA_CERT`. |
| `no provider enabled: set gitea.base_url (...), bitbucket_server.base_url (...), github.base_url (...) and/or gitlab.base_url (...)` | No provider has a base URL; set at least one. |

A GitLab URL that reports `url_not_configured` although it looks right: check
that `gitlab.base_url` is the web address (`https://gitlab.com` for the public
product, your own address, with its context path, otherwise), and that the
scheme and port match. A URL without `/-/` before `merge_requests`, or with a
tail other than `/diffs`, `/commits` or `/pipelines`, is `url_malformed`.

## Repository tool notes

`pr_review` and `pr_ask` add these fixed sentences when the model could read the
repository through tools ([Repository tools](repo-context.md#repository-tools)).
`coverage.repo_context.tools` gives the status, the reason, the rounds and every
call; `metadata.tool_rounds` counts the rounds. The notes are in the tool result
and in a published overview or answer.

| Sentence | Cause and what to do |
|---|---|
| The LLM endpoint does not accept tool calls, so the review used the diff and repository context without reading further files. | The endpoint or model refused tool calls (`endpoint_no_tools`). The run was single-pass, and this process remembers it for that endpoint and model: restart the server after you move to a model that supports tools, or set `context.repo.max_tool_rounds` to 0. `server_info` shows `refused`. |
| The review was made in parts, so repository tools were not used. | The diff did not fit one model call (`review_in_parts`). Tools work in a review in one call only; raise `llm.context_window` or review a smaller change. |
| The repository tool budget was used up after N calls. | A tool result did not fit the rest of `context.repo.max_tool_tokens` or the room beside the diff (`budget_exhausted`); N counts the calls that ran. The model then answered with what it had. Raise `context.repo.max_tool_tokens` (at most 40,000) or `llm.context_window`. |
| The diff left no room for repository tools, so the review was made without them. | The tools' instruction and definitions do not fit beside the whole diff, or the diff was trimmed already (`no_room`). The diff always wins. Raise `llm.context_window` or narrow the diff. |
| A model call failed while the model was reading the repository through tools, so the review was made without them. | A model call of the conversation failed in a way that ends the rounds (a second restart on the next model, or a context-length error), and the answer came single-pass (`conversation_failed`). Check the endpoint; see the [fallback notes](#rate-limit-and-fallback-notes). |

Inside a search result the tool says how many files it could not search:

| Sentence | Cause and what to do |
|---|---|
| 1 file was not searched: it was not fetched. / N files were not searched: they were not fetched. | The cache lacks those files' contents (over 1 MiB, never fetched), so text search could not look in them. The counts read "1 match", "1 entry" and "the file has 1 line" the same way. Read such a file through the provider if it matters. |

Reasons without a note: `off` and `repo_context_off` (the tools are off; with
rounds above 0 and repository context off `server_info` warns at startup),
`repo_context_unavailable` (see [Repository context was
skipped](#repository-context-was-skipped)) and `no_model_call` (the run made
no model call).

Status of one call (`coverage.repo_context.tools.calls[].status`):

| Status | Cause and what to do |
|---|---|
| `ok` | The call ran. |
| `not_allowed` | The model asked for a path outside the repository rules (`..`, a leading `/`, `\`, a `.git` segment, control characters, over 4,096 bytes). Nothing was read. Expected when a pull request text tries to send the model elsewhere. |
| `not_available` | Binary file, a file the partial clone lacks (over 1 MiB, never fetched), a path that does not exist at the head, a path that is not a directory, a start line past the end of the file, or a repository read that failed. |
| `too_large` | The file is over `diff.max_file_bytes`. |
| `budget_exhausted` | The result did not fit; not run. See the budget note above. |
| `invalid_arguments` | The arguments were not a JSON object of at most 4 KiB, a required one was missing, a range was invalid, or the tool does not exist. |
| `too_many_calls` | More than four calls in one round; the extra ones were not run. |

Each round is a model call: a slow review with tools is usually the rate limit
(`metadata.rate_limit_waits`) or a slow endpoint ([Rate limits](setup.md#rate-limits)).
Set `context.repo.max_tool_rounds` lower, or to 0.

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
| `client_findings` | The call was a [publish of findings supplied by the client](review.md#focused-reviews-and-the-merged-publish); no prompt is sent, so no repository context is read. Not a problem. |

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
| the LLM endpoint rate-limited the request | `llm_rate_limited` (HTTP 429) | The endpoint is cooled down for every caller and the call is retried after the cool-down, up to `llm.max_rate_retries` times (apart from `llm.max_retries`). To avoid it, set `llm.requests_per_minute`; see [429 from the gateway](#429-from-the-gateway). The sentence with an endpoint wait longer than `llm.max_rate_wait_seconds` is under [Rate-limit and fallback notes](#rate-limit-and-fallback-notes). |
| the request is too long for the model's context window | `llm_context_too_long` | `llm.context_window` is larger than the model really accepts; lower it. |
| the LLM endpoint rejected the request | `llm_bad_request` | Often an unsupported sampling setting; the hint names the keys you set (`llm.temperature`, `llm.seed`, `llm.reasoning_effort`, `llm.max_output_tokens`). Unset them. |
| the LLM endpoint reported an internal error | `llm_upstream` (HTTP 5xx) | The endpoint failed; see its logs. Retried up to `llm.max_retries` times, then a [fallback model](setup.md#fallback-models) is asked when one is configured. |
| the LLM request timed out | `llm_timeout` | The default `llm.timeout_seconds` is 300; raise it, lower `diff.max_tokens`, or use a faster model. Large reviews on slow models take minutes. |
| the LLM endpoint does not list the configured model; check llm.model | `llm_not_found` | The endpoint's model list (`GET {llm.base_url}/models`) has no entry whose `id` equals `llm.model` exactly. Fix `llm.model`, or set `llm.context_window` to skip the lookup. |
| the LLM endpoint does not report the model's context window; set llm.context_window | `llm_protocol` | `llm.context_window` is unset and the endpoint's entry for the model has none of `max_model_len`, `context_length`, `context_window`, `max_context_length` (training-size fields are never used). Set `llm.context_window` to the window your server runs; for Ollama, its `num_ctx`. |
| the LLM endpoint reports a context window below the 4096-token minimum; set llm.context_window if the endpoint is wrong | `llm_protocol` | 90 % of the reported window is below 4096. Use a model with a larger window, or, if the endpoint reports it wrongly, set `llm.context_window` (at least 4096). |
| could not complete the request to the LLM endpoint | `llm_transport` | Network, DNS, TLS or proxy problem between you and the endpoint. |
| the LLM endpoint sent an unexpected response | `llm_protocol` | The endpoint is not OpenAI-compatible at `llm.base_url`, or it answered with an empty message. |
| the pull request diff does not fit the configured context window | `diff_does_not_fit` | Raise `llm.context_window`, or narrow the PR. Nothing was sent to the model. Applies to `pr_review`, `pr_ask`, `pr_describe` and `pr_improve`. |
| the model's answer could not be parsed as a review, also after one retry | `review_unparseable` | Try again, or use a model that follows YAML output instructions. |
| output_language must be a locale code such as en-US or tr-TR / max_findings must be an integer from 1 to 20 / wait_seconds must be an integer from 0 to 600 | (argument) | Fix the argument; nothing was sent anywhere. |
| the `focus` and `findings` sentences, for example "a focused review does not publish; merge the findings and call pr_review with findings and publish=true" | `invalid_argument` | See [Focus and findings refusals](#focus-and-findings-refusals); nothing was sent anywhere. |

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
| the description has a line GitLab could run as a quick action; publish in comment mode instead | GitLab only. The author's text of the description (outside review-mcp's region) has a line that starts with `/` and a letter, a digit or an underscore, after up to three spaces, which GitLab would run when the description is updated. Nothing was written, also when only the title would change. Publish in comment mode, or remove the line; see [GitLab](#the-description-has-a-line-gitlab-could-run-as-a-quick-action). |
| This provider does not support editing the pull request description; use publish_mode=comment. | The provider has no description edit (the capability `DescriptionEdit`). Gitea, Bitbucket Server, GitHub and GitLab all have it, so you see this only with a provider that does not. Use `publish_mode=comment`. No request was made. |
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
unchanged. On GitLab a draft is also a title prefix, and the known prefixes are
`Draft:`, `[Draft]` and `(Draft)` as well as `WIP:` and `[WIP]`, in any case; the
same two rules apply. GitLab's own setting is fixed, so no prefix is unknown
there.

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
| The guidelines and file-tree sentences ("None of the guidelines files in review.guidelines_paths exists at the target branch; ...", "The project guidelines were cut ...", "The directories this change touches could not be listed at the target branch." and the others) | The same as in `pr_review`: see [CI, guidelines and file-tree notes](#ci-guidelines-and-file-tree-notes). |
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
| N suggestions were already posted on this PR and were not repeated. (singular: "1 suggestion was already posted on this PR and was not repeated.") | An inline comment with the same key (file, existing code and improved code) by the token's user is on the PR, from an earlier run. This is the expected outcome of a second run. The suggestion's `anchor.url` and the overview link to that comment. To get a fresh inline comment for a suggestion, delete the old one first. |
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
| the pull request URL does not match any configured provider | `url_not_configured` | The URL's scheme, host, port or path prefix matches no configured base URL (or Gitea `web_url`). Compare it with `REVIEW_MCP_GITEA_BASE_URL` / `REVIEW_MCP_BITBUCKET_SERVER_BASE_URL` / `REVIEW_MCP_GITHUB_BASE_URL` / `REVIEW_MCP_GITLAB_BASE_URL`, including `http` vs `https`, the port, and a Bitbucket context path. No request was sent. |
| the URL matches a configured provider but is not a pull request URL | `url_malformed` | Use the PR page URL: `.../{owner}/{repo}/pulls/{n}` (Gitea), `.../{owner}/{repo}/pull/{n}` (GitHub), `.../{namespace}/{project}/-/merge_requests/{iid}` (GitLab; the `/-/` is required, and only `/diffs`, `/commits` or `/pipelines` may follow the number) or `.../projects/{KEY}/repos/{slug}/pull-requests/{id}` (Bitbucket Server; `/users/{user}/repos/...` for personal repositories). Do not put credentials in the URL. |
| authentication failed: check the token and its scopes | `auth` (HTTP 401/403) | The token is wrong, expired, or lacks a scope; see the token scopes below. Also check that the token belongs to the provider whose URL you used. |
| authentication failed: check the token and its scopes (GitLab, with a fine-grained personal access token or the `read_api` scope) | `auth` (HTTP 401/403) | GitLab refused both in testing although the token was valid. Use a legacy personal access token with `api` and `read_repository` ([Setup, GitLab](setup.md#gitlab), [Authentication failed on GitLab](#authentication-failed-on-gitlab)). |
| the requested resource was not found | `not_found` (HTTP 404) | The repository or PR does not exist, or the token's user cannot see it (many servers answer 404 for a hidden repository). On Bitbucket Server, a wrong context path also gives 404. |
| the server rate-limited the request | `rate_limited` (HTTP 429, or 403 on GitHub and GitLab) | Wait and retry; check any rate limits or a reverse proxy in front of the server. On GitHub and GitLab the sentence ends with the reset time when it is known; see [GitHub](#github) and [GitLab](#gitlab). |
| the server reported an internal error | `upstream` (HTTP 5xx) | The provider (or a proxy in front of it) failed. Look at the server's own logs. On GitLab the hint `the merge request's diff is not ready yet; try again in a moment` is not a server failure; see [GitLab](#the-diff-is-not-ready). |
| a response exceeded its size limit | `too_large` | The hint names the limit: `diff.max_diff_bytes` or `diff.max_file_bytes`; raise it, or review a smaller PR. The hint `(json response limit)` is a fixed 10 MiB safety cap on JSON responses and is not configurable; if you hit it, please report it. |
| the server version is not supported | `unsupported_version` | Bitbucket Server / Data Center 7.0 or later is required; GitLab 16.0 or newer (the hint `GitLab 16.0 or newer is required`, see [GitLab](#version)). |
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
| the pull request URL does not match any configured provider | `url_not_configured` | The URL matches no provider's base URL (or Gitea `web_url`). Check `REVIEW_MCP_GITEA_BASE_URL` / `REVIEW_MCP_BITBUCKET_SERVER_BASE_URL` / `REVIEW_MCP_GITHUB_BASE_URL` / `REVIEW_MCP_GITLAB_BASE_URL`: scheme, host, port, context path. |
| the URL matches a configured provider but is not a pull request URL | `url_malformed` | Use the PR page URL; no credentials in the URL. |
| authentication failed: check the token and its scopes | `auth` | Wrong, expired or under-privileged provider token. Check `REVIEW_MCP_GITEA_TOKEN` / `REVIEW_MCP_BITBUCKET_SERVER_TOKEN` / `REVIEW_MCP_GITHUB_TOKEN` / `REVIEW_MCP_GITLAB_TOKEN` (stdio) or the `X-Review-MCP-...-Token` header (serve), and [Token scopes](#token-scopes). |
| the requested resource was not found | `not_found` | Wrong repository or PR, a token that cannot see it, or a wrong Bitbucket context path. |
| the server rate-limited the request | `rate_limited` | Provider or proxy rate limit; wait and retry. GitHub and GitLab add `(HTTP 403)` or `(HTTP 429)` and, when known, `: retry after <time> UTC`. |
| the server reported an internal error | `upstream` | Provider (or its proxy) failed; see its logs. |
| a response exceeded its size limit | `too_large` | Check `diff.max_diff_bytes` / `diff.max_file_bytes` (named in the hint). |
| the server version is not supported | `unsupported_version` | Bitbucket Server / Data Center 7.0 or later is required; GitLab 16.0 or newer (the hint `GitLab 16.0 or newer is required`, see [GitLab](#version)). |
| could not complete the request to the server | `transport` | DNS, timeout, TLS or connection problem; see the hint and [Base URL notes](#base-url-notes). |
| the server sent an unexpected response | `protocol` | Base URL points at something that is not the provider API (for GitHub also `github.api_url`); the hint `the comment id is ambiguous on GitHub` is explained under [GitHub](#github); the hints `a link in the response points outside the configured API base` and `the created discussion's note was not reported` under [GitLab](#a-link-points-outside-the-api-base); a hint such as `empty body` or `invalid comment id` means `pr_comment_reply` got a blank `body` or a `comment_id` that is not a positive integer. |
| the LLM endpoint rejected the credentials | `llm_auth` | `REVIEW_MCP_LLM_API_KEY` (stdio, or serve with `llm_key_source = server`) or the `X-Review-MCP-LLM-API-Key` header. |
| the LLM endpoint or model was not found | `llm_not_found` | `llm.base_url` and `llm.model`. |
| the LLM endpoint rate-limited the request | `llm_rate_limited` | Set `llm.requests_per_minute`; see [429 from the gateway](#429-from-the-gateway). |
| the request is too long for the model's context window | `llm_context_too_long` | `llm.context_window` is larger than the endpoint really accepts. |
| the LLM endpoint rejected the request | `llm_bad_request` | Unset the sampling keys named in the hint (`llm.temperature`, `llm.seed`, `llm.reasoning_effort`, `llm.max_output_tokens`). |
| the LLM endpoint reported an internal error | `llm_upstream` | The endpoint failed; see its logs. |
| the LLM request timed out | `llm_timeout` | Raise `llm.timeout_seconds` (default 300), or lower `diff.max_tokens`. |
| the LLM endpoint does not list the configured model; check llm.model | `llm_not_found` | The endpoint's model list (`GET {llm.base_url}/models`) has no entry whose `id` equals `llm.model` exactly. Fix `llm.model`, or set `llm.context_window` to skip the lookup. |
| the LLM endpoint does not report the model's context window; set llm.context_window | `llm_protocol` | `llm.context_window` is unset and the endpoint's entry for the model has none of `max_model_len`, `context_length`, `context_window`, `max_context_length` (training-size fields are never used). Set `llm.context_window` to the window your server runs; for Ollama, its `num_ctx`. |
| the LLM endpoint reports a context window below the 4096-token minimum; set llm.context_window if the endpoint is wrong | `llm_protocol` | 90 % of the reported window is below 4096. Use a model with a larger window, or, if the endpoint reports it wrongly, set `llm.context_window` (at least 4096). |
| too many background jobs are running or waiting; wait for one to finish | (job limit) | stdio only. Four `pr_review`, `pr_ask`, `pr_describe` or `pr_improve` runs go on in the background and 32 more wait for a slot. Collect one with `job_result`, or wait for one to finish; nothing ran for this call. |
| The review is waiting for a free slot (position N in the queue). Call `job_result` with job_id `…` to get the result. | (queued; also "answer", "description", "suggestion job") | Not an error. Four runs were going when this one started, so it waits its turn (the five focus areas of a multi-perspective review do this). `wait_seconds` counts the wait. Call `job_result` with the job id; it reports the same status until a slot is free, then the result. |
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
| focus must be one of security, architecture, performance, ux, tests or docs, or a description of 1 to 200 characters without line breaks or control characters | `invalid_argument` | Fix `focus` (`pr_review`). |
| findings cannot be combined with focus; call pr_review with findings and publish=true, without focus | `invalid_argument` | Drop `focus` or `findings` (`pr_review`). |
| a focused review does not publish; merge the findings and call pr_review with findings and publish=true | `invalid_argument` | Drop `publish`, or merge the findings and send them as `findings` ([Focus and findings refusals](#focus-and-findings-refusals)). |
| findings are only used to publish; call pr_review with findings and publish=true | `invalid_argument` | Add `publish=true`, or drop `findings`. |
| security_concerns and performance_concerns are only used with findings and publish=true | `invalid_argument` | Send them with `findings`, or drop them. |
| security_concerns and performance_concerns must be at most 5000 characters each | `invalid_argument` | Shorten the text. |
| findings must hold at most N items (twice review.max_total_findings); merge repeats before publishing | `invalid_argument` | Merge duplicates, or raise `review.max_total_findings`. |
| question must not be empty | argument | Pass a non-blank `question` (`pr_ask`). |
| question is too long: at most 8000 characters are allowed | argument | Shorten the question. |
| publish_mode must be comment or description | argument | Fix `publish_mode` (`pr_describe`). |
| update_title needs publish=true and publish_mode=description | argument | Set both, or drop `update_title` (`pr_describe`). |
| issue_context must hold 1 to 3 issues; issue_context[N].key is required; issue_context[N].title is required; issue_context[N].key must be at most 64 characters; issue_context[N].title must be at most 300 characters; issue_context[N].description must be at most 20000 characters; issue_context[N].acceptance_criteria must be a string or a list of strings; issue_context[N].acceptance_criteria must be at most 10000 characters in total; issue_context[N].url must be an http or https URL | argument | Fix `issue_context` (`pr_review`, `pr_describe`); `N` is the item, counting from 1. See [Issue compliance notes](#issue-compliance-notes). |
| the number names a pull request, not an issue | `not_an_issue` | Never returned to a client: an issue reference that names a pull request is skipped (`issue.refs` outcome `not_an_issue`). |
| no Gitea token in this request: set the X-Review-MCP-Gitea-Token header in your MCP client configuration | `credentials_missing` | Serve mode: the request carried no Gitea token. Add the header to the client configuration ([Serve mode](serve.md#client-configuration-for-a-remote-server)); `server_info` shows which headers arrived. No outbound request was made. |
| no Bitbucket Server token in this request: set the X-Review-MCP-Bitbucket-Server-Token header in your MCP client configuration | `credentials_missing` | Same, for a Bitbucket Server URL. |
| no GitHub token in this request: set the X-Review-MCP-GitHub-Token header in your MCP client configuration | `credentials_missing` | Same, for a GitHub URL. |
| no GitLab token in this request: set the X-Review-MCP-GitLab-Token header in your MCP client configuration | `credentials_missing` | Same, for a GitLab URL. |
| no LLM API key in this request: set the X-Review-MCP-LLM-API-Key header in your MCP client configuration | `credentials_missing` | Same, for the LLM key. Only when `serve.llm_key_source = header`; with `server` the server's own key is used. |
| malformed credential header: <header name> | (HTTP 400) | A credential header is longer than 4096 bytes, contains anything but visible ASCII (a stray newline or a space inside the value) or was sent twice. Fix the value in the client configuration. The value is never echoed. |
| the server is busy: retry shortly | `server_busy` | All `serve.max_concurrent_calls` slots are in use and `serve.max_queued_calls` calls (default 32; 0 means none may wait) already wait for one. Retry, or raise `serve.max_concurrent_calls` (1 to 64) or `serve.max_queued_calls` (0 to 1024). |
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
  The CI results (`pr_info`, and `pr_review` unless `review.ci_status` is
  false) also need **Commit statuses** (read) and **Checks** (read); with only
  one of them the CI results are partial with a note, without both the CI
  state is `unreadable` with a note, and the rest still works. A
  classic token's `repo` scope covers them. See [GitHub](github.md#token).
- **GitLab:** a legacy personal access token with the scopes `api` (reading,
  publishing, `pr_comment_reply`, `pr_comment_create` and the description
  edit) and `read_repository` (the repository context); or a project or group
  access token with the role Reporter (read) or Developer (publish) and the
  same scopes. See [GitLab](gitlab.md#token).
- **Bitbucket Server / Data Center:** an HTTP access token with repository
  read permission, plus write permission only for comments and for
  `pr_describe` with `publish_mode=description`, which updates the pull
  request (`pr_improve` with `publish=true` needs comment write, like
  `pr_review`). The token is sent
  as `Authorization: Bearer ...`; basic authentication is not supported.
- **CI results, guidelines and file tree:** on Gitea and Bitbucket Server the
  read access above is enough (the commit status or build status of the head
  commit, the guidelines file and the directory listings at the target
  branch). On GitHub the guidelines and the listings need Contents (read); the
  CI results need the two permissions named under GitHub. On GitLab the `api`
  scope (or the Reporter role) covers all of it.

Which call fails tells you which scope is missing: if `diag pr` works but
`diag comment` fails with `authentication failed`, the token is valid but
lacks the write scope; if `diag pr` itself fails, the token is wrong or
cannot read the repository (a hidden repository may also answer 404). Pass
tokens only through the environment (`REVIEW_MCP_GITEA_TOKEN`,
`REVIEW_MCP_BITBUCKET_SERVER_TOKEN`, `REVIEW_MCP_GITHUB_TOKEN`, `REVIEW_MCP_GITLAB_TOKEN`) or, in serve mode, the request headers
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
- **GitLab base URL.** `REVIEW_MCP_GITLAB_BASE_URL` is the web address merge
  request URLs start with, with its context path if the instance has one (for
  example `https://example.com/gitlab`), and has no default. The API address
  is always `{base}/api/v4`; `server_info` shows it. It must be the address in
  GitLab's `external_url`, or paging fails ([GitLab](#a-link-points-outside-the-api-base)).
- **Scheme, host and port must match exactly** (case-insensitive host; default
  ports 80/443 are implied). `https://your-gitea.example.evil.example` or a
  sibling path such as `/bitbucket-old` does not match `/bitbucket`.
- **CA certificates.** For a server with a private CA, set
  `REVIEW_MCP_GITEA_CA_CERT`, `REVIEW_MCP_BITBUCKET_SERVER_CA_CERT` or
  `REVIEW_MCP_GITHUB_CA_CERT` or `REVIEW_MCP_GITLAB_CA_CERT` to the
  path of a PEM bundle; it is used in addition to the system roots. A
  `transport` error with the hint `TLS verification failed` usually means the
  CA is missing or the certificate does not cover the host name.
- **`insecure_skip_verify`.** `REVIEW_MCP_GITEA_INSECURE_SKIP_VERIFY=true` (and
  `REVIEW_MCP_BITBUCKET_SERVER_INSECURE_SKIP_VERIFY`, and the GitHub and GitLab equivalents) turns off certificate verification. Use it
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
