# Reviewing pull requests with `pr_review`

`pr_review` reads a pull request, asks your LLM to review it, and returns a
structured review: key issues with code excerpts, an effort estimate, whether
the PR contains tests, and security and performance concerns. With
`publish=true` it also posts the review: one overview comment that later runs
edit in place, and an inline comment on each changed line a finding is about
(see [Publishing](#publishing)).

## Calling the tool

| Argument | Required | Meaning |
|---|---|---|
| `pr_url` | yes | The pull request URL, on a configured Gitea or Bitbucket Server host. |
| `extra_instructions` | no | Extra guidance for the model for this call; replaces `review.extra_instructions`. An empty value means "not given". |
| `output_language` | no | Locale code for the review text, such as `en-US` or `tr-TR`; replaces `output.language`. Same format as the config key. |
| `max_findings` | no | The most key issues to return, 1 to 20; replaces `review.max_findings` (default 3). |
| `publish` | no | `true` also posts the review: the overview comment and, unless `inline_findings` is false, inline comments. Default `false`. |
| `inline_findings` | no | With `publish`, post each finding that falls on a changed line as an inline comment. Replaces `review.inline_findings` (default `true`) for this call. |
| `wait_seconds` | no | How long the call waits for the review before it answers with a `job_id`, 0 to 600; replaces `llm.wait_seconds` (default 45). stdio only; see [Slow endpoints](#slow-endpoints). |

Invalid `output_language`, `max_findings` or `wait_seconds` values are
rejected with a fixed message before anything is sent to the provider or the
LLM.

The result has two parts: the review as portable markdown (the text content;
no raw HTML, so terminal clients show it as is) and the same data as
structured content (`pr`, `review`, `coverage`, `notes`, `metadata` and, when
requested, `publish`). While it runs, a client that sent a progress token
receives the stages `fetching`, `preparing diff`, `calling model` and
`rendering`. A review in parts reports `calling model (part 1 of 3)`,
`calling model (part 2 of 3)` and so on instead of `calling model` (see
[Large pull requests](#large-pull-requests)).

## What is sent to the LLM

One chat completion request per review (two if the first answer cannot be
parsed, see below) goes to `llm.base_url`; a large pull request is reviewed in
several parts, one request each (see
[Large pull requests](#large-pull-requests)). Each request contains:

- the review instructions and the output format;
- the PR's **title**, source **branch** and **description** (the description
  is cut to `diff.max_description_tokens`, 500 by default);
- the PR's existing **comment threads**, as untrusted data (see
  [Discussion awareness](#discussion-awareness)), unless
  `review.max_discussion_tokens` is 0;
- the **diff**, filtered by `ignore.glob`, `ignore.regex` and the built-in
  rules for generated files, lockfiles and binaries, with extra context lines
  around each change;
- your `extra_instructions`, if any, and the output-language instruction.

Never sent: provider tokens, the LLM API key (it is only the `Authorization`
header of the request itself), configuration files, or anything outside the
PR. Commit messages are not part of the review prompt. Comment text is part
of it, unless you turn the discussion off.

The prompts, the model's answer and the diff are never written to the logs,
at any level. A failed review reports one of a fixed set of sentences, never
a response body (see [Troubleshooting](troubleshooting.md)).

If your LLM endpoint is not under your control, remember that the PR text and
code leave your machine for it; point review-mcp only at endpoints you trust
with that code.

## Choosing `llm.context_window`

review-mcp has no model registry. When `llm.context_window` is unset it reads
the window from the endpoint (`GET {llm.base_url}/models`, 90 % of the value
reported for `llm.model`); to set it yourself, give the real context window,
in tokens, with `llm.context_window` (`REVIEW_MCP_LLM_CONTEXT_WINDOW`, at least
4096), which always wins. Use the limit your server actually runs the model with; some servers start a
model with less than it supports.

The diff gets what is left after the output reserve and the prompt:

```
soft limit = context_window - max(max_output_tokens, 1000) - 500 - prompt tokens
```

`llm.max_output_tokens` is optional. If you set it, make it generous enough for
a review with all fields enabled and several findings (a few thousand tokens);
an answer cut off by the limit is reported in the notes.

`diff.max_tokens` (`REVIEW_MCP_DIFF_MAX_TOKENS`, at least 1000, unset by
default) caps the diff budget below that: the soft limit is the smaller of the
formula and the cap. Use it when a large window makes requests slow (for a
local model, `24000` is a reasonable start). The cap applies to each model
call: files that no longer fit are reviewed in further parts (see
[Large pull requests](#large-pull-requests)), and those left after the last
part are listed under Omitted, as with a small window; `pr_ask` uses the same
cap, in its one call. See
[Getting started](getting-started.md#how-the-context-window-shapes-the-diff-budget)
for the full table.

### Prompt tokens

The prompt scaffolding (instructions, output format, empty PR fields) was
measured with the real templates. With every field enabled, a non-English
language and extra instructions it is **2205 tokens** (estimate, including the
safety factor and framing). The other combinations range from 1589 to 2205.
That maximum is the default of `diag diff --prompt-tokens`. On top of it come
the title, branch and description of the actual PR and, when the PR has
comments, the existing-discussion block (at most 1500 tokens by default, header
and fence included); `diag review --dry-run` (below) reports the exact figure
for a given PR.

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
  identified", or the text of the concern ("... identified in the reviewed
  files" when the review is [partial](#partial-reviews)).
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
- **Deleted (listed by name)**: a deleted file whose patch was left out on
  purpose (the compressed diff drops the removed lines of deleted files) and
  whose name was sent in the diff's list of deleted files. The model was told
  the file is deleted, so it counts as reviewed.
- **Omitted**: left out to fit the context window (for a review in parts:
  left after the last part); grouped as added, modified and deleted files. If
  many, only the first 50 are listed, then "and N more".
- **Skipped**: not reviewed, with a reason: `binary`, `file_limit`
  (`diff.max_files_full_content`), `size_limit` (`diff.max_file_bytes` or
  `diff.max_diff_bytes`), `fetch_failed`, `empty_diff`, `unparseable_patch`,
  and for a review in parts `too_large` (the file does not fit a part of its
  own) and `model_call_failed` (the file was in a part whose model call
  failed).
- **Filtered**: excluded by the ignore rules, with the rule that matched
  (`ignore_glob`, `ignore_regex`, `lockfile_or_minified`, `bad_extension`,
  `generated:<framework>`).

A review whose files are mostly omitted or filtered says little about the PR.
Raise `llm.context_window`, narrow the PR, or adjust `ignore.*`. If nothing
reviewable remains after filtering, the model is not called and the review
says "No reviewable changes after filtering."

The structured `coverage` object also carries `deleted_listed` (the deleted
files listed by name), the counts behind the
[partial banner](#partial-reviews) (`partial`, `reviewed_files`, `total_files`
and `not_reviewed_files`), and `model_calls` and `failed_parts` (see
[Large pull requests](#large-pull-requests)).

### Partial reviews

A review is **partial** when at least one changed file with a reviewable text
change was not fully seen by the model: it was omitted to fit the diff budget,
clipped, skipped by the provider because it is too large (`size_limit`,
`file_limit`), could not be read (`fetch_failed`, `unparseable_patch`), did not
fit a part of its own (`too_large`) or was in a part whose model call failed
(`model_call_failed`).
Files excluded on purpose (the **Filtered** group) do not make a review
partial, and neither do binary files and files without a text change (a pure
rename), which have nothing to review. A deleted file listed by name counts as
reviewed; a deleted file that was left out (its name did not fit either) does
count as not reviewed.

A partial review leads with this line, before everything else (in the MCP
text it is the first line, before `## PR Review`):

```text
**Partial review: 2 of 5 changed files were reviewed. 3 files were not reviewed (see Coverage); nothing is concluded about them.**
```

The same sentence is directly under the heading of the published overview
(on Gitea as a warning blockquote, on Bitbucket Server as a bold line), and it
stays there when a later run edits the overview in place. A count of 1 takes
the singular ("1 file was not reviewed ... nothing is concluded about it").

In a partial review the "nothing found" statements are limited to what was
read: "No security concerns identified in the reviewed files", "No performance
concerns identified in the reviewed files" and "No key issues found in the
reviewed files". A complete review keeps the plain sentences.

The structured result has the counts: `coverage.partial`,
`coverage.reviewed_files` (fully included files and deleted files listed by
name), `coverage.total_files` (the
files with a reviewable change) and `coverage.not_reviewed_files`
(omitted, clipped, skipped for size, unreadable, too large for a part and
failed-part files);
`reviewed_files + not_reviewed_files == total_files`. `job_result` returns the
same fields, because it returns the original result unchanged.

To cover every file, see [the review says partial](troubleshooting.md#the-review-says-partial).
The notes also say how: with `diff.max_tokens` as the limit that applied,
"To review every file, raise or unset diff.max_tokens, or use a model with a
larger context window."; otherwise "To review every file, use a model with a
larger context window." When a review in parts used every part
`review.max_chunks` allows and files were still left, the hint also names
`review.max_chunks`: "To review every file, raise review.max_chunks, raise or
unset diff.max_tokens, or use a model with a larger context window." (or
without the `diff.max_tokens` part when the window was the limit). The tool
description tells the client model to report
how many files were not reviewed and never to say that they have no issues.

### Notes

The notes section appears when something about the run deserves attention:

- the answer was cut off by the output limit (raise `llm.max_output_tokens`);
- the first answer could not be parsed as YAML and the review comes from one
  second attempt;
- findings were dropped (more than `max_findings`, or a finding without a file
  or without text), or, for a review in parts, not shown because of
  `review.max_total_findings`;
- a part of a review in parts failed ("Part 2 of 3 failed (llm_timeout); its
  files were not reviewed.");
- files were clipped, or the diff was shortened by the request-size guard;
- a snippet could not be verified.

If the second attempt also cannot be parsed, the review fails with "the model's
answer could not be parsed as a review, also after one retry". Try again, or
check that the model follows YAML output instructions.

## Large pull requests

When the diff of a pull request does not fit one request, `pr_review` reviews
the files that were left out in further model calls, called **parts**, and
merges the answers into one review. A pull request that fits one request is
reviewed exactly as before, in one call.

| Setting | Env | Default | Effect |
|---|---|---|---|
| `review.max_chunks` | `REVIEW_MCP_REVIEW_MAX_CHUNKS` | `8` | the most parts (model calls) of one review, 1 to 32; `1` turns parts off and reviews in one call |
| `review.max_total_findings` | `REVIEW_MCP_REVIEW_MAX_TOTAL_FINDINGS` | `10` | the most findings of the merged review, up to 50 |

How the files are split:

- Part 1 holds the files a single-call review would send, chosen by the same
  ranking and rules; its budget is a few tokens smaller, to leave room for the
  part line below. Each further part takes the files the earlier parts did
  not include, clip or list, in the same ranking and with the same rules. A
  file is in at most one part; a deleted file listed by name is listed in one
  part only.
- A part that fits in full is sent with extended context, like a small pull request.
- When the next file is too large for a further part of its own, it is
  clipped (`diff.large_patch_policy = "clip"`, the default) and is that part's
  content, or, with `skip`, it is skipped with reason `too_large` and packing
  goes on with the next file. It never ends the review.
- Files still left when `review.max_chunks` parts exist are not reviewed; they
  are listed under Omitted, and the review is [partial](#partial-reviews).

Each part is one model call with the same instructions, title, description
and discussion block as a single-call review, plus one line before the diff:
"This pull request is large and is reviewed in N parts. This is part I of N.
Review only the files in the diff below; the other files are reviewed
separately." Each part asks for at most `max_findings` findings and has its own
`llm.timeout_seconds`, YAML repair and re-ask.

The parts run **one after another**, not in parallel: a local endpoint usually
serves one request at a time, so parallel calls would only queue. A review in
N parts therefore takes about N times as long as one call. In stdio mode that
is what [background jobs](#slow-endpoints) are for: the call answers with a
`job_id`, and progress reports `calling model (part I of N)`. In serve mode the
call runs in its request; see [Serve mode](serve.md#long-calls).

How the answers are merged:

- **Findings**: in part order, then in the model's order. A finding an earlier
  part already returned (the same [fingerprint](#no-repeated-findings): file,
  header and text) is dropped. The list is then capped at
  `review.max_total_findings`, and the rest are counted in a note: "N further
  findings were not shown because of review.max_total_findings." Findings that
  are already posted on the pull request are handled as for any review: they
  stay in the overview and are not posted again.
- **Effort**: the highest of the parts.
- **Tests**: "PR contains tests" if any part says so.
- **Security and performance**: the concerns when at least one part has one,
  each prefixed with `Part I:` when more than one part has one. "No ..." only
  when every part that answered says no and no part left the question
  unanswered. When some parts say no and another part did not answer, the
  field stays empty, because "no concerns" would also cover that part's files,
  and a note says so for each such part: "Part 2 did not answer the security
  question; nothing is concluded about its files." (or "the performance
  question"). When no part answers, the field stays empty without a note, as
  in a review in one call.

`review.max_total_findings` must be at least `review.max_findings` when you
set it. When you leave it at its default, a `review.max_findings` above 10 is
still valid: the merged list is then capped at the larger of the two, so a
review never shows fewer findings than one part may return.

**A failed part.** When a part's model call fails (a timeout, an LLM error, or
an answer that cannot be parsed after the re-ask), the review goes on with the
other parts. The failed part's files are skipped with reason
`model_call_failed` and are not reviewed, the review is partial, and a note
names the part and a fixed error class, never the error text: "Part 2 of 3
failed (llm_timeout); its files were not reviewed." The result can be
published as usual. Only when every part fails does the review fail, with the
first part's error.

**What the result says.** The coverage counts the files of every part. The
coverage section ends its counts with "Reviewed in N model calls." when N is
more than 1, in the tool text and in the published overview. The structured
`coverage` has `model_calls` (the parts sent to the model, failed ones
included: 1 for a review in one call, 0 when the model was not called) and
`failed_parts`. `model_calls` counts parts attempted, while
`metadata.llm_calls` counts every chat completion, re-asks included, so a
review in 3 parts with one re-ask has `model_calls` 3 and `llm_calls` 4. In `metadata`, `diff_tokens` is the
sum over the parts and `request_tokens` the largest part's request.
`diag review --dry-run` shows the planned parts the same way, with
`coverage.model_calls` as the number of parts it would send.

`diff.max_tokens` applies to each part: with a cap, each part is smaller and
answers sooner, but the review as a whole is not shorter.

## Publishing

`publish=true` (or `diag review --publish`) posts the review after it was
produced. Nothing is ever posted without it. The comments are rendered for the
provider: Gitea gets the richer GitHub-style markdown (emojis, a table and one
collapsible block per finding); Bitbucket Server gets headings and tables
without raw HTML. The token needs write access to pull requests or comments
(see the [token scopes](setup.md#2-create-tokens) in the setup guide, which
marks them "verify at A3").

What is posted, in this order:

1. The **overview**: one comment with the review's fields (effort, tests,
   security, performance), a findings index, the coverage and the notes.
2. The **inline comments**, one per anchorable finding, unless
   `inline_findings` is false.
3. The overview is edited once so that each finding links to its inline
   comment (and to the file line otherwise).

The overview is posted before the inline comments, so a failed inline batch
never leaves the PR without it. A failure to post never discards the review:
the result carries the review and a `publish` object with `published: false`
and the reason, and `publish.inline` counts `posted`, `skipped_duplicate`,
`unanchorable` and `failed` findings. Publishing a second time is safe for the
overview and for findings already posted (below), but it is still an explicit
write each time.

| Setting | Env | Default | Effect |
|---|---|---|---|
| `review.inline_findings` | `REVIEW_MCP_REVIEW_INLINE_FINDINGS` | `true` | inline comments on publish; the `inline_findings` argument overrides it per call |
| `review.persistent_overview` | `REVIEW_MCP_REVIEW_PERSISTENT_OVERVIEW` | `true` | edit the earlier overview in place; `false` posts a new overview every run |
| `review.max_discussion_tokens` | `REVIEW_MCP_REVIEW_MAX_DISCUSSION_TOKENS` | `1500` | token budget of the discussion block; `0` turns the block off |
| `review.require_performance` | `REVIEW_MCP_REVIEW_REQUIRE_PERFORMANCE` | `true` | the performance field |

### The overview and its marker

The overview is **one comment per pull request and per token user**. Its last
line is a marker, `[//]: # (review-mcp:overview:v1)`, a Markdown link reference
that both Gitea and Bitbucket render as nothing. On the next publish,
review-mcp lists the PR's general comments and takes the ones whose last line is
exactly that marker **and** whose author is the token's own user; it edits the
newest in place, so the PR keeps one overview with an updated time and head
commit. Older matches are left alone, with a note.

A marker in a comment written by anyone else is ignored: that person could have
planted it, and review-mcp never edits or adopts a comment it did not write.
`pr_comment_create` and `pr_comment_reply` refuse a body that contains a marker
line, so a comment posted for you cannot be mistaken for an overview either.

If the old overview cannot be edited (permissions, or it was deleted), or the
lookup fails, a new overview is posted and a note says so; the review itself
never fails because of it. Set `review.persistent_overview` to false to post a
new overview on every run.

### Inline comments and anchors

A finding names a file and a line range. It is **anchorable** when the first
line of that range is a line the provider shows in the PR's diff on the new
side: an added line or one of the context lines around a change. Anchors are
resolved on the provider's own hunks, not on the extra context the model saw,
because a server only accepts comments on lines of its own diff. A finding in
a deleted or binary file, or on lines outside every hunk, is not anchorable.

- Anchors are single-line on both providers (the first line of the range; the
  comment says "Lines 40–52" when the range is longer).
- **Gitea:** one review per run, event `COMMENT`, pinned to the head commit.
  review-mcp never leaves a pending review behind, and it refuses to post when
  the token's user already has a pending (draft) review on the PR, because
  Gitea would submit that draft together with the comments.
- **Bitbucket Server:** one comment per finding, with the line type (`ADDED` or
  `CONTEXT`) computed from the hunk, never assumed; a renamed file carries its
  old path.
- Findings that are not anchorable, or whose inline comment failed, stay in the
  overview and a note counts them ("N findings could not be placed on a changed
  line and are listed in the overview only").

### No repeated findings

Each inline comment ends with a hidden marker holding a **fingerprint** of the
finding: a hash of the file path, the header and the first 200 characters of
the explanation, lower-cased with whitespace collapsed. The line number is left
out on purpose, so a push that moves the code does not make the finding new.
Before posting, review-mcp collects the fingerprints of its own inline comments
on the PR and skips findings that are already there (`skipped_duplicate`, and a
note). Only comments by the token's own user count.

## Discussion awareness

Before it calls the model, `pr_review` reads the PR's comment threads (general
and inline, resolved ones marked as resolved) and puts them in the prompt, so
the model does not report what people already raised. Its own earlier
comments (marker and author) are left out.

**The comment text is untrusted data.** It is written by third parties and may
try to steer the model ("ignore previous instructions..."). The block is
introduced as data, not instructions; each comment is sanitized and cut at 600
characters, at most two replies are shown per thread, the whole block sits
inside a fence the text cannot close, and the block is capped by
`review.max_discussion_tokens` (default 1500, counted before the diff budget;
whole threads are dropped, newest kept first, and a note says how many). A
prompt-injection attempt in a comment cannot be ruled out by construction, so
treat the model's output as you would any review: read it. The comment text is
never written to the logs.

- The overview shows an "Already discussed" count when the review skipped points
  because of the discussion.
- If the threads cannot be read, the review continues without them and a note
  says "findings may repeat" the discussion.
- If the context window is too small to hold both the discussion and the diff,
  the discussion is dropped and the diff wins.
- `review.max_discussion_tokens = 0` turns the block off: the comments are then
  not sent to the LLM. With `publish` and inline comments on, the PR is still
  read to skip findings that were already posted.

## Slow endpoints

A review on a slow model, such as a local one or any model on a large diff,
can take minutes. Many MCP clients give up on a tool call after about 60
seconds, whatever the server is doing. In stdio mode `pr_review` therefore
waits at most `wait_seconds` for the review: the argument, or
`llm.wait_seconds` (`REVIEW_MCP_LLM_WAIT_SECONDS`, default 45, 0 to 600).

- If the review finishes in time, the result is exactly the one described
  above; nothing about it changes.
- If not, the call answers at once with a running status. It is not an
  error. The text is

  ```text
  The review is still running (stage: calling model, 45 s so far). Call `job_result` with job_id `job_…` to get the result.
  ```

  and the structured content is
  `{"status": "running", "job_id": "job_…", "stage": "calling model", "elapsed_seconds": 45}`.
  The stage is one of the progress stages, or `starting` before the first.

The review keeps running in the server. Call `job_result` with that `job_id`
and, optionally, its own `wait_seconds` (same range and default). It waits
again and returns one of:

- the finished review, exactly as `pr_review` would have returned it,
  including the `publish` outcome;
- the review's error, as a tool error with the usual fixed sentence;
- the running status again, when the review is still not done.

A client that prefers polling passes `wait_seconds: 0` and gets the running
status at once.

**Why the client timeout no longer matters.** Every call, `pr_review` and each
`job_result`, answers within `wait_seconds`, however long the model takes. At
the default of 45 that stays under a typical 60-second client timeout. If
your client gives up sooner, lower `wait_seconds`. `llm.timeout_seconds` still
bounds the model request itself. While a call waits, a client that sent a
progress token keeps receiving the stages; a client that resets its timeout
on progress may then never see the running status at all.

Good to know:

- With `publish=true` the run itself posts the comments, so they appear even
  if `job_result` is never called.
- At most 4 reviews and answers run in the background at once. A fifth call
  gets "too many background jobs are running; wait for one to finish".
- A finished result is kept for 30 minutes, and at most 64 results are kept
  (the oldest goes first). After that, or for an id the server never issued,
  `job_result` answers "unknown or expired job_id". Results live in the
  server's memory only; nothing is written to disk.
- Ending the server (the client closes it, or SIGINT or SIGTERM) cancels the
  runs that are still going.
- Argument errors, a pull request URL on no configured host and, in serve
  mode, missing credentials still fail at once, before any run starts.
- Serve mode has no background jobs: calls run in their request, and
  `wait_seconds` is ignored. See [Serve mode](serve.md#long-calls).

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
  "budget": { "context_window": 32000, "soft_limit": 28200, "hard_limit": 28700, "prompt_tokens": 2300, "factor": 0.3, "limit": "context_window" },
  "tokens": { "prompt": 2300, "diff": 5120, "request": 7480, "context_window": 32000 },
  "fast_path": true,
  "coverage": { "included": ["src/app.go"], "clipped": [], "omitted": { "added": [], "modified": [], "deleted": [] }, "skipped": [], "filtered": [] },
  "notes": [],
  "elapsed_ms": 120
}
```

`budget.limit` says what bounds the soft limit: `context_window`, or
`diff.max_tokens` when that cap is set and lower (the report then also has
`budget.max_diff_tokens`). `tokens.request` is the estimate of the whole
request; compare it with `context_window`. If `fast_path` is false, the diff did not fit whole and the
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
