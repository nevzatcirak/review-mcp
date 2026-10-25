# GitLab

review-mcp reads and writes merge requests on GitLab (gitlab.com and
self-managed GitLab 16.0 or newer) through the REST API v4 and an access
token. Every tool works: `pr_review`, `pr_ask`, `pr_describe`, `pr_improve`,
`pr_comments`, `pr_comment_reply`, `pr_comment_create` and `pr_info`. GraphQL
is not used (see [What is not supported](#what-is-not-supported)). Elsewhere
in the documentation a merge request is called a pull request; the tools take
a merge request URL in `pr_url`.

This page is the reference for what is specific to GitLab. Installation and
the LLM endpoint are in the [Setup guide](setup.md); the fixed sentences you
may see are collected in [Troubleshooting](troubleshooting.md#gitlab).

> **Not checked live yet.** The provider is tested against fake servers (and a
> real `git` for the repository context). The live acceptance of rc.6 runs on a
> personal project on gitlab.com (Free tier) inside a nested group, after the
> release candidate is published
> ([below](#still-to-be-checked-at-live-acceptance)). Until then every statement
> about what GitLab answers is derived from its documentation and from the
> code, and each place where that matters says so. What cannot be checked on
> the Free tier at all (required approvals above 0, merged-results pipelines)
> will stay unverified.

## Configuration

| Key | Env | Default | Notes |
|---|---|---|---|
| `gitlab.base_url` | `REVIEW_MCP_GITLAB_BASE_URL` | none | The web base that merge request URLs start with; it may include a context path such as `https://example.com/gitlab`. **No default**: GitLab is enabled only when this is set (`https://gitlab.com` is not assumed). |
| (secret) | `REVIEW_MCP_GITLAB_TOKEN` | none | A personal, project or group access token. Required when `gitlab.base_url` is set (stdio). Secrets are never in the TOML file. |
| `gitlab.ca_cert` | `REVIEW_MCP_GITLAB_CA_CERT` | none | A PEM bundle for a private CA, used in addition to the system roots. |
| `gitlab.insecure_skip_verify` | `REVIEW_MCP_GITLAB_INSECURE_SKIP_VERIFY` | `false` | Turns TLS verification off; review-mcp warns at startup. Prefer `ca_cert`. |

There is no key for the API address: it is always `{gitlab.base_url}/api/v4`.

In `serve` mode the token comes from the request header
`X-Review-MCP-GitLab-Token`, and `REVIEW_MCP_GITLAB_TOKEN` must be unset (see
[Serve mode](serve.md#the-header-contract)). The token is sent as
`PRIVATE-TOKEN`, never logged and never stored. Every request also sends
`Accept: application/json` (raw file contents use `Accept: */*`) and a
`User-Agent` of `review-mcp/<version>`.

`server_info` lists the enabled provider with its base URL, its API URL
(`{base}/api/v4`), and the token as set or unset.

Configuration sentences are listed under
[Troubleshooting](troubleshooting.md#configuration-sentences-gitlab).

## Token

review-mcp acts as the token's user: it never uses a service account of its
own. Three kinds of token work, and all are sent the same way:

| Token type | What to grant |
|---|---|
| **Personal access token** (Preferences, Access tokens) | Use a **legacy** personal access token with the scopes `api` and `read_repository`. `api` covers reading, publishing, replying and creating comments; `read_repository` is for the [repository context](#repository-context). `read_api` cannot publish, and in testing GitLab refused it, and fine-grained personal access tokens, for review-mcp's requests. |
| **Project access token** or **group access token** (Settings, Access tokens of the project or group) | Role **Reporter** for reads, **Developer** to publish. Scopes `api` and `read_repository`, as for a personal token. The token's user is a bot, so review-mcp's comments are written under the bot's name. |

| Use | Needs |
|---|---|
| `pr_review`, `pr_ask`, `pr_describe`, `pr_improve` with `publish=false`, `pr_comments`, `pr_info` | read access (the role Reporter for a project or group token; the `api` scope of a personal token): the file contents, the directory listings, the commit statuses and the approvals |
| `publish=true`, `pr_comment_reply`, `pr_comment_create` | `api` (Developer) |
| `pr_describe` with `publish_mode=description` (and `update_title`) | `api` (Developer): it edits the merge request itself |
| The [repository context](#repository-context) | `read_repository` (Reporter), in addition |

Two things on GitLab's side can look like a token problem:

- A project or group access token sees only its own project or group. A
  merge request outside it answers `404`.
- A user who cannot see a private project gets `404`, not `403`.

Every scope in this table is derived from the API calls the code makes and is
unconfirmed until the live acceptance has used a token with exactly these
scopes. `DELETE` of an own note with a project or group access token is one of
the items still to be checked.

## Merge request URLs

A merge request URL is
`{gitlab.base_url}/{namespace}/{project}/-/merge_requests/{iid}`:

```text
https://gitlab.example.com/group/project/-/merge_requests/7
https://gitlab.example.com/group/sub/project/-/merge_requests/7/diffs
https://example.com/gitlab/group/sub/sub2/project/-/merge_requests/7/commits
```

- Scheme, host, port and path prefix must match the configured base URL
  exactly (the host is case-insensitive; default ports are implied). A URL on
  `gitlab.com` is **not** resolved unless `gitlab.base_url` is configured.
- The namespace is every segment between the base (with its context path) and
  the project, and there must be at least one: `user/project` and
  `group/sub/sub2/project` both work. The project id sent to the API is the
  URL-encoded full path (`group%2Fsub%2Fproject`).
- Only the `/-/` form is accepted. The older form without `/-/`
  (`.../group/project/merge_requests/7`) is refused.
- One of `/diffs`, `/commits` or `/pipelines` may follow the number, and a
  `#fragment` is ignored. Any other tail (`/changes`, `/edit`) is refused.
- An escaped slash (`%2F`), `.` or `..` as a segment, and an empty segment are
  refused.
- A URL that matches the base but is not a merge request URL is
  `url_malformed`.

## Minimum version

GitLab 16.0 (released 2023-05) or newer is required. The changed files come
from `GET .../diffs`, the only diff source review-mcp uses, and
`detailed_merge_status` and `draft` are always present from that version on.
When an endpoint answers `404` although the merge request itself can be read,
the call fails with the class `unsupported_version` and the hint
`GitLab 16.0 or newer is required`; see
[Troubleshooting](troubleshooting.md#gitlab). A `404` before the merge request
was read is an ordinary `not_found` (wrong URL, or a token that cannot see
it).

## The diff base and a diff that is not ready

The diff is computed against `diff_refs.base_sha` of the merge request, reported
as the base strategy `gitlab:diff_refs` (`diag pr` shows it). The head is
`diff_refs.head_sha`, the commit GitLab computed its diff for; the file
contents, the inline positions and the head the other tools use all belong to
that one revision. `start_sha` is kept for inline positions.

GitLab fills `diff_refs` asynchronously. The diff is **not ready** when
`diff_refs` is empty (right after a merge request is created) or when the
merge request's own head (`sha`) differs from `diff_refs.head_sha` (a push GitLab
has not finished processing). review-mcp then reads the merge request once more
after 2 seconds, inside the call's deadline. If it is still not ready, the call
fails with the class `upstream` and the hint:

```text
the server reported an internal error: the merge request's diff is not ready yet; try again in a moment
```

Run the tool again in a moment. Reading the comments (`pr_comments`) reads the
merge request too, so it can fail the same way for a few seconds after a push.
Posting a comment does not need the diff and does not wait for it.

## Limits of the file and commit lists

- **Files GitLab does not list.** For a very large merge request GitLab stops
  listing changed files and reports a `changes_count` ending in `+` (for
  example `1000+`). The files it left out are not known. The files that were
  listed are reviewed as usual and every tool that reads the diff (`pr_review`,
  `pr_ask`, `pr_describe`, `pr_improve` and `diag pr`) adds the note:
  `GitLab did not list every changed file (its diff limit was reached); the files it left out are not reviewed.`
  The unknown files are not counted in the coverage numbers, so the coverage
  can look complete. Split the merge request, or review the rest by hand.
- **Files past 4000.** review-mcp reads the file list in pages of 100 and stops
  after 40 pages. When GitLab listed more, the note is:
  `review-mcp reads at most 4000 changed files of a merge request; the files GitLab listed after them are not reviewed.`
- **Commits.** The commit messages (`pr_describe`) are read in pages of 100, at
  most 10 pages. A merge request with more commits is read up to the cap
  (the newest 1000); there is no note, only a line in the debug log. GitLab
  lists the newest commit first, review-mcp returns them oldest first.

## Files without a diff

GitLab sends `diff` as an empty string for a binary file, for a file it cut
because it is too large, and for a change with no hunks. review-mcp labels such
a file from what it knows:

| The file | Label |
|---|---|
| a rename without edits (`renamed_file`, equal modes) | listed with an empty patch, which the pipeline reports as `empty_diff` |
| `too_large` or `collapsed` is true | `size_limit` |
| its extension is on the binary-extension list of the file filter (`.png`, `.zip`, ...) | `binary` |
| a `diff` that starts with `Binary files ` and has no hunk | `binary` |
| any other empty diff (a mode change, an empty new file) | listed with an empty patch (`empty_diff`) |
| a `diff` with no hunk header at all, otherwise | `fetch_failed` |

`binary`, `size_limit` and `empty_diff` files stay outside the coverage counts,
as for the other providers. The shape GitLab uses for a binary file is
unconfirmed until the live acceptance (item Q1), which is why both rules exist.

The full contents of both sides (for extended context) are fetched with
`GET .../repository/files/{path}/raw?ref={sha}`, at most
`diff.max_files_full_content` files and `diff.max_file_bytes` bytes each; the
statuses are the Gitea ones (`not_fetched_file_limit`, `not_fetched_size_limit`,
`fetch_failed`). A file keeps its patch without full content.

## Rate limits

GitLab limits the requests of a token. A `429` answer, and a `403` with the
header `RateLimit-Remaining: 0`, is the error class `rate_limited`, reported as:

```text
the server rate-limited the request (HTTP 429): retry after 2026-10-09 12:30:00 UTC
```

The time comes from `Retry-After` (seconds or an HTTP date) or from
`RateLimit-Reset` (epoch seconds), in UTC. When neither gives a usable time (or
the time is more than 24 hours away), the sentence ends after `(HTTP 429)`.

review-mcp waits **at most once**, and only when it is short: when the reset is
60 seconds or less away and the wait ends before the call's deadline, it
sleeps until the reset and repeats the request one time. This holds for writes
too, because a `429` means the request was not processed. The second answer is
final, whatever it is; there is no retry loop. A longer limit fails at once with
the sentence above. A `403` without `RateLimit-Remaining: 0` is not a rate
limit: it is `authentication failed: check the token and its scopes (HTTP 403)`.

The response body is never read for any of this, only the headers.

## Comment threads

`pr_comments` reads the merge request's discussions (all pages, at most 40
pages of 100; a merge request with more is cut at the cap, with a line in the
debug log only):

- **System notes** (`system: true`, such as "added 1 commit") are left out.
- A discussion whose first note has a position (a `DiffNote`) is an **inline
  thread**: the path is the position's new path and the line its new line. A
  note on a removed line has no new line and is shown with line 0.
- Any other discussion is a **general thread**, including an individual note
  (a comment that is not part of a discussion).
- Thread and comment ids are note ids. The discussion id GitLab uses
  internally is never shown.
- **Resolved state is available.** Both resolution flags are true, so
  `include_resolved` works. A discussion is resolved when all of its
  resolvable notes are. An individual general note is not resolvable and is
  shown with a resolved state of `null`, so it is never hidden.
- **Outdated.** A `DiffNote` is outdated when its position's `head_sha`
  differs from the merge request's current `diff_refs.head_sha`. GitLab moves
  the position of a note forward on a new push when it can still trace the
  line; a position left on an older head means it could not. An outdated own
  finding never blocks a new one at the same place
  ([No repeated findings](review.md#no-repeated-findings)). That GitLab moves
  positions forward is not confirmed until the live acceptance (item Q3).
- Comment links are `{web_url}#note_{id}`, where `web_url` is the merge
  request's own `web_url` (see [A proxy or an `external_url` that
  differs](#a-proxy-or-an-external_url-that-differs-from-the-base-url)).

## Replies and edits

- `pr_comment_reply` takes a note id. review-mcp finds the note's discussion in
  the discussions list (so an id that is not on this merge request is
  `not_found`, never touched) and posts into it with
  `POST .../discussions/{id}/notes`. The reply is always **in the thread**
  (`in_thread: true`), for general and inline discussions alike. A reply to an
  individual note makes it a thread; that GitLab does so is unconfirmed until
  the live acceptance (item Q3).
- **Edits** (review-mcp edits its own overview and `pr_describe` comments in
  place): the note is read first (`GET .../notes/{id}`; a system note is
  `not_found`), and it is edited only when its author is the token's user
  (otherwise the error is `not_owner`: "the comment was not written by the
  token's user, so it was not changed"); the check happens before any write.
  That `GET` works for a `DiffNote` is unconfirmed until the live acceptance.
- **New comments.** `pr_comment_create` without a file posts a merge request
  note (`POST .../notes`); with a file and a line it is an inline discussion
  (below).

## Publishing: one discussion per finding

Inline comments of `pr_review` and `pr_improve` (and of `pr_comment_create`
with a file and a line) are posted as **one discussion per comment**:
`POST .../discussions` with the body and a text position. GitLab's batched
review (draft notes and a bulk publish) is not used in rc.6: it would send one
notification instead of several, and it is on the v2.1 backlog. The overview of
`pr_review` and `pr_improve` is a merge request note, edited in place by later
runs.

The position carries `base_sha`, `start_sha` and `head_sha` from `diff_refs`,
`new_path`, `old_path` (the old path for a rename, else the new path) and:

- for an **added** line: `new_line`;
- for a **context** line: `old_line` **and** `new_line`. review-mcp knows the old
  line from the hunk (`InlineComment.OldLine`) and always sends both.
  What GitLab does with a context line that has `new_line` alone (a `400`, or a
  general note) is unconfirmed until the live acceptance (item Q3).

A **range** is anchored on one line: GitLab has no multi-line anchor here
(`line_range` is on the backlog), so a finding or a suggestion that covers
several lines sits on its first line, and `EndLine` is ignored for the anchor,
as on Gitea.

The result of each post:

| GitLab answers | Result |
|---|---|
| `201` with a note of type `DiffNote` | posted; the id and the link are read from the answer |
| `400` whose error names `position` or `line_code` | `unanchorable` (the line cannot be placed on the diff) |
| any other `4xx` or `5xx`, no answer | `failed`; the class's fixed sentence, never the server's text |
| `201` with a note that is not a `DiffNote` | the stray-note guard, below |

The body of a `400` is looked at only to decide whether it is about the
position (the first 4 KiB, a yes or no); nothing of it is kept or logged. After
an authentication, rate-limit or cancel failure the remaining comments get that
error and are not sent. GitLab sends the structured error of a refused position in one of two
shapes (an object, or a string that names the keys); review-mcp recognises
both, and the live acceptance (item Q3) records the real one.

### The stray-note guard

If GitLab cannot place a position, it may create a **general** note instead of
failing. Such a note would look like a finding without a place, and a later run
would not recognise it. So when a `201` answers with a note that is not a
`DiffNote`, review-mcp deletes that note at once (`DELETE .../notes/{id}`) and
reports the comment as `unanchorable`:

```text
GitLab could not place the inline comment on its line; the general comment it created instead was removed
```

The guard deletes only the note just created, and only after checking that its
author is the token's user; it never deletes any other note. When the check or
the delete fails, the comment is `failed`, and the general note is still
there:

```text
an inline comment GitLab could not place was posted as a general comment and could not be removed
```

Delete that note by hand. Whether a project or group access token may delete
its own note is unconfirmed until the live acceptance (item Q3).

Running the tool again does not repeat a comment: the checks described in
[Reviewing pull requests](review.md#no-repeated-findings) (by place, then by
fingerprint) and [Suggesting code changes](improve.md#no-repeated-suggestions)
work as on the other providers.

### Native suggestion blocks

`pr_improve` posts a native **suggestion block** on GitLab, so a person can
apply the change with **Apply suggestion**. GitLab writes the number of lines a
block replaces in the fence, below the line the comment sits on:

````text
**Use the constant**

Label: possible issue · Score: 9 of 10

The literal repeats the constant defined above.

```suggestion:-0+1
    first := timeoutSeconds
    second := timeoutSeconds
```
````

`suggestion:-0+N` replaces the comment's line and the N lines below it (a
block of one line is a plain `suggestion`). The rest of the rules are those of
[GitHub's blocks](github.md#native-suggestion-blocks) and
[Suggesting code changes](improve.md#inline-comments): the suggestion must be
verified, its whole range must be on new-side lines of one hunk, and the
improved code must be re-indented onto the real lines safely; otherwise the
comment shows the fenced `diff` block. Whether `suggestion:-0+N` applies
cleanly is unconfirmed until the live acceptance (item Q4).

## Quick-action safety

GitLab runs a line that starts with `/` in a comment or a description as a
**quick action** (`/close`, `/label ~x`, `/merge`). The text review-mcp
publishes includes the model's words, code from the diff and, for `pr_ask`,
your question, so none of it may be able to run a command. This is the rule
(decision X-35); it does not depend on how GitLab treats code blocks.

**What is sanitised.** On GitLab (a provider with the quick-action
capability) every published body, whatever the tool, goes through the same
sanitiser: a space is put in front of every line that starts with `/` at
column 0, including the first line and the lines after a carriage return. The
comment shows ` /close` instead of running anything. This covers the
overviews, the inline comments, `pr_comment_create`, `pr_comment_reply`,
`pr_ask` and `pr_describe`. The text returned to your client is not changed.

**The suggestion-block refinement.** Inside a suggestion block the sanitiser
cannot add a space to a code line, because the block is applied as it is. So
inside a fence whose info string is `suggestion` or `suggestion:-N+M` (three
or more backticks or tildes, up to three spaces of indent, closed by the same
character at least as long), a line gets the space only when it could be a
quick action: `/` followed by a letter, a digit or an underscore. A line such
as `// Retry the call.` or `/* ... */` is left exactly as it is, so a Go doc
comment stays intact. Outside such a fence nothing changes: every line that
starts with `/` gets the space. The refinement only ever spares a line that
cannot be a command, so it is safe whatever the fence parsing does with an
unclosed or hand-written block.

**The diff fallback.** When the suggested code of a `pr_improve` suggestion has
a line that could be a quick action (sanitising it would change the code the
block applies), review-mcp does not post a native block. The suggestion is shown
as the fenced `diff` block instead, and the comment carries this paragraph above
its marker:

```text
Note: a line of the suggested code starts with a slash command, so it is shown as a diff.
```

The diff block is a reading aid: copy the change by hand. This keeps the
suggestion exact and never publishes a possible command.

**The description refusal.** `pr_describe` with `publish_mode=description`
writes inside the merge request description, and GitLab runs quick actions in a
description when it is updated. review-mcp must not change the author's text, so
it cannot sanitise it. Therefore, when the text **outside** review-mcp's marked
region has a line that could run a quick action (`/` followed by a letter, a
digit or an underscore, after up to three spaces), description mode is refused
before any write, and `publish.error` is:

```text
the description has a line GitLab could run as a quick action; publish in comment mode instead
```

The check runs on the description as it is right before the write (after the
re-read), and it also refuses a run that would only change the title, or write
nothing because the region is up to date. review-mcp's own region is
sanitised as usual. Use `publish_mode=comment`, or remove the line from the
description. The refusal applies only to GitLab; Gitea, Bitbucket Server and
GitHub, and comment mode, are unaffected.

**The column-0 assumption.** The sanitiser, like the one on every provider,
looks at column 0 only. The whole design therefore rests on GitLab running a
quick action only when the line starts with `/` at column 0 (the description
refusal is stricter: it allows for up to three spaces, because the author's
text cannot be changed and a refusal costs nothing). The live check **Q5b**
verifies the assumption: a note whose lines are ` /label ~probe` (one leading
space) and `    /label ~probe` (four spaces) must not add the label. If GitLab
runs either, the sanitising rule must change first, and rc.6 is not promoted
to 2.0.0 before it does.

## `pr_info`

`pr_info` ([Pull request status](pr-info.md#gitlab)) reads the merge request
again and adds:

- **Reviewers.** `GET .../reviewers` lists the requested reviewers; every one
  is `requested: true`. The state maps as:

  | GitLab `state` | `reviewers[].state` |
  |---|---|
  | `approved` | `approved` |
  | `requested_changes` | `changes_requested` |
  | `reviewed` | `commented` |
  | `unreviewed`, `review_started`, `unapproved`, anything else | `pending` |

  The users in `approved_by` of `GET .../approvals` are **authoritative**: a
  requested reviewer who is listed there is reported **approved**, whatever
  `/reviewers` says, except a reviewer whose state is `requested_changes`,
  who keeps `changes_requested`. (Older 16.x servers report only `unreviewed`
  and `reviewed` in `/reviewers`, so the list of approvers is the reliable
  source.) An approver who is not a reviewer is added as approved with
  `requested: false`. `stale` is always false, because GitLab reports no
  commit per approval. If `/approvals` cannot be read, the reviewers from
  `/reviewers` are still listed, without the approvers who are not reviewers;
  `required_approvals` then carries the note "not readable with this token".
  A `404` on `/reviewers` (an instance without the endpoint) falls back to the
  merge request's own `reviewers` list, all `pending`; any other failure is
  `reviewers: null` with the note `The reviews could not be read, so the reviewers are not listed.`
  Approvals carry no body and review-mcp's own reviews are never approvals, so
  there is no review of its own to leave out; its comments are counted under `review_mcp_activity`
  as on every provider.
- **Required approvals.** `approvals_required` of `GET .../approvals`. The value
  `0` is reported as `0` with the note `no approval rule applies to this merge request`;
  an absent or unreadable value is `null` with `not readable with this token`.
  What `approvals_required` is on the Free tier (0 or absent) is unconfirmed
  until the live acceptance (item Q2). Required approvals above 0 are a Premium
  feature and cannot be checked on the Free tier.
- **Merge status.** `mergeable` and `merge_blockers` come from the merge
  request's `detailed_merge_status`. The value itself is never shown, only the
  fixed texts:

  | `detailed_merge_status` | `mergeable` | Blocker |
  |---|---|---|
  | `mergeable` | `true` | none |
  | `unchecked`, `checking`, `approvals_syncing`, `preparing` | `null` (not known yet) | none |
  | `conflict`, `broken_status` | `false` | `merge conflict` |
  | `ci_must_pass`, `ci_still_running`, `external_status_checks`, `security_policy_pipeline_check` | `false` | `required builds are missing or failing` |
  | `discussions_not_resolved` | `false` | `unresolved discussions must be resolved` |
  | `draft_status` | `false` | `the pull request is a draft` |
  | `need_rebase` | `false` | `the branch is behind the target branch` |
  | `not_approved` | `false` | `required approvals missing` |
  | `requested_changes` | `false` | `a reviewer marked the pull request as needs work` |
  | anything else (`not_open`, `blocked_status`, `merge_request_blocked`, `jira_association_missing`, `title_regex`, `merge_time`, `locked_paths`, an unknown value) | `false` | `other merge check` |

  A merge request that is merged or closed gets no merge state (`mergeable`
  `null`, no blockers). An open one with no `detailed_merge_status` is `null`
  with the note `The merge status could not be read.`

  `detailed_merge_status` holds a single value, so a merge request that is
  blocked for two reasons shows one blocker. The thread blocker
  (`unresolved discussions must be resolved`) appears only when the project
  requires all threads to be resolved ("All threads must be resolved").

## CI results

`ci` is read from the **commit statuses of the head commit** in the merge
request's project: `GET .../repository/commits/{sha}/statuses`, followed
through the `Link` header (at most 10 pages of 100, and at most 500 items).
GitLab's default answer holds the latest status of each name. The base strategy
of the CI results is `gitlab:commit_statuses`.

| GitLab `status` | State |
|---|---|
| `success` | success |
| `failed` | failure; neutral when `allow_failure` is true |
| `canceled` | error |
| `skipped` | skipped |
| `manual` | neutral when `allow_failure` is true, else pending (the pipeline waits for it) |
| `created`, `waiting_for_resource`, `preparing`, `pending`, `running`, `scheduled` | pending |
| anything else | neutral |

The name is the status `name` and the link its `target_url`, with the usual
URL cleaning. No check is marked required. Everything else about `ci` is as in
[CI results](pr-info.md#ci-results). Whether `/statuses` answers with the latest
status per name and matches the pipeline page is unconfirmed until the live
acceptance (item Q6).

**What is not read.** The pipelines of **merged results** and **merge trains**
(Premium) and pipelines that ran in a **fork's project** are not read in rc.6.
A merge request whose only CI is such a pipeline shows `ci` as `none` ("no
checks reported") although a pipeline exists; look at the pipeline page. They
are on the backlog.

## The description edit

`pr_describe` with `publish_mode=description` (and `update_title`) sends
`PUT .../merge_requests/{iid}` with only the `title` and/or the `description`;
the draft state, the reviewers, the labels and the target branch are never part
of the request. GitLab has no version to send, so the safety is the re-read rule
that Gitea uses: the merge request is read again right before the write, a
description that changed since the first read is merged once, and a second
change refuses with "The pull request description changed while it was being
updated; nothing was written."
([Describing pull requests](describe.md#description-mode)). The quick-action
refusal above is checked on every re-read.

**The draft state follows the title.** GitLab derives `draft` from a title
prefix. review-mcp knows `Draft:`, `[Draft]` and `(Draft)` (and `WIP:`,
`[WIP]`), in any case: it removes such a prefix from a generated title, and
puts the merge request's own prefix, exactly as written, in front of the
generated title when GitLab reports the merge request as a draft. Replacing the
title therefore keeps the draft state. See
[Describing pull requests](describe.md#description-mode).

## Repository context

With `context.repo.enabled=true` ([Repository context](repo-context.md)),
review-mcp fetches the merge request head into its cache:

- the clone address is `{gitlab.base_url}/{full path}.git` (the **web** base,
  with its context path and every group of the path; never the API base);
- the ref is `refs/merge-requests/{iid}/head`;
- the authentication is HTTP **Basic** with the fixed user `oauth2` and the
  token as the password, sent through `http.extraHeader` in the environment of
  the one `git` process (never as an argument, never on disk). This is the only
  scheme: there is no second attempt and no lookup of the token's identity.
  Whether both a personal and a project access token work this way is
  unconfirmed until the live acceptance (item Q-repo, below).

The token needs `read_repository` (a personal access token) or the Reporter
role (a project or group access token).

## A proxy or an `external_url` that differs from the base URL

review-mcp calls `{gitlab.base_url}/api/v4` and follows the `Link` header for
paging, but only a next link that stays on the configured API base. A
self-managed GitLab whose `external_url` differs from `gitlab.base_url` (a
different scheme or host behind a reverse proxy, a context path the proxy
strips) sends `Link` headers and `web_url` values that point at its own idea of
its address. Two things follow:

- **Paging fails.** A list that needs a second page (the changed files, the
  discussions, the commit statuses) fails with
  `the server sent an unexpected response: a link in the response points outside the configured API base`.
  The same refusal protects the token from being sent to another host. Make
  `gitlab.base_url` equal to the address GitLab puts in its links (its
  `external_url`), or set the instance's `external_url` to the address review-mcp
  is configured with.
- **Comment links use `web_url`.** The links review-mcp builds to a comment
  (`{web_url}#note_{id}`, shown in `pr_comments`, `diag comment` and the
  "already posted" links) start with the merge request's own `web_url`, as
  GitLab reports it, for every tool alike. Behind such a proxy they can differ
  from the URL you typed. Only the web base is used to recognise merge request
  URLs and to build links to lines (`FileLineURL`).

## Known gaps

- **Individual notes.** A comment that is not part of a discussion cannot be
  resolved and is shown with `resolved: null`.
- **Binary files.** The shape of a binary file in `GET .../diffs` is
  unconfirmed (see [above](#files-without-a-diff)).

### Still to be checked at live acceptance

The acceptance runs after rc.6 is published, by the owner, on gitlab.com in a
project under `group/subgroup/`. Nothing is published to a merge request
without the owner's go-ahead. The full checklist is in the
[CHANGELOG](../CHANGELOG.md) entry of rc.6. The items:

| Item | What is checked |
|---|---|
| Q1 | `diag pr` and `diag comments` on a merge request with a rename, a deletion and a new file in a nested directory: files and threads match the UI, the base strategy is `gitlab:diff_refs`. The binary-file shape, the `Accept: */*` raw request and the head moving with a push. |
| Q2 | `pr_info` on an open and a merged merge request: reviewers (their states on the server, and an approver's state), `approvals_required` on the Free tier, the blocker for a draft and for an unresolved thread. |
| Q3 | `pr_review` with `publish=true` on a throwaway merge request: inline discussions on an added and a context line; a second run posts nothing new; after a push that moves the commented line away the old note is outdated. The `400` body for a bad position, the stray-note guard, `DELETE` of an own note with a project token, `position.head_sha` moving forward, a reply turning an individual note into a thread, `GET .../notes/{id}` for a `DiffNote`, the posted-note URL. |
| Q4 | `pr_improve` with `publish=true`: a native suggestion applies cleanly with **Apply suggestion**; a suggestion whose code starts with `//` stays native; the comments contain no executed quick action. |
| Q5 | `pr_describe` in description mode: the author's text is unchanged; a description line `/label ~probe` makes the call refuse. |
| Q5b | The column-0 assumption (above). |
| Q6 | CI: one passing job, one failing job and one `allow_failure` job; the overview CI line and `pr_info.ci` match the pipeline page. |
| Q7 | The guidelines file at the base revision and the file tree, as on GitHub; a file path given to the tree endpoint. |
| Q-repo | The repository context fetches `refs/merge-requests/{iid}/head` with the Basic user `oauth2`, with a personal access token and with a project access token. |

Not verifiable on gitlab.com Free: required approvals above 0 (Premium),
merged-results and merge-train pipelines. They are recorded as not verified.

## What is not supported

- **GraphQL.** Anything that needs it is not available.
- **Resolving or unresolving a discussion.**
- **Requesting changes on a merge request.** GitLab's REST API has no request
  for it. Approving a merge request, and removing your own approval, are
  supported by [`pr_review_decision`](decision.md), which is off by default.
  review-mcp's reviews and comments never approve.
- **Batched reviews** (draft notes with a bulk publish): backlog.
- **Multi-line anchors** (`line_range`): backlog. A range is anchored on its
  first line.
- **Merged-results, merge-train and fork pipelines** in the CI results:
  backlog.
- **GitLab before 16.0.** The `/changes` fallback is not implemented.
- **Webhooks and a bot mode.** review-mcp acts only when a tool is called.
- **Merging, labels, assignees** or any write beyond comments, discussions,
  suggestions and the title and description.

See also: [Setup guide](setup.md#gitlab), [Pull request status](pr-info.md),
[Repository context](repo-context.md), [Serve mode](serve.md) and
[Troubleshooting](troubleshooting.md#gitlab).
