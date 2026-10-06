# Reviewing pull requests with `pr_review`

`pr_review` reads a pull request, asks your LLM to review it, and returns a
structured review: key issues with code excerpts, an effort estimate, whether
the PR contains tests, and security and performance concerns. With
`publish=true` it also posts the review as one PR-level comment.

## Calling the tool

| Argument | Required | Meaning |
|---|---|---|
| `pr_url` | yes | The pull request URL, on a configured Gitea or Bitbucket Server host. |
| `extra_instructions` | no | Extra guidance for the model for this call; replaces `review.extra_instructions`. An empty value means "not given". |
| `output_language` | no | Locale code for the review text, such as `en-US` or `tr-TR`; replaces `output.language`. Same format as the config key. |
| `max_findings` | no | The most key issues to return, 1 to 20; replaces `review.max_findings` (default 3). |
| `publish` | no | `true` also posts the review as a PR comment. Default `false`. |

Invalid `output_language` or `max_findings` values are rejected with a fixed
message before anything is sent to the provider or the LLM.

The result has two parts: the review as portable markdown (the text content;
no raw HTML, so terminal clients show it as is) and the same data as
structured content (`pr`, `review`, `coverage`, `notes`, `metadata` and, when
requested, `publish`). While it runs, a client that sent a progress token
receives the stages `fetching`, `preparing diff`, `calling model` and
`rendering`.

## What is sent to the LLM

One chat completion request per review (two if the first answer cannot be
parsed, see below) goes to `llm.base_url`. It contains:

- the review instructions and the output format;
- the PR's **title**, source **branch** and **description** (the description
  is cut to `diff.max_description_tokens`, 500 by default);
- the **diff**, filtered by `ignore.glob`, `ignore.regex` and the built-in
  rules for generated files, lockfiles and binaries, with extra context lines
  around each change;
- your `extra_instructions`, if any, and the output-language instruction.

Never sent: provider tokens, the LLM API key (it is only the `Authorization`
header of the request itself), configuration files, or anything outside the
PR. Commit messages are not part of the review prompt.

The prompts, the model's answer and the diff are never written to the logs,
at any level. A failed review reports one of a fixed set of sentences, never
a response body (see [Troubleshooting](troubleshooting.md)).

If your LLM endpoint is not under your control, remember that the PR text and
code leave your machine for it; point review-mcp only at endpoints you trust
with that code.

## Choosing `llm.context_window`

review-mcp has no model registry: tell it the real context window, in tokens,
with `llm.context_window` (`REVIEW_MCP_LLM_CONTEXT_WINDOW`, at least 4096).
Use the limit your server actually runs the model with; some servers start a
model with less than it supports.

The diff gets what is left after the output reserve and the prompt:

```
soft limit = context_window - max(max_output_tokens, 1000) - 500 - prompt tokens
```

`llm.max_output_tokens` is optional. If you set it, make it generous enough for
a review with all fields enabled and several findings (a few thousand tokens);
an answer cut off by the limit is reported in the notes. See
[Getting started](getting-started.md#how-the-context-window-shapes-the-diff-budget)
for the full table.

### Prompt tokens

The prompt scaffolding (instructions, output format, empty PR fields) was
measured with the real templates. With every field enabled, a non-English
language and extra instructions it is **2205 tokens** (estimate, including the
safety factor and framing). The other combinations range from 1589 to 2205.
That maximum is the default of `diag diff --prompt-tokens`. On top of it come
the title, branch and description of the actual PR; `diag review --dry-run`
(below) reports the exact figure for a given PR.

Reproduce the table with
`go test ./internal/review -run TestScaffoldingTokens -v`.

If a PR does not fit at all, the review stops with "the pull request diff
does not fit the configured context window" and nothing is sent to the model.

## Sampling settings

review-mcp sends only the sampling parameters you configure
(`llm.temperature`, `llm.seed`, `llm.reasoning_effort`, `llm.max_output_tokens`
as `max_tokens`); by default none are sent, so the request is as compatible
as possible. A recommended starting point for reviews is

```toml
[llm]
temperature = 0.2
```

(or `REVIEW_MCP_LLM_TEMPERATURE=0.2`), which is what PR-Agent uses by default.
Some models reject `temperature`; the error then names the sampling keys in
its hint, and you can unset it. If you set `seed`, set `temperature` yourself
too: review-mcp does not force it.

## Reading the result

### Review

The markdown starts with `## PR Review` and a line naming the pull request,
then the enabled fields in a fixed order:

- **Estimated effort to review** (`review.require_effort_estimate`): `N/5`.
- **Relevant tests** (`review.require_tests`): "PR contains tests" or "No
  relevant tests".
- **Security** (`review.require_security`): "No security concerns
  identified", or the text of the concern.
- **Performance** (`review.require_performance`): "No performance concerns
  identified", or the text of the concern (for example unbounded work, N+1
  access or blocking I/O on a request path).
- **Key issues to review**, last: a numbered list; each item has a header, the
  file with line numbers (a link to the line on the provider when one is
  available), an explanation and a code excerpt.

Code excerpts come from the PR's head files when they could be fetched in
full, and otherwise from the diff itself. If the lines a finding names cannot
be verified against the diff, the finding is kept without an excerpt and
says so: treat such a finding with extra care, the model may have the lines
wrong.

### Coverage

A review always ends with a coverage section, so you can see what the model
did **not** see. Every changed file appears in exactly one group:

- **Included**: the diff was sent in full.
- **Clipped**: only part of the diff was sent (a very large file was cut to fit).
- **Omitted**: left out to fit the context window; grouped as added, modified
  and deleted files. If many, only the first 50 are listed, then "and N more".
- **Skipped**: not reviewable, with a reason: `binary`, `file_limit`
  (`diff.max_files_full_content`), `size_limit` (`diff.max_file_bytes` or
  `diff.max_diff_bytes`), `fetch_failed`, `empty_diff`, `unparseable_patch`.
- **Filtered**: excluded by the ignore rules, with the rule that matched
  (`ignore_glob`, `ignore_regex`, `lockfile_or_minified`, `bad_extension`,
  `generated:<framework>`).

A review whose files are mostly omitted or filtered says little about the PR.
Raise `llm.context_window`, narrow the PR, or adjust `ignore.*`. If nothing
reviewable remains after filtering, the model is not called and the review
says "No reviewable changes after filtering."

### Notes

The notes section appears when something about the run deserves attention:

- the answer was cut off by the output limit (raise `llm.max_output_tokens`);
- the first answer could not be parsed as YAML and the review comes from one
  second attempt;
- findings were dropped (more than `max_findings`, or a finding without a file
  or without text);
- files were clipped, or the diff was shortened by the request-size guard;
- a snippet could not be verified.

If the second attempt also cannot be parsed, the review fails with "the model's
answer could not be parsed as a review, also after one retry". Try again, or
check that the model follows YAML output instructions.

## Publishing

`publish=true` (or `diag review --publish`) posts the review as a single
PR-level comment after it was produced. Nothing is ever posted without it.
The comment is rendered for the provider: Gitea gets the richer GitHub-style
markdown (emojis, a table and one collapsible block per finding); Bitbucket
Server gets headings and tables without raw HTML. The coverage and the notes
are included. The token needs write access to pull requests or comments (see
the [token scopes](troubleshooting.md#token-scopes)).

A failure to post never discards the review: the result carries the review
and a `publish` object with `published: false` and the reason. Publishing is
not idempotent; calling the tool twice posts two comments.

## Tuning the budget with `diag review --dry-run`

```sh
review-mcp diag review https://your-gitea.example/octo/demo/pulls/7 --dry-run
```

runs everything up to the model call and prints a JSON report; the model is
not called and nothing is posted. It needs the same configuration as a real
review (the LLM API key must be set, but it is not used).

```json
{
  "dry_run": true,
  "pr": { "kind": "gitea", "url": "https://your-gitea.example/octo/demo/pulls/7", "number": 7, "title": "Add feature" },
  "empty": false,
  "budget": { "context_window": 32000, "soft_limit": 28200, "hard_limit": 28700, "prompt_tokens": 2300, "factor": 0.3 },
  "tokens": { "prompt": 2300, "diff": 5120, "request": 7480, "context_window": 32000 },
  "fast_path": true,
  "coverage": { "included": ["src/app.go"], "clipped": [], "omitted": { "added": [], "modified": [], "deleted": [] }, "skipped": [], "filtered": [] },
  "notes": [],
  "elapsed_ms": 120
}
```

`tokens.request` is the estimate of the whole request; compare it with
`context_window`. If `fast_path` is false, the diff did not fit whole and the
coverage lists what was clipped or omitted. To see the effect of a smaller
window, set `REVIEW_MCP_LLM_CONTEXT_WINDOW` for one run.

`--show-prompt` additionally prints the rendered system and user prompts to
stdout, after the JSON, under the lines `--- system prompt ---` and
`--- user prompt ---`. This shows exactly what the model would receive,
including the PR description and diff: handle the output like the PR itself.
The prompts are never logged.

Without `--dry-run`, `diag review` runs the full review and prints the same
markdown the tool returns; with `--publish` it also posts it. `--dry-run` and
`--publish` cannot be combined. Exit codes are as for `diag pr`: 0, 1 on a
failure (a failed publish also exits 1, after printing the review), 2 on a
usage error.
