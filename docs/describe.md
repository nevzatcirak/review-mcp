# Describing pull requests with `pr_describe`

`pr_describe` reads a pull request and asks your LLM for a description of it:
a title, the change types (bug fix, tests, enhancement, documentation, other),
a short summary and a walkthrough of the changed files. By default it only
reads: the description comes back to your client and nothing is written to the
pull request. With `publish=true` it writes it to the pull request, either as
one comment that later runs edit in place, or inside a marked region at the
end of the pull request description (see [Publishing](#publishing)).

## Calling the tool

| Argument | Required | Meaning |
|---|---|---|
| `pr_url` | yes | The pull request URL, on a configured Gitea or Bitbucket Server host. |
| `output_language` | no | Locale code for the description text, such as `en-US` or `tr-TR`; replaces `output.language`. Same format as the config key. It applies to every model call of the run, the summary call of a [large pull request](#large-pull-requests) included. |
| `publish` | no | `true` writes the description to the pull request. Default `false`. |
| `publish_mode` | no | Where `publish` writes: `comment` (the default) or `description`. |
| `update_title` | no | With `publish=true` and `publish_mode=description`, also replaces the pull request title with the generated one. Default `false`. |
| `wait_seconds` | no | How long the call waits for the description before it answers with a `job_id`, 0 to 600; replaces `llm.wait_seconds` (default 45). stdio only; see [Slow endpoints](#slow-endpoints). |

There are no extra instructions for this tool, and no setting for the number
of files or parts beyond the ones the other tools use (`review.max_chunks`,
`diff.max_tokens`, `llm.context_window`).

An invalid `output_language`, a `publish_mode` other than `comment` or
`description`, `update_title` without `publish=true` and
`publish_mode=description`, and a `wait_seconds` outside 0 to 600 are rejected
with a fixed message before anything is sent to the provider or the LLM. The
messages are "output_language must be a locale code such as en-US or tr-TR",
"publish_mode must be comment or description" and "update_title needs
publish=true and publish_mode=description"; none of them repeats your input.

`pr_describe` is not marked read-only for MCP clients, because `publish=true`
writes to the pull request. It is marked not destructive: it creates or edits
its own comment, or the region it owns, and never anything else.

The result has two parts: the description as portable markdown (the text
content; no raw HTML) and the same data as structured content (`title`,
`type`, `description`, `files`, `coverage`, `notes`, `metadata` and, when
publishing was requested, `publish`). While it runs, a client that sent a
progress token receives the stages `fetching`, `preparing diff`,
`calling model` and `rendering`. A description in parts reports
`calling model (part 1 of 3)`, `calling model (part 2 of 3)` and so on instead
of `calling model`, and `calling model (summary)` for the last call (see
[Large pull requests](#large-pull-requests)).

## What is sent to the LLM

One chat completion request per description (two if the first answer cannot
be parsed, see below) goes to `llm.base_url`; a large pull request is
described in several parts, one request each, and then one more request for
the summary. Each request with the diff contains:

- the description instructions and the output format;
- the PR's **title**, source **branch**, **target branch** and **description**
  (the description is cut to `diff.max_description_tokens`, 500 by default);
- the PR's **commit messages**, oldest first and numbered, cut to
  `diff.max_commits_tokens` (500 by default); empty messages are skipped;
- the **diff**, filtered by `ignore.glob`, `ignore.regex` and the built-in
  rules for generated files, lockfiles and binaries, in its plain form (`+`,
  `-` and space prefixes, no line numbers);
- the output-language instruction.

Never sent: provider tokens, the LLM API key (it is only the `Authorization`
header of the request itself), configuration files, the PR's comments, or
anything outside the PR. `pr_describe` describes the change, not the
conversation about it, so the discussion is not part of the prompt, and
[repository context](repo-context.md) is not used even when it is on
(`coverage.repo_context` always reads `off`).

If the provider cannot return the commit messages, the description is made
without them and the notes say "The commit messages could not be read from the
provider; the description was generated without them."

The model is told that the previous title, description and commit messages may
be partial or out of date and are only a reference, that it must describe only
the files whose changes the diff shows, and that the diff may list files by
name that were left out because of its size and that it must not describe
those.

The prompts, the model's answer and the diff are never written to the logs, at
any level. A failed description reports one of a fixed set of sentences, never
a response body (see [Troubleshooting](troubleshooting.md#pr_describe)). As
with `pr_review`, the PR text and code leave your machine for the LLM
endpoint: point review-mcp only at endpoints you trust with that code.

The context-window budget works as for reviews; see
[Reviewing pull requests](review.md#choosing-llmcontext_window). The title,
description and commit messages count as prompt scaffolding. If nothing of the
diff fits, the call fails with "the pull request diff does not fit the
configured context window; raise llm.context_window
(REVIEW_MCP_LLM_CONTEXT_WINDOW) or narrow the pull request" and nothing is
sent to the model. If nothing reviewable remains after filtering, the model is
not called, the description is empty and the notes say "No reviewable changes
after filtering."

## Reading the result

The markdown starts with the [partial banner](#the-partial-description-banner)
when the description is partial, then these sections in a fixed order:

- **Title**: the generated title, or "(not generated)".
- **Type**: one or more of `Bug fix`, `Tests`, `Enhancement`, `Documentation`,
  `Other`, or "(not generated)". A value outside this list is dropped, with a
  note.
- **Summary**: up to four bullets, or "(not generated)".
- **Walkthrough**: per described file its path, a label (free text, one line,
  at most 40 characters) and a one-line title, then a summary of the file's
  changes indented below. "No file was described." when there is none.
- **Not described**, only when files were not described: every such file with
  its reason (see [Not described](#not-described-and-why)).
- **Coverage**, always, and **Notes**, when there are notes. The coverage
  section is the one of a review (see
  [Reading the result](review.md#coverage)) with "Described" for "Reviewed":
  "Described in N model calls." when there was more than one.
- **Publishing**, only when `publish=true`: what was written, as a fixed
  sentence (see [Publishing](#publishing)).

```text
## Title

Raise the retry count and test it

## Type

Enhancement, Tests

## Summary

- Raise the retry count to three
- Add a retry test

## Walkthrough

- `src/app.go` (enhancement): Raise the retry count
  - Raise `retries` from 1 to 3
- `src/app_test.go` (tests): Add a retry test
  - Add `TestRetries`

## Coverage

- Included: 2 files
- Omitted: 1 file

Filtered: `ignore_glob` (1):

- `vendor/lib.go`
```

The structured result carries the same data. `title`, `type` and `description`
are `null` when they were not generated. `files` has one entry per described
file (`path`, `title`, `summary`, `label`), in the order the model returned
them, parts in order. `coverage` is the coverage object of the other tools;
"reviewed" in its field names reads "described" (`reviewed_files` counts the
described files, `not_reviewed_files` the files that were not described), and
it has `model_calls` and `failed_parts`. `metadata` has names and numbers only
(`model`, `context_window`, `prompt_tokens`, `diff_tokens`, `request_tokens`,
`fast_path`, `llm_calls`, `commit_messages`, `repair_tactic`, `reasked`,
`truncated`, `diff_trimmed`). `publish` has `published`, `mode`, and, when they
apply, `comment_id`, `url`, `error`, `updated` and `title_updated`.

The title and the labels are single lines written by the model and are
escaped, the summaries are markdown bullet lists and are shown as they are.

## Large pull requests

A diff that leaves files out is described in parts, exactly as `pr_review`
reviews in parts (see [Large pull requests](review.md#large-pull-requests)).
This happens when the prepared diff does not hold every file and
`review.max_chunks` is greater than 1 (default 8, 1 to 32). With
`review.max_chunks = 1`, or when the files do not split into at least two
parts, the description is one call and the files that do not fit are listed as
not described.

- The files are packed into at most `review.max_chunks` parts by the same
  ranking and admission rules as a review; a file is in at most one part. The
  parts run one after another, each with its own timeout, YAML repair and
  re-ask. Each part's prompt has one line before the diff: "This pull request
  is large and is described in N parts. This is part I of N. Describe only the
  files in the diff below; the other files are described separately."
- **Each part describes only its own files.** A part asks for the files
  walkthrough only (no title, type or summary), and an entry for a file that is
  in another part, or not in the diff at all, is dropped and counted in a note.
- After the last part, **one more call, the summary call**, writes the title,
  the types and the summary. It carries no diff: its input is the PR title,
  description, branches and commit messages plus the walkthrough of all parts
  (each file's path, title and summary). Its prompt is written for review-mcp;
  it tells the model to base the answer on the walkthrough and not to mention
  files that are not in it.
- The summary call is measured against the context window first. If the
  walkthrough with the files' summaries does not fit, the call is made with the
  files' titles only, and the notes say "The summary was generated from the
  files' titles only; their summaries did not fit the context window." If not
  even the titles fit, no call is made and the fallback below applies.
- **If the summary call fails** (or cannot be made), `title` and `type` are
  `null`, `description` is the titles of the described files, one bullet per
  file, and the notes say "The summary could not be generated; the walkthrough
  lists the described files." The walkthrough itself is unaffected.
- **A part that fails** (timeout, LLM error, unparseable after the re-ask) does
  not fail the description. Its files are not described (skipped with reason
  `model_call_failed`), `coverage.failed_parts` counts it, and the notes say
  "Part 2 of 3 failed (llm_timeout); its files were not described." The class
  in parentheses is fixed and never contains the error text (see
  [Part I of N failed](troubleshooting.md#part-i-of-n-failed)). Only when every
  part fails does the call fail, with the first part's error.

Because the parts run one after another, a description in N parts takes about
N + 1 times as long as one call. See [Slow endpoints](#slow-endpoints).

## Not described, and why

The description never claims a file it did not see. Every changed file is
exactly one of: described (it has a walkthrough entry), listed under **Not
described** with its reason, or left out on purpose (filtered by the ignore
rules or generated-file rules, binary, or without a text change). A file the
model was not shown never gets a walkthrough entry, and the model is not asked
to pad one.

| Reason shown under Not described | Coverage category | What happened |
|---|---|---|
| included only in part (clipped to fit the context window) | `clipped` | The model saw only the beginning of the file. |
| left out to fit the context window | `omitted` | The file did not fit the diff budget, or was left after the last part. |
| its part's model call failed | `skipped`, `model_call_failed` | The file was in a part whose call failed. |
| too large for a part of its own | `skipped`, `too_large` | `diff.large_patch_policy = "skip"` and the file does not fit a part. |
| shown to the model, but it returned no walkthrough entry | `skipped`, `not_returned` | See below. |
| skipped: `size_limit` (also `file_limit`, `fetch_failed`, `unparseable_patch`) | `skipped` | The provider could not return the file; see [Reading the result](review.md#coverage) of a review. |

At most 50 files are listed; the rest are counted ("and N more (not listed; at
most 50 files are listed)"). The coverage section lists the same files by
category.

**`not_returned`.** The model was shown the file's diff but its answer had no
usable entry for it: it left the file out, gave the path wrongly, or gave an
entry with neither a title nor a summary. PR-Agent pads the walkthrough with a
placeholder line for such files; review-mcp does not. The file is counted as not
described, and the notes say "1 file was shown to the model but got no
walkthrough entry; listed as not described (see Coverage)." (or "N files were
shown to the model but got no walkthrough entry; listed as not described (see
Coverage)."). A model that stops early on a large pull request shows up here.
Run again, or use a model that follows the output instructions better.

**A clipped file with an entry.** When the model saw only part of a file but
described it, the entry stays in the walkthrough and its line ends with
"(partial: only part of this file was shown)". The file is still counted as not
described and listed under Not described, so a count and an unmarked entry never
contradict each other. The mark is in the client view and in the published
description.

### The partial description banner

A description is **partial** under the same rule as a
[partial review](review.md#partial-reviews): a changed file with a reviewable
text change was omitted to fit the budget, clipped, skipped by the provider for
size or unreadable, did not fit a part, was in a failed part, or was not
returned by the model. Filtered files, binary files and files without a text
change do not count. A partial description leads with this line, before
everything else (in the MCP text it is the first line, before `## Title`):

```text
**Partial description: 2 of 5 changed files were described. 3 files were not described (see Coverage).**
```

In a published comment or region the line is directly under the
`PR Description` heading (on Gitea as a warning blockquote, on Bitbucket Server
as a bold line), and because later runs replace the whole text, every edit
carries it. A count of 1 takes the singular ("1 file was not described").
The structured `coverage` has the counts: `partial`, `reviewed_files`,
`total_files` and `not_reviewed_files`, with
`reviewed_files + not_reviewed_files == total_files`. Treat the walkthrough of
a partial description as covering only the files it lists. The tool description
tells the client model to say how many files were not described and never to
present the walkthrough as covering them.

The notes of a partial description say how to cover every file, with
"describe" for "review": "To describe every file, raise or unset
diff.max_tokens, or use a model with a larger context window.", "To describe
every file, use a model with a larger context window." and, when a description
in parts used every part `review.max_chunks` allows, "To describe every file,
raise review.max_chunks, raise or unset diff.max_tokens, or use a model with a
larger context window." (or without the `diff.max_tokens` part). Files the
provider skipped get "Some changed files were skipped or could not be read from
the provider (see Coverage); a larger diff budget does not change that."

### Counting model calls

Two numbers count calls, and they differ on purpose:

- `coverage.model_calls` counts the calls **with a diff**: 1 for a description
  in one call, N for a description in N parts, 0 when the model was not
  called. The summary call and re-asks are not counted. The coverage section
  says "Described in N model calls." when N is greater than 1.
- `metadata.llm_calls` counts **every chat completion**: the parts, the summary
  call and every re-ask, also those of a call that then failed.

A description in 3 parts with no failures and no re-asks has `model_calls: 3`
and `llm_calls: 4`.

### Notes

The notes section appears when something deserves attention. Besides the notes
above:

- "The model's answer was cut off by its output limit; the description may be
  incomplete." (raise `llm.max_output_tokens`);
- "The model's first answer could not be parsed as YAML; the description comes
  from a second attempt.";
- "N type values outside the allowed list (Bug fix, Tests, Enhancement,
  Documentation, Other) were dropped." (singular: "1 type value ... was
  dropped.");
- "N walkthrough entries for a file that was not in the diff shown to the model
  were dropped." (singular: "1 walkthrough entry ... was dropped.");
- "N duplicate walkthrough entries were dropped; the first entry of each file is
  kept." (singular: "1 duplicate walkthrough entry was dropped; ...");
- "N files were included only in part (clipped) to fit the context window." and
  "The diff was shortened to fit the context window; the coverage section lists
  the files that are incomplete or left out.";
- the publishing notes below.

If an answer cannot be parsed, the same call is asked once more with a note
that the previous answer was not valid YAML. If the second answer cannot be
parsed either, that call fails with "the model's answer could not be parsed as
a pull request description, also after one retry; try again, or check that
llm.model follows the YAML output instructions". For a part or for the summary
call this means a failed part or the summary fallback; for a description in one
call it fails the tool call.

## Publishing

`publish=true` writes the description after it was produced. Nothing is ever written without it. A
failure to write never discards the description: the result carries it and a
`publish` object with `published: false` and the reason, and the Publishing
section of the text says "not written" with the same sentence. Publishing is
rendered for the provider: Gitea gets headings with emojis and a warning
blockquote for the banner; Bitbucket Server gets plain headings and no HTML
anywhere. Both have the sections Title, Type, Summary, Walkthrough, Not
described (when needed), Coverage and Notes under one `PR Description` heading.

The model's text is escaped before it is published, as `pr_review` escapes a
finding's text: markdown control characters are escaped, on Bitbucket Server
no raw HTML can come out of a summary, and the bullet markers of the summaries
are kept so that they stay lists. This also means model text cannot form one of
the marker lines below. The backticks the prompt asks for therefore show as
literal backticks in a published description, as they do in a published review.
On a provider where a line that starts with `/` runs a quick action, every
published line that starts with `/` gets a leading space (the same rule as
`pr_ask`); neither Gitea nor Bitbucket Server runs such actions, so today this
changes nothing.

### Comment mode

`publish_mode=comment` (the default) posts one PR-level comment whose last line
is the marker `[//]: # (review-mcp:describe:v1)`. It is a markdown link
reference definition and renders as nothing.

- On a later run the comment is found by its marker, on the last line, and its
  author, which must be the token's user, and **edited in place**. There is no
  setting to turn this off.
- If several comments of yours carry the marker, the newest is edited and the
  others are left as they are and never deleted; the notes say "1 older
  description comment by the same user was left unchanged." (or "N older
  description comments by the same user were left unchanged.").
- If the comments cannot be read to look for the old one, a new comment is
  posted and the notes say "The existing description comment could not be
  looked up; a new one was posted." If the old comment cannot be edited, a new
  one is posted and the notes say "The previous description comment could not
  be updated; a new one was posted."
- The comment is posted even when nothing was described, with the coverage and
  the notes, as `pr_review` publishes an empty review.

The comment is separate from the pull request description, so it can be
deleted like any comment, and the description is never touched. The
`pr_review` overview and the description comment are different comments with
different markers.

### Description mode

`publish_mode=description` puts the description into the pull request
description, without losing what the author wrote. review-mcp owns only a
**region**: the lines from this start marker to this end marker, both
included:

```text
[//]: # (review-mcp:describe:start)
## PR Description
...
[//]: # (review-mcp:describe:end)
```

Both markers are markdown link reference definitions, so they render as
nothing.

- **First run:** the region is appended after the author's text, separated from
  it by one blank line (nothing is added if the text already ends in a blank
  line; an empty description becomes the region alone). The author's text is
  not trimmed or changed.
- **Later runs:** only the region is replaced. Every byte before the start
  marker line and after the end marker line stays as it was: line endings
  (CRLF or LF), trailing spaces, emoji, fenced code blocks. The region itself
  is written with the line endings of the description (CRLF when its first line
  break is CRLF, else LF).
- **What is a marker line:** a line that, after removing spaces and tabs at
  both ends, equals the marker exactly (upper and lower case matter). A line
  ending in CRLF, trailing spaces or an editor's indentation is still a marker;
  anything else on the line is not.
- **Fences are not special.** A marker line inside a fenced code block counts
  like any other. Tracking fences would let an unclosed fence in the author's
  text hide the region on the next run and append a second one.
- **A damaged region is refused.** Two or more start markers, two or more end
  markers, an end marker before a start marker, a start without an end, or an
  end without a start: nothing is written, and the publish outcome is "The pull
  request description contains a damaged review-mcp region; fix or remove it
  and run again." See [Troubleshooting](troubleshooting.md#pr_describe).
- **An up-to-date description is not rewritten.** If running again would write
  the same region and the same title, nothing is sent, so the pull request gets
  no new history entry (and Bitbucket Server no new version). The result still
  says `published: true`.

**Removing the region.** Edit the pull request description in the web UI and
delete both marker lines and everything between them. The author's text stays.
The next `publish_mode=description` run appends a new region. Deleting only one
of the two markers makes the region damaged and the next run refuses.

**The title.** `update_title=true` replaces the pull request title with the
generated one, folded to one line; without it the title is never touched. If
no title was generated (the summary call failed), the title is left alone and
the notes say "The title was not generated, so the pull request title was not
changed." A title that was replaced is reported as `publish.title_updated` and,
in the text, "Pull request title: replaced with the generated one".

On Gitea a pull request is a draft when its title starts with a work-in-progress
prefix, so replacing the title could change the draft state. review-mcp
therefore:

- removes a leading `WIP:` or `[WIP]` (any case) from the generated title, so a
  generated title never turns a pull request into a draft;
- puts the pull request's own prefix, exactly as it is written, in front of the
  generated title when the server reports the pull request as a draft and its
  title starts with one of these prefixes.

Only the default Gitea prefixes `WIP:` and `[WIP]` are known: the server does
not report its `WORK_IN_PROGRESS_PREFIXES` setting, so a pull request that is a
draft through another prefix loses it when the title is replaced. Bitbucket
Server keeps the draft state as a flag; the title is replaced as it is.

**Concurrent edits.** A person may edit the description while a description is
being generated. review-mcp never overwrites such an edit:

1. The pull request is read again right before the write.
2. If its description differs from the text the region was computed on, the
   region is computed again on the fresh text, once.
3. On Bitbucket Server the write carries the version of that fresh read; if
   someone changed the pull request after it, the server answers with a
   conflict and review-mcp reads again. This uses the same single retry.
4. If the text changes or conflicts a second time, nothing is written and the
   outcome is "The pull request description changed while it was being
   updated; nothing was written." Run again.

Gitea has no version on a pull request, so an edit that lands between the
re-read and the write, a window of one request, cannot be detected there.

### Bitbucket Server rewrites the whole pull request

Bitbucket Server updates a pull request with a full `PUT`, not a patch. To
keep everything the author set, review-mcp sends the title and description
(the generated ones or the current ones), the **reviewers** from a fresh read
(by user name), the **draft flag** when the server reports it, and the version;
the target branch is not sent. It then compares the reviewers and their review
states with those read just before the write:

- If a reviewer is missing or a state differs, the notes say "Bitbucket Server
  changed the reviewer list or a review state while the description was
  updated; check the pull request." The description was written and the change
  cannot be undone; look at the pull request and ask the reviewer to approve
  again if needed.
- If the reviewers could not be read before or after the write, the notes say
  "The reviewers could not be read around the description update, so a change
  to the reviewer list or a review state could not be ruled out; check the pull
  request."
- A reviewer without a user name cannot be sent back, so the update is refused
  before any write with "the server sent an unexpected response: a reviewer
  cannot be named, so the update was not sent".

Gitea uses a patch that names only the fields it changes, so its reviewers,
labels, assignees and base branch are not touched.

### Nothing described

If no file was described and there is no summary, description mode writes
nothing and sends no request: an empty region would replace an earlier good one.
The publish outcome is "Nothing was described, so the pull request description
was not changed." Comment mode still publishes the coverage and the notes.

### Permissions

Publishing needs write access to the pull request. The token of the user
that runs review-mcp must be able to:

| | Comment mode | Description mode (and `update_title`) |
|---|---|---|
| Gitea | write access to issues (`write:issue`): post and edit PR comments | write access to the repository (`write:repository`): edit the pull request |
| Bitbucket Server | repository write permission: post and edit comments | repository write permission: update the pull request |

All scopes are unconfirmed until the in-use acceptance has checked them; see
[Token scopes](troubleshooting.md#token-scopes) and the endpoints in the
[Setup guide](setup.md#2-create-tokens). A token without the right gets
"authentication failed: check the token and its scopes (HTTP 403)" as the
publish outcome; the description is still returned. A provider that cannot edit
the description gets "This provider does not support editing the pull request
description; use publish_mode=comment." and no request is made. Reading needs
only a read token.

## Slow endpoints

`pr_describe` handles a slow model the way `pr_review` does; the details are
in [Reviewing pull requests: Slow endpoints](review.md#slow-endpoints). In
short, in stdio mode the call waits at most `wait_seconds`, then answers
without an error with "The description is still running (stage: …, … s so far).
Call `job_result` with job_id `job_…` to get the result." and
`job_result` returns the finished description exactly as `pr_describe` would
have. With `publish=true` the run itself publishes, so the comment or region is
written even if `job_result` is never called. Serve mode has no background jobs
and ignores `wait_seconds`; set the client's tool timeout to fit a description
in parts.

## Checking the budget with `diag describe`

```sh
review-mcp diag describe https://your-gitea.example/octo/demo/pulls/7 --dry-run
review-mcp diag describe https://bitbucket.example.com/projects/PROJ/repos/demo/pull-requests/7
```

`--dry-run` runs everything up to the model calls and prints a JSON report; the
model is not called and nothing is written. It needs the same configuration as
a real call (the LLM API key must be set, but it is not used). The report has
the budget, the token estimates of the prompt and the diff, the `coverage` of
the description (for a description in parts, of all parts), the notes known so
far, `commit_messages` (how many were read) and `elapsed_ms`. It contains no
prompt and no PR text.

Without `--dry-run`, `diag describe` runs the full call and prints the markdown
the tool returns; `--json` prints the structured result instead. `--show-prompt`
prints the rendered system and user prompts of the first call to stdout, after
the output, under the lines `--- system prompt ---` and `--- user prompt ---`:
this is what the model receives, including the PR description and the diff, so
handle it like the PR itself. The prompts are never logged. `--dry-run` cannot
be combined with `--json`. `diag describe` never writes to the pull request:
there is no publish option, use the tool for that. Usage errors exit 2 and
send nothing; a failure exits 1.
