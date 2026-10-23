# CI mode

`review-mcp ci` reviews, describes or suggests changes for one pull request
from a pipeline step, with nobody calling a tool. It runs the same code as
`pr_review`, `pr_describe` and `pr_improve`, prints the result, posts it when
you pass `--publish`, and exits with a status the pipeline can act on.

```sh
npx -y @nevzatcirak/review-mcp@2.6.0-rc.1 ci review --publish --incremental
```

The command is a stable interface: its flags, exit codes and output change
only with a [CHANGELOG](../CHANGELOG.md) entry. The `diag` commands stay
diagnostic and are not meant for pipelines.

## What it does and never does

It does:

- read the pull request through the provider's API with the token you give
  it, send it to your LLM endpoint and print the result;
- with `--publish`, post the result exactly as the tool does: the review's
  overview (edited in place on later runs) and inline comments, the
  description's managed region, or the suggestions;
- with [repository context](repo-context.md) on, fetch its own copy of the
  pull request's head into a cache, as in stdio.

It never:

- builds, tests, runs or loads the code under review (R-1). The pipeline's
  own checkout is never read either, so the examples below have no checkout
  step;
- posts anything without `--publish`;
- reads `GITHUB_TOKEN`, `CI_JOB_TOKEN` or any other CI token on its own. Only
  the `REVIEW_MCP_*` variables are read; the pipeline maps a secret to one of
  them explicitly;
- sends a token to a host that is not configured: a detected pull request URL
  goes through the same pinned-host check as a tool argument;
- contacts an issue tracker (X-42). An earlier step can write the issue to a
  file for `--issue-context-file`.

## Commands and flags

```
review-mcp ci review   [flags]
review-mcp ci describe [flags]
review-mcp ci improve  [flags]
```

There is no `ci ask`: a pipeline has no one to ask a question.

| Flag | Commands | Meaning |
|---|---|---|
| `--pr-url URL` | all | The pull request. Without it, the pull request of the pipeline is used (see [Which pull request](#which-pull-request)). |
| `--publish` | all | Post the result. review: the overview and inline comments. describe: the managed region at the end of the pull request description (see [`describe --publish`](#describe---publish)). improve: the overview and the suggestions. Without it, nothing is posted. |
| `--related URL` | review | A related pull request or repository URL; repeatable, at most 4. The `related` argument. |
| `--focus NAME` | review | The `focus` argument. A focused review never publishes, so `--focus` with `--publish` is a usage error. |
| `--incremental` | review | The `incremental` argument: review only what changed since the last published review (see [Incremental runs](#incremental-runs)). |
| `--max-findings N` | review | The `max_findings` argument, 1 to 20. |
| `--output-language L` | all | The `output_language` argument, for example `en-US` or `tr-TR`. |
| `--issue-context-file PATH` | review, describe | A JSON file holding the `issue_context` argument (see [The issue context file](#the-issue-context-file)). |
| `--json` | all | Print the tool's structured result instead of markdown. |
| `--fail-on LEVEL` | review | Exit 3 when the review meets `LEVEL`: `security`, `any` or `issue-not-compliant` (see [`--fail-on`](#--fail-on)). |
| `--fail-on-partial` | all | Exit 3 when the coverage is partial. |
| `--timeout D` | all | Bounds the whole run: a Go duration such as `20m` or `1h`. Default `30m`, at least `1m`. |

Every tool argument is checked by the tool's own validation, so the command
refuses what the tool refuses, with the same sentence. The configuration
(provider base URLs and tokens, the LLM endpoint, every other key) comes from
the `REVIEW_MCP_*` variables or the TOML file, as for the server; see
[Setup](setup.md). The rate limits (`llm.requests_per_minute` and the others)
and the [fallback models](setup.md#fallback-models) apply unchanged. There is
no `job_result`: the command waits for the result itself, up to `--timeout`.

## Exit codes

| Code | When |
|---|---|
| `0` | Done: the result was printed, and posted with `--publish`. Also when the review has findings or the coverage is partial, unless a `--fail-on` flag applies. |
| `1` | The run failed: configuration, provider, LLM, a failed publish, or `--timeout`. The last stderr line is the error sentence (the same ones as the tools, listed in [Troubleshooting](troubleshooting.md)), never a token. |
| `2` | Usage: an unknown command or flag, an invalid value, `--focus` with `--publish`, an invalid issue context file, or no pull request found. Nothing is loaded and nothing is sent before a usage error. |
| `3` | A `--fail-on` level or `--fail-on-partial` is met. The result was printed, and posted with `--publish`, before the command exits. |

A reviewer model is not a gate by default: without `--fail-on` or
`--fail-on-partial`, findings never fail the job.

### `--fail-on`

| Level | Met when |
|---|---|
| `any` | The review has at least one finding. Findings carried from an earlier review of an incremental run count. |
| `security` | The review's security concerns field is set (anything other than "No"), or the run is focused on security (`--focus security`) and has at least one finding. |
| `issue-not-compliant` | The [issue compliance](review.md#issue-compliance) verdict is `not_compliant`. |

A finding has no category, so `security` is not decided by reading the
titles of the findings: it is the review's security concerns field, which
the model fills for every review, or any finding of a review that looked
only at security. The security concerns field is filled only while
`review.require_security` is on (the default). With it off and no
`--focus security`, the level cannot be met: the run logs a warning saying
so and exits 0. In the same way, `issue-not-compliant` logs a warning when no
issue was checked (none was found, or none was passed).

A `--focus` run never publishes, so a pipeline that wants a security gate and
a posted review runs two steps: `ci review --publish` and
`ci review --focus security --fail-on security`.

## Output

- **stdout:** the result as markdown, the same text the tool returns, or the
  structured result with `--json`.
- **The step summary:** when `GITHUB_STEP_SUMMARY` is set (GitHub Actions and
  Gitea Actions), the markdown is also appended to that file. A failure to
  write it is a warning, not an error.
- **stderr:** the logs (at the configured level; the pull request URL only
  redacted) and one final line:

| Final line | Meaning |
|---|---|
| `ci review: printed only (no --publish)` | Nothing was posted, as asked. |
| `ci review: posted overview <id>` | The overview was posted or edited; `<id>` is its comment id. `ci improve` prints the same line. |
| `ci describe: posted description` | The managed region of the description was written. |
| `ci review: nothing posted` | `--publish` was given but there was nothing to post: an incremental run with nothing changed. |
| `ci review: <error sentence>` | Exit 1, for example `ci review: the result was not posted: …`. |
| `ci review: the run did not finish within --timeout (class: timeout)` | Exit 1: `--timeout` passed, whatever request it cut off. |
| `no pull request: pass --pr-url (detected: <CI>)` | Exit 2: see [Which pull request](#which-pull-request). |

On exit 3, one more line follows the final line and says which condition was
met, for example:

```
ci review: posted overview 4711
ci review: exit 3: --fail-on any is met (2 findings)
```

## Which pull request

1. `--pr-url` when it is given.
2. Otherwise the pipeline's environment, read only from these variables:

| CI | When | URL |
|---|---|---|
| Gitea Actions (`GITEA_ACTIONS=true`) | a `pull_request` or `pull_request_target` event | `{GITHUB_SERVER_URL}/{GITHUB_REPOSITORY}/pulls/{n}` |
| GitHub Actions (`GITHUB_ACTIONS=true`, not Gitea) | a `pull_request` or `pull_request_target` event | `{GITHUB_SERVER_URL}/{GITHUB_REPOSITORY}/pull/{n}` |
| GitLab CI (`GITLAB_CI=true`) | a merge request pipeline (`CI_MERGE_REQUEST_IID` set) | `{CI_SERVER_URL}/{CI_MERGE_REQUEST_PROJECT_PATH}/-/merge_requests/{CI_MERGE_REQUEST_IID}` |

On GitHub and Gitea Actions, `{n}` is `pull_request.number` from the event
file at `GITHUB_EVENT_PATH`. The file is read up to 1 MiB and no other field
of it is used. Nothing is guessed from a branch name or a commit message.

Any other pipeline (a `push` event, a GitLab branch pipeline, Jenkins,
Bamboo, Bitbucket Pipelines and so on) needs `--pr-url`. Without one, the
command exits 2 with `no pull request: pass --pr-url (detected: <CI>)`, where
`<CI>` is `GitHub Actions`, `Gitea Actions`, `GitLab CI` or `none`.

The URL, given or detected, must be on a configured provider: its host is
checked against the base URLs exactly as a tool argument is, and an
unconfigured host fails with the `url_not_configured` sentence before any
request is sent. Set the provider's base URL to the address the CI reports
(the examples use the CI's own server URL variable).

## Tokens

Map a secret to the provider's `REVIEW_MCP_*_TOKEN` variable in the pipeline;
review-mcp reads nothing else. With `--publish` the token needs write access,
as for the tools; the full tables are in
[Setup: Create tokens](setup.md#2-create-tokens).

| Provider | Token | Minimal rights for `ci review --publish` |
|---|---|---|
| GitHub | The job's `GITHUB_TOKEN`, as `REVIEW_MCP_GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}` | `permissions: pull-requests: write, contents: read`. Add `issues: read` for issue compliance, and `statuses: read` and `checks: read` for the CI results; without them those parts are reported as unreadable with a note and the review goes on. |
| GitLab | A project or group access token in a masked CI/CD variable `REVIEW_MCP_GITLAB_TOKEN` | Role Developer, scope `api` (add `read_repository` for repository context). `CI_JOB_TOKEN` cannot post notes and is never read. |
| Gitea | A token in a secret mapped to `REVIEW_MCP_GITEA_TOKEN` | `write:repository`, `write:issue`, `read:user`. |
| Bitbucket Server | An HTTP access token from the CI's secret store as `REVIEW_MCP_BITBUCKET_SERVER_TOKEN` | Repository write. |

> **Unconfirmed: the job's `GITHUB_TOKEN`.** review-mcp finds its own earlier
> overview by the token's user (`GET /user`). The job token is an app token,
> and that call may be refused for it. If it is, each run posts a new overview
> with the note that the earlier one could not be looked up, and
> `--incremental` reviews in full. The acceptance run (Q1, Q2) verifies this.
> Until then, a fine-grained personal access token or a bot account's token
> in a secret works the same way.

`describe --publish` writes to the pull request itself, not only to its
comments; the rights are in
[Setup: What each token needs](setup.md#what-each-token-needs).

## Examples

Each example pins the version (`@2.6.0-rc.1`, the release these docs ship
with), so that `npx` never runs a cached older copy, and uses `--publish` and
`--incremental`. Replace `llm.example.com` and the model name with your
endpoint. Store the LLM key as a secret.

### GitHub Actions

`.github/workflows/review.yml`:

```yaml
name: review
on:
  pull_request:

permissions:
  pull-requests: write
  contents: read

jobs:
  review:
    runs-on: ubuntu-latest
    timeout-minutes: 40
    steps:
      - uses: actions/setup-node@v4
        with:
          node-version: 22
      - name: Review the pull request
        env:
          REVIEW_MCP_GITHUB_BASE_URL: ${{ github.server_url }}
          REVIEW_MCP_GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
          REVIEW_MCP_LLM_BASE_URL: https://llm.example.com/v1
          REVIEW_MCP_LLM_MODEL: your-model-name
          REVIEW_MCP_LLM_API_KEY: ${{ secrets.REVIEW_MCP_LLM_API_KEY }}
        run: npx -y @nevzatcirak/review-mcp@2.6.0-rc.1 ci review --publish --incremental
```

The pull request is detected from the event; the review is also in the job's
step summary. On GitHub Enterprise Server, `github.server_url` is your
server's address, which is the base URL review-mcp needs.

### Gitea Actions

`.gitea/workflows/review.yml`:

```yaml
name: review
on:
  pull_request:

jobs:
  review:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/setup-node@v4
        with:
          node-version: 22
      - name: Review the pull request
        env:
          REVIEW_MCP_GITEA_BASE_URL: ${{ github.server_url }}
          REVIEW_MCP_GITEA_TOKEN: ${{ secrets.REVIEW_MCP_GITEA_TOKEN }}
          REVIEW_MCP_LLM_BASE_URL: https://llm.example.com/v1
          REVIEW_MCP_LLM_MODEL: your-model-name
          REVIEW_MCP_LLM_API_KEY: ${{ secrets.REVIEW_MCP_LLM_API_KEY }}
        run: npx -y @nevzatcirak/review-mcp@2.6.0-rc.1 ci review --publish --incremental
```

Gitea sets `GITEA_ACTIONS=true` and the `GITHUB_*` variables, so the pull
request is detected under `/pulls/`. `github.server_url` is the Gitea
`ROOT_URL`; if the runner reaches Gitea at another address, set the base URL
to the address of the pull request links instead, or the URL is refused as
not configured.

### GitLab CI

`.gitlab-ci.yml`:

```yaml
review:
  image: node:22
  rules:
    - if: $CI_PIPELINE_SOURCE == "merge_request_event"
  variables:
    GIT_STRATEGY: none
    REVIEW_MCP_GITLAB_BASE_URL: $CI_SERVER_URL
    REVIEW_MCP_LLM_BASE_URL: https://llm.example.com/v1
    REVIEW_MCP_LLM_MODEL: your-model-name
  script:
    - npx -y @nevzatcirak/review-mcp@2.6.0-rc.1 ci review --publish --incremental
```

`REVIEW_MCP_GITLAB_TOKEN` and `REVIEW_MCP_LLM_API_KEY` are masked CI/CD
variables of the project. A protected variable is given only to pipelines of
protected branches, so leave them unprotected unless merge request source
branches are protected. `GIT_STRATEGY: none` skips the clone, which
review-mcp does not use. GitLab has no step summary; the review is in the
job log.

### Any other CI

Jenkins, Bamboo, Bitbucket Pipelines or a plain shell: pass the URL.

```sh
export REVIEW_MCP_BITBUCKET_SERVER_BASE_URL=https://bitbucket.example.com
export REVIEW_MCP_BITBUCKET_SERVER_TOKEN="$BITBUCKET_TOKEN"   # from the CI's secret store
export REVIEW_MCP_LLM_BASE_URL=https://llm.example.com/v1
export REVIEW_MCP_LLM_MODEL=your-model-name
export REVIEW_MCP_LLM_API_KEY="$LLM_API_KEY"                  # from the CI's secret store
npx -y @nevzatcirak/review-mcp@2.6.0-rc.1 ci review --publish --incremental \
  --pr-url "https://bitbucket.example.com/projects/PROJ/repos/demo/pull-requests/$PR_ID"
```

Any configured provider works the same way. Without Node.js, use the binary
from a [release archive](setup.md#release-archive) instead of `npx`.

## Incremental runs

With `--incremental`, a run reviews only the files whose content changed
since the last review it published, and keeps the earlier findings of the
files that did not change (see
[Incremental review](review.md#incremental-review)). It needs `--publish`
on the earlier runs, because the overview records what was reviewed.

When nothing changed since the last published review (the job is re-run
without a new push), no model call is made, nothing is posted, the final line
is `nothing posted` and the command exits 0 even with `--fail-on any`. The
exit code reflects this run, which found nothing new. A team that wants a
sticky gate, one that fails again on every re-run while the findings stand,
runs without `--incremental`.

## The issue context file

`--issue-context-file` takes a JSON array with the shape of the
`issue_context` argument: 1 to 3 items, each with `key` and `title` (required)
and `description`, `acceptance_criteria` (a string or a list of strings) and
`url`. The limits are those of the argument (see
[The `issue_context` argument](review.md#the-issue_context-argument)).

```json
[
  {
    "key": "PROJ-123",
    "title": "Export the report as CSV",
    "description": "Users need the monthly report as a CSV file.",
    "acceptance_criteria": ["A CSV button on the report page", "UTF-8 with a header row"],
    "url": "https://tracker.example.com/browse/PROJ-123"
  }
]
```

The file is at most 1 MiB. An empty array, an unknown field, a file that is
not a JSON array, or an item the argument would refuse is a usage error
(exit 2) whose sentence names the problem and never the file's content. An
earlier pipeline step writes the file from any tracker by its own means;
review-mcp holds no tracker credential.

## `describe --publish`

`ci describe --publish` writes the description into the managed region at the
end of the pull request description (`publish_mode=description` of
`pr_describe`). The author's text above the region is never changed, and the
title is never replaced. Later runs rewrite the region in place. The provider
limits of that mode (Bitbucket Server rewrites the whole pull request, and
the GitLab quick-action rules) are in
[Describing pull requests](describe.md#description-mode).

## Repository context

[Repository context](repo-context.md) and its tools are allowed, as in
stdio: set `REVIEW_MCP_CONTEXT_REPO_ENABLED=true`. When
`context.repo.cache_dir` is unset and `RUNNER_TEMP` is set (GitHub and Gitea
Actions), the cache goes to `$RUNNER_TEMP/review-mcp-repos`. Elsewhere, set
`REVIEW_MCP_CONTEXT_REPO_CACHE_DIR` to a directory the job may write. The
pipeline's own checkout is never read: review-mcp fetches the pull request's
head itself, with its own isolation.

## Pull requests from forks

Use `pull_request` (GitHub, Gitea) or merge request pipelines (GitLab) as the
default. On GitHub, a `pull_request` run for a pull request from a fork gets
no secrets and a read-only `GITHUB_TOKEN`, so the review cannot be posted
there: same-repository pull requests are reviewed, forks are not.

`pull_request_target` runs the base branch's workflow with the secrets, for
fork pull requests too. It is an opt-in, and the trigger is the pipeline
owner's choice; review-mcp has no switch either way. The usual danger of
`pull_request_target`, running the fork's code with the secrets, does not
apply to review-mcp itself: it never checks out or runs the pull request's
code (do not add a checkout of the head to the workflow). Two risks remain:

- **Prompt injection.** The pull request's text and diff reach the LLM. A
  crafted pull request can steer what the model writes, and so the comment
  that is posted under your token. The text is fenced as untrusted data, but
  no model is immune.
- **The LLM key is spent on anyone's pull request.** Anyone who can open a
  pull request starts a model call on your endpoint and your budget.
