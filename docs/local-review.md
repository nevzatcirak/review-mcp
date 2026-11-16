# Local review

`local_review` reviews work that exists only on your machine, before you push
it. It reads a git repository on the machine where review-mcp runs, sends the
change to your LLM endpoint and shows you the review. Nothing is published:
there is no pull request yet, so there is nowhere to post to.

You can use it in two ways:

- from your MCP client, with the `local_review` tool or the
  `multi_perspective_review` prompt with `repo_path`;
- from a git `pre-push` hook or a terminal, with `review-mcp local review`.

Local review is new in 2.10.0-rc.1 (npm `next`). It is available in stdio
mode and on the command line only; see [Not in serve mode](#not-in-serve-mode).
Since 2.10.0-rc.2 (planned) it needs no git provider at all; see
[Local reviews only](#local-reviews-only).

## Local reviews only

If you only want to review local work before you push it, set the LLM and
nothing else. No git provider, no token and no host other than your LLM
endpoint:

```sh
export REVIEW_MCP_LLM_BASE_URL=https://llm.example.com/v1
export REVIEW_MCP_LLM_MODEL=example-model
export REVIEW_MCP_LLM_API_KEY=...   # your own key
```

That configuration is valid when `local.enabled` is `true` (the default) and
review-mcp runs over stdio or as `review-mcp local review`. Nothing marks it
as a setting: review-mcp calls a configuration with no provider enabled
*local-only*, and `server_info` reports `local_only: true`.

In a local-only stdio start:

- the tools are exactly `server_info`, `local_review` and (as always in stdio
  mode) `job_result`; the `pr_*` tools are not registered. Add a provider
  later and they appear;
- the server instructions are this paragraph, and nothing else:

  ```text
  review-mcp reviews local git work before it is pushed. No git provider is configured, so only local_review is available: it reads the commits after the merge base with the base and the uncommitted changes, and publishes nothing. Pull request tools appear once a provider (gitea, bitbucket_server, github or gitlab) is configured.
  ```

- the startup log shows `providers=[]` and then one more line:

  ```text
  no git provider is configured; only local reviews are available
  ```

- the `multi_perspective_review` prompt accepts `repo_path`; with `pr_url` it
  is refused (`InvalidParams`) with:

  ```text
  no git provider is configured; only local reviews are available (pass repo_path)
  ```

The configuration is **still invalid**, with the sentence `no provider
enabled: ...` ([Troubleshooting](troubleshooting.md#configuration-sentences-gitlab)),
when `local.enabled` is `false`, in `review-mcp serve`, in `review-mcp ci` and
in every `review-mcp diag` command that reads a pull request.

## What is reviewed

A local review compares a **base** commit with your local work:

- the commits after the merge base of the base and `HEAD`;
- plus, by default, the uncommitted changes to tracked files, staged and
  unstaged (`include_uncommitted`, default `true`; on the command line
  `--uncommitted=false` turns them off);
- plus, only when you ask for it, the untracked files that your ignore rules
  do not exclude (`include_untracked`, default `false`; on the command line
  `--untracked`). See [Untracked files and secrets](#untracked-files-and-secrets).

### The base

Pass `base` (on the command line `--base`) to name the base: a branch, a tag,
a remote-tracking branch such as `origin/main`, or a commit. Without it,
review-mcp tries these in order and uses the first that exists:

1. the current branch's upstream;
2. `origin/HEAD`;
3. `origin/main`, then `origin/master`;
4. `main`, then `master`.

When none exists, the review is refused with:

```text
local review: no base was found: tried the current branch's upstream, origin/HEAD, origin/main, origin/master, main and master; pass base (a branch, a tag or a commit)
```

The result names the base it used: `local.base_ref` (the base as you gave
it, or the default it found, such as `origin/main`), `local.base_sha`, and
`local.merge_base`, the commit the diff starts from.

### A remote-tracking base may be stale

review-mcp never fetches. A remote-tracking branch such as `origin/main` is
your repository's local copy of the remote branch, as of your last fetch or
pull. When the base is one, the result carries this note (the ref is the one
used):

```text
the base origin/main is the local copy of a remote-tracking branch and may be behind the remote; nothing was fetched
```

If the remote has moved on, the review may include commits that are already
on the remote, or compare against an old version of the base. Run `git fetch`
first when that matters.

### Nothing to review

When the commits after the merge base and the included changes are all
empty, no model call is made and the result is empty, with a note that names
the base, for example:

```text
nothing to review against origin/main (0123456789ab): the commits after the merge base and uncommitted changes to tracked files are empty
```

## Read-only: what review-mcp never does

The repository is yours and on your machine, so review-mcp treats it more
strictly than a pull request it fetched itself. For a local review,
review-mcp:

- only **reads** the repository with git. It never writes to it: not to the
  work tree, the index, the configuration or the refs. It works on a private
  copy of the index in a temporary directory, which it removes afterwards;
- never builds, tests, runs or loads the code;
- never runs a git hook, a clean or smudge filter, a textconv or external
  diff driver, or an fsmonitor, whatever the repository's configuration says;
- never touches the network for this work: no fetch, no lazy fetch of a
  partial clone, no submodule update. The only network request is the one to
  your LLM endpoint;
- never adds a `safe.directory` entry. A repository owned by another OS user
  is refused with:

  ```text
  local review: git refuses the repository because it is owned by another OS user (safe.directory); review-mcp neither adds nor reads safe.directory entries: run it as the repository's owner
  ```

Submodules are never entered.

## What goes to the LLM endpoint

The same kinds of content as for a pull request
([Reviewing pull requests](review.md)), taken from the repository:

- a title: the subject of the newest commit after the merge base, or
  "Uncommitted changes" when there is no such commit;
- a description: the subjects of up to 20 of those commits;
- the commit messages, at most the 250 newest;
- the diff and the contents of the changed files, within the same limits as
  for a pull request (`diff.*`), and, with `include_untracked`, the untracked
  files that are not held back;
- the project's guidelines file and the listing of the directories the change
  touches, read at the merge base;
- with [repository context](repo-context.md) on, the uses of the changed
  symbols found elsewhere in the repository (see below);
- the `issue_context` you pass.

The CI results, the discussion of a pull request, related pull requests and
issue lookups do not exist for local work, so nothing of them is sent and no
note mentions them.

The repository config (`.review-mcp.toml`), the documentation style file and
the focus profiles are read at the **merge base**, by the same rule as the
base revision of a pull request: an uncommitted or unpushed edit to
`.review-mcp.toml` does not change how the work that contains it is reviewed.

### Repository context

With `context.repo.enabled`, repository context searches your repository
read-only at `HEAD`, the last commit; no copy is fetched. Files with
uncommitted changes are left out of that search entirely, because their
committed content is not what you are about to push. When uncommitted changes
are part of the review and repository context ran, the result carries this
note:

```text
Repository context searched the last commit (HEAD): uses in files with uncommitted changes were not searched.
```

The [repository tools](repo-context.md#repository-tools) read the same
read-only view of `HEAD`, with their usual path rules.

## Untracked files and secrets

Untracked files are left out unless you ask for them, so that a file such as
an `.env` that you forgot to ignore never reaches the model by surprise. With
`include_untracked` (or `--untracked`):

- files that your ignore rules exclude stay out: the repository's
  `.gitignore` files, `.git/info/exclude`, the repository's own
  `core.excludesFile`, and your global excludes file;
- your global excludes file is the `core.excludesFile` of your global git
  configuration, else `$XDG_CONFIG_HOME/git/ignore`, else
  `~/.config/git/ignore`. A global `core.excludesFile` that is not an absolute
  path once `~` is expanded (for example `.gitignore_global`) is skipped
  without a note; write it as an absolute path or as `~/...`;
- files whose names suggest secrets are **always** held back, even when they
  are not ignored. The names are matched without case on the file's base
  name: `.env`, `.env.*`, `*.pem`, `*.key`, `*.p12`, `*.pfx`, `*.jks`,
  `*.keystore`, `id_rsa*`, `id_ed25519*`, `.netrc`, `.npmrc`, `.pypirc`,
  `*.tfvars`, `credentials*`. They are counted in `local.untracked_held_back`
  and named in no output; the result carries this note, with their number:

  ```text
  %d untracked files were left out because their names suggest secrets.
  ```

  (`%d` is the number.) To have such a file reviewed, commit it or rename it;
- binary untracked files are skipped, as binary files in a diff are.

The held-back list is a safety net, not a secret scanner: a secret in a file
with another name, or in a tracked file, goes to the model like any other
content.

## Allowed roots

An operator can limit which directories `repo_path` may name:

| Key | Environment variable | Default | Meaning |
|---|---|---|---|
| `local.enabled` | `REVIEW_MCP_LOCAL_ENABLED` | `true` | With `false`, `local_review` is not registered, the prompt refuses `repo_path`, and `review-mcp local review` exits 1. |
| `local.allowed_roots` | `REVIEW_MCP_LOCAL_ALLOWED_ROOTS` | (empty) | Absolute directories (comma-separated in the environment). When set, `repo_path` must be one of them or under one, after symbolic links are resolved. Empty allows any git work tree. |

A path outside the roots, or a symbolic link that points outside them, is
refused before any git command runs:

```text
local review: repo_path is not under a directory of local.allowed_roots
```

## The `local_review` tool

Arguments: `repo_path` (required: the absolute path of the work tree or a
directory in it), `base`, `include_uncommitted`, `include_untracked`,
`focus`, `extra_instructions`, `output_language`, `max_findings`,
`issue_context` and `wait_seconds`. A slow call answers with a `job_id`, as
`pr_review` does; fetch the result with `job_result`
([Slow endpoints](review.md#slow-endpoints)).

`publish`, `findings`, `incremental` and `related` do not exist for local
work.

The result is the `pr_review` result with:

- `pr.kind` `"local"` and `pr.url` `null`;
- a `local` object: `repo_path` (the work tree's absolute path), `base_ref`,
  `base_sha`, `merge_base`, `head_sha`, `uncommitted` (whether uncommitted
  changes were part of the diff), `untracked_files` (how many untracked files
  were included) and `untracked_held_back` (how many were held back as likely
  secrets; always 0 without `include_untracked`, because untracked files are
  not listed then).

Findings name the file and line (`src/app.go` L2) **without a link**: there is
no host to link to. Your client or editor can open the path in the work tree.

The `multi_perspective_review` prompt takes `repo_path` and `base` instead of
`pr_url` (exactly one of the two). It tells the client to run one
`local_review` per focus and show the merged findings; there is nothing to
publish. The default list of focuses and the focus rules use the local diff's
files.

## The command line

```
review-mcp local review [--path DIR] [--base REF] [--uncommitted=true|false] [--untracked]
                        [--focus NAME] [--max-findings N] [--output-language L]
                        [--issue-context-file PATH] [--json] [--timeout D]
                        [--fail-on security|any|issue-not-compliant]
```

| Flag | Meaning |
|---|---|
| `--path DIR` | The work tree or a directory in it. Default: the current directory. A relative path is taken against the current directory. |
| `--base REF` | The `base` argument. Default: see [The base](#the-base). |
| `--uncommitted=BOOL` | The `include_uncommitted` argument (default `true`). |
| `--untracked` | The `include_untracked` argument. |
| `--focus NAME` | The `focus` argument. |
| `--max-findings N` | The `max_findings` argument, 1 to 20. |
| `--output-language L` | The `output_language` argument, for example `en-US`. |
| `--issue-context-file PATH` | A JSON file holding the `issue_context` argument (1 to 3 issues), as in [CI mode](ci.md#the-issue-context-file). |
| `--json` | Print the structured result instead of markdown. |
| `--timeout D` | Bound the whole run: a Go duration, default `30m`, at least `1m`. |
| `--fail-on LEVEL` | Exit 3 when the review meets `LEVEL`, with the rules of [CI mode](ci.md#exit-codes): `security` (the security concerns are set, or a finding of a `--focus security` run), `any` (at least one finding) or `issue-not-compliant` (the issue verdict is `not_compliant`). |

The review goes to standard output as markdown (or JSON with `--json`). The
markdown heads with `## Local review` and the line `Local work: <subject>`
(`Local work` when there is no subject), not with the pull request heading;
the progress stages are `reading` and `reading repository context`, not
`fetching`. The
logs and one final line go to standard error. The command never reads
standard input and never asks a question on the terminal, so it is safe in a
hook.

Settings come only from the `REVIEW_MCP_*` variables and the configuration
file named by `REVIEW_MCP_CONFIG`, as for the MCP server
([Setup guide](setup.md)). The LLM API key is a secret and is read from
`REVIEW_MCP_LLM_API_KEY` only. A local review needs no provider section (see
[Local reviews only](#local-reviews-only)); before 2.10.0-rc.2 it needed one,
which was never contacted.

### Exit codes

| Code | Meaning |
|---|---|
| 0 | Done: the review was printed (also with findings, unless `--fail-on` applies). |
| 1 | The run failed: the configuration, the model, git or the timeout; the reason is on standard error. Also when `local.enabled` is `false`. |
| 2 | A usage error, or no repository or base: the path is not a git work tree, is outside `local.allowed_roots`, the base is not found or has no merge base with `HEAD`. Nothing is sent to the model. |
| 3 | A `--fail-on` condition is met. The review was printed first. |

The final line on success is `local review: printed; nothing is published`;
on exit 3 it is followed by the condition, for example
`local review: exit 3: --fail-on any is met (1 findings)`.

## A `pre-push` hook

git runs `.git/hooks/pre-push` before every push and cancels the push when
the hook exits with a status other than 0. These samples block the push only
when the review meets `--fail-on` (exit 3). When review-mcp cannot review
(the endpoint is down, the configuration is wrong), they print a warning and
let the push go ahead, so a broken endpoint never stops you from pushing; to
block in that case too, change the last branch to `exit 1`.

Install review-mcp so that the hook does not download it on every push
([Install](setup.md#1-install)), and make the `REVIEW_MCP_*` settings
(at least the LLM endpoint and key) available in the environment the hook
runs in, for example from your shell profile, with the rest in a
configuration file named by `REVIEW_MCP_CONFIG`.

### POSIX shells (Linux, macOS)

Save as `.git/hooks/pre-push` and make it executable
(`chmod +x .git/hooks/pre-push`):

```sh
#!/bin/sh
# .git/hooks/pre-push: review the work you are about to push.
# review-mcp reads its settings from REVIEW_MCP_* and the file named by
# REVIEW_MCP_CONFIG, as the MCP server does.
review-mcp local review --fail-on any
status=$?
case $status in
  0) exit 0 ;;
  3) echo "pre-push: review-mcp reported findings; push with --no-verify to skip the review once" >&2
     exit 1 ;;
  *) echo "pre-push: review-mcp could not review the work (exit $status); the push goes ahead" >&2
     exit 0 ;;
esac
```

### Windows (Git for Windows)

Git for Windows runs hooks with its own bundled `sh`, so the hook is a shell
script on Windows too. Save it as `.git/hooks/pre-push`, **without** a file
extension and with **LF** line endings (a CRLF line ending breaks the first
line). `review_mcp` is the path of `review-mcp.exe`, for example from the
release archive; with forward slashes, a path such as
`C:/Tools/review-mcp/review-mcp.exe` works.

```sh
#!/bin/sh
# .git/hooks/pre-push for Git for Windows: review the work you are about to push.
# review-mcp reads its settings from REVIEW_MCP_* and the file named by
# REVIEW_MCP_CONFIG, as the MCP server does.
review_mcp=review-mcp.exe
"$review_mcp" local review --fail-on any
status=$?
case $status in
  0) exit 0 ;;
  3) echo "pre-push: review-mcp reported findings; push with --no-verify to skip the review once" >&2
     exit 1 ;;
  *) echo "pre-push: review-mcp could not review the work (exit $status); the push goes ahead" >&2
     exit 0 ;;
esac
```

### Skipping the review once

```sh
git push --no-verify
```

skips every `pre-push` hook for that push.

### The time a review adds

The git reads take well under a second on a typical repository. The review
itself is one model call (more for a large change reviewed in parts, or with
repository tools), so a push waits as long as a `pr_review` of the same
change would take on your endpoint: seconds on a fast hosted model, minutes
on a slow local one. `--timeout` bounds the wait; when it passes, the command
exits 1 and the samples above let the push go ahead.

## Not in serve mode

In serve mode `local_review` is not registered, and the
`multi_perspective_review` prompt refuses `repo_path` with "local reviews run
in stdio mode only": the server's disk is not the user's
([Serve mode](serve.md)).

## What is not available

- Publishing: there is no pull request. Once you have pushed and opened one,
  review it with `pr_review`.
- `incremental`, `findings`, `related`, the CI results, the discussion, issue
  lookups on a host and `pr_review_decision`.
- `pr_ask`, `pr_describe` and `pr_improve` on local work.
- Searching uncommitted content with repository context (see
  [Repository context](#repository-context)).
- Links from findings to files.
