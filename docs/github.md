# GitHub

review-mcp reads and writes pull requests on GitHub (github.com) and on GitHub
Enterprise Server (GHES) through the REST API v3 and a personal access token.
Every tool works: `pr_review`, `pr_ask`, `pr_describe`, `pr_improve`,
`pr_comments`, `pr_comment_reply`, `pr_comment_create` and `pr_info`. GraphQL
is not used (see [What is not supported](#what-is-not-supported)).

This page is the reference for what is specific to GitHub. Installation and
the LLM endpoint are in the [Setup guide](setup.md); the fixed sentences you
may see are collected in [Troubleshooting](troubleshooting.md#github).

> **Checked live on github.com, not on GitHub Enterprise Server.** The provider
> is tested against fake servers (and a real `git` for the repository
> context). The rc.3 acceptance ran the tools against a throwaway pull request
> on github.com (items Q1 to Q5, [below](#still-to-be-checked-at-live-acceptance)):
> a review without a body is accepted, and a native suggestion block applies
> cleanly with **Commit suggestion**. What has not run live is GitHub Enterprise
> Server, a renamed or transferred repository and a real rate limit; each place
> where that matters says so.

## Configuration

| Key | Env | Default | Notes |
|---|---|---|---|
| `github.base_url` | `REVIEW_MCP_GITHUB_BASE_URL` | none | The web base that pull request URLs start with. **No default**: GitHub is enabled only when this is set (no host is ever assumed). |
| `github.api_url` | `REVIEW_MCP_GITHUB_API_URL` | derived | The API base. Optional; see [below](#the-api-base-is-derived). Setting it without `github.base_url` is a configuration error. |
| (secret) | `REVIEW_MCP_GITHUB_TOKEN` | none | A personal access token. Required when `github.base_url` is set (stdio). Secrets are never in the TOML file. |
| `github.ca_cert` | `REVIEW_MCP_GITHUB_CA_CERT` | none | A PEM bundle for a private CA, used in addition to the system roots. |
| `github.insecure_skip_verify` | `REVIEW_MCP_GITHUB_INSECURE_SKIP_VERIFY` | `false` | Turns TLS verification off; review-mcp warns at startup. Prefer `ca_cert`. |
| `github.self_login` | `REVIEW_MCP_GITHUB_SELF_LOGIN` | none | The login review-mcp treats as its own: the author of its overview, its held and carried records and its comments in the discussion, and the author check before an edit. When it is set, `GET /user` is not called. Leave it unset for a personal access token. Set it for a token that cannot read `GET /user`, such as another GitHub App's token (`<app-slug>[bot]`); `ci` on GitHub Actions sets `github-actions[bot]` for the job token by itself (see [CI mode](ci.md#the-jobs-github_token-and-review-mcps-own-login)). 1 to 100 characters of `A-Z a-z 0-9 - [ ]`; setting it without `github.base_url` is a configuration error. |

In `serve` mode the token comes from the request header
`X-Review-MCP-GitHub-Token`, and `REVIEW_MCP_GITHUB_TOKEN` must be unset (see
[Serve mode](serve.md#the-header-contract)). The token is sent as
`Authorization: Bearer`, never logged and never stored. Every request also
sends `Accept: application/vnd.github+json` (raw file contents use
`application/vnd.github.raw`), `X-GitHub-Api-Version: 2022-11-28` and a
`User-Agent` of `review-mcp/<version>`.

`server_info` lists the enabled provider with its base URL, its API URL, and
the token as set or unset.

### The API base is derived

GitHub's API does not live at the address of its web pages, so review-mcp
works out the API base from `github.base_url` unless you set `github.api_url`:

| `github.base_url` | API base used |
|---|---|
| exactly `https://github.com` (any letter case, no port, no path) | `https://api.github.com` |
| anything else, for example `https://ghe.example.com` | `{base_url}/api/v3` (GitHub Enterprise Server) |
| anything else, with a context path such as `https://ghe.example.com/github` | `https://ghe.example.com/github/api/v3` |

Set `github.api_url` when the API is somewhere else, for example a hosted
enterprise whose API has its own host (`https://api.github.example.com`). A
trailing slash is ignored. The API base is used for every API request; the web
base is used to recognise pull request URLs, to build links to lines, and as
the clone address for the [repository context](#repository-context). An API
request never goes to the web base, and a redirect that leaves the API base is
refused.

## Token

Create a personal access token under **Settings, Developer settings, Personal
access tokens** on your GitHub (`https://github.com/settings/tokens` on the
public product). review-mcp acts as the token's user: it never uses an app or
a service account of its own.

| Token type | What to grant |
|---|---|
| **Fine-grained** (recommended) | Resource owner: the user or organisation that owns the repository. Repository access: only the repositories you review. Repository permissions: **Pull requests: Read and write**, **Contents: Read**, **Commit statuses: Read**, **Checks: Read**, **Metadata: Read** (selected automatically). |
| **Classic** | `repo` for private repositories, or `public_repo` for public ones only. |

Read-only use needs only the read side: with a fine-grained token, **Pull
requests: Read-only**. Write access (**Pull requests: Read and write**, or the
classic scope) is needed only for `publish=true`, `pr_comment_reply` and
`pr_comment_create`.

| Use | Needs |
|---|---|
| `pr_review`, `pr_ask`, `pr_describe`, `pr_improve` with `publish=false`, `pr_comments`, `pr_info` | Pull requests: read; Contents: read (file contents, the merge base and the repository context); Metadata: read |
| The CI results of the head commit: `pr_info`, and `pr_review` unless `review.ci_status` is false | Commit statuses: read and Checks: read (fine-grained). Both are needed: the check runs need Checks, the legacy statuses need Commit statuses. With only one of them the other list is still read and `ci` carries the note "Only part of the CI results could be read with this token (check runs or commit statuses)."; with neither, `ci` is `unreadable` with a note. A classic token's `repo` scope covers them. Everything else keeps working without them |
| The guidelines file and the file tree of `pr_review` and `pr_improve` | Contents: read (they are read at the target branch) |
| `publish=true`, `pr_comment_reply`, `pr_comment_create` | the same, with Pull requests: write |
| `pr_describe` with `publish_mode=description` (and `update_title`) | Pull requests: write (it edits the pull request itself) |
| `pr_info` required approvals from classic branch protection | Administration: read (fine-grained) or admin rights (classic). Optional: without it the count falls back to the rulesets, with a note (see [`pr_info`](#pr_info)) |

Two things on GitHub's side can look like a token problem:

- An organisation that uses SAML single sign-on needs the token authorised for
  that organisation (the token's page has a **Configure SSO** button).
- A fine-grained token sees only the repositories it was created for, and only
  the ones of its resource owner. A repository it cannot see answers `404`.

Every scope in this table is derived from the API calls the code makes and is
unconfirmed until the live acceptance has used a token with exactly these
permissions.

## Pull request URLs

A pull request URL is `{github.base_url}/{owner}/{repo}/pull/{number}`:

```text
https://github.com/{owner}/{repo}/pull/{n}
https://ghe.example.com/octo/demo/pull/7
https://ghe.example.com/github/octo/demo/pull/7/files
```

- Scheme, host, port and path prefix must match the configured base URL
  exactly (the host is case-insensitive; default ports are implied). A URL on
  `github.com` is **not** resolved unless `github.base_url` is configured.
- The owner is exactly one segment (GitHub has no nested owners). An escaped
  slash (`%2F`) in the owner or the repository, and `.` or `..` as a name, are
  refused.
- Segments after the number (`/files`, `/commits`) are accepted, and a
  `#fragment` is ignored. A URL that matches the base but is not a
  pull request URL is `url_malformed`.
- The API address (`github.api_url`, or the derived one) is never matched as a
  web base: a pull request URL on `api.github.com` is not a pull request URL.

## Rate limits

GitHub limits the requests a token can make. A `403` or `429` answer that says
so (`X-RateLimit-Remaining: 0`, or a `Retry-After` header for the secondary
limit) is the error class `rate_limited`, reported as:

```text
the server rate-limited the request (HTTP 403): retry after 2026-10-09 12:30:00 UTC
```

The time comes from `Retry-After` (seconds or an HTTP date) or from
`X-RateLimit-Reset`, in UTC. When neither gives a usable time (or the time is
more than 24 hours away), the sentence ends after `(HTTP 403)`.

review-mcp waits **at most once** and only when it is short: when the reset is
60 seconds or less away and the wait ends before the call's deadline, it
sleeps until the reset and repeats the request one time. The second answer is
final, whatever it is; there is no retry loop. A longer limit fails at once with
the sentence above. A `403` without a rate-limit signal is not a rate limit: it
is `authentication failed: check the token and its scopes (HTTP 403)`.

The response body is never read for any of this, only the headers.

## Limits of the file and commit lists

GitHub lists at most 3000 files and 250 commits of a pull request, however
large it is.

- **Files past 3000.** The files that were listed are reviewed as usual. The
  others have no path to name, so they are reported in a note instead of as
  skipped files, on every tool that reads the diff (`pr_review`, `pr_ask`,
  `pr_describe`, `pr_improve` and `diag pr`):
  `GitHub lists at most 3000 files of a pull request: N more changed files were not listed, so they are not reviewed (file_limit).`
  They are not counted in the coverage numbers. Split the pull request, or review the
  rest by hand.
- **Commits past 250.** The commit messages stop at 250 (oldest first). `pr_info` says so with
  the note `GitHub lists at most 250 commits of a pull request; the commits past them are not available.`
  `pr_describe` reads the same list, and when GitHub counts more commits than
  it returned it adds its own note (N is the commit count, M the number of
  messages read): `The pull request has N commits, but only M commit messages could be read; the description used those.`

## Files without a patch

GitHub sends no patch for a binary file or for a diff that is too large to
show. review-mcp labels such a file from what it knows:

| The file | Label |
|---|---|
| its extension is on the binary-extension list of the file filter (`.png`, `.zip`, ...) | `binary` |
| no patch and no line changes otherwise: an empty new file, a pure rename, a mode change | listed with an empty patch, which the pipeline reports as `empty_diff` |
| no patch, but GitHub counts line changes: the diff is too large to show | `size_limit` |
| a patch with no hunk header at all | `fetch_failed` |

`binary` and `empty_diff` files stay outside the coverage counts, as for the
other providers. GitHub sends neither a patch nor line counts for a binary
file, so a binary file with an extension that is not on the list (for example
`.dat`) is reported as `empty_diff`, not `binary`. Both are skipped.

## Comment threads

`pr_comments` lists:

- **general threads:** the pull request's issue comments, and the reviews
  that have a body (a review submitted with a summary), shown under the
  reviewer's name. Pending reviews are never shown;
- **inline threads:** review comments, grouped by the comment they reply to,
  the root first, replies in order. A comment whose line no longer exists in
  the current diff (the line moved or the code changed) falls back to its
  original line and the thread is marked outdated. A comment on the base side
  of the diff has line 0 (no new-side line).

All lists are followed through GitHub's `Link` header; only a next link on the
configured API base is followed.

### Resolved state is not shown

GitHub exposes whether a review thread is resolved only through GraphQL, which
review-mcp does not use. So neither resolution flag is set for GitHub: every
thread is shown, `include_resolved` changes nothing, and the result carries the
note `Resolved state is not available on GitHub without GraphQL; all threads are shown.`

## Replies and edits

Comment ids stay plain numbers, but GitHub numbers three things separately:
issue comments, review comments and review bodies. So one id can name up to
three different things on the same pull request.

- `pr_comment_reply` takes the id alone, without a kind; review-mcp looks the
  id up among this pull request's issue comments, review comments
  and review bodies. A comment that does not belong to this repository and
  pull request is `not_found`, never touched.
- **An id found in more than one place is refused before anything is
  written:** `the server sent an unexpected response: the comment id is ambiguous on GitHub`.
  See [Troubleshooting](troubleshooting.md#github) for what to do.
- **A reply to an inline comment** goes into the thread
  (`in_thread: true`). GitHub's replies endpoint takes the first comment of a
  thread, so the id of a reply is replaced by its root's.
- **A reply to a general comment or to a review body** has no thread on GitHub,
  so it is a new pull request comment that starts with the quote line
  `> Replying to @author` (`in_thread: false`), as on Gitea.
- **Edits** (review-mcp edits its own overview and `pr_describe` comments in
  place): the comment is read first, and it is edited only when its author is
  the token's user (otherwise the error is `not_owner`:
  "the comment was not written by the token's user, so it was not changed"); the check happens before
  any write. A review body is not a comment and is not editable (`not_found`).

## Publishing: one review per run

Inline comments of `pr_review` and `pr_improve` (and of `pr_comment_create`
with a file and a line) are posted as **one review per run**:
`POST /pulls/{n}/reviews` with the event `COMMENT`, pinned to the head
commit, one entry per comment (path, line and side `RIGHT`; `start_line` and
`start_side` for a range). GitHub creates the review and all of its comments,
or nothing.

- **The review has no body.** The review only carries its comments, and a
  review with a body would show up as a general entry in `pr_comments`. GitHub's
  documentation says a body is required for the event `COMMENT`, but in the
  rc.3 acceptance on github.com a review that carries comments and no body was
  accepted, and `line` and `start_line` came back from
  `GET /pulls/{n}/reviews/{id}/comments`. Not checked on GitHub Enterprise
  Server. If a server refuses it, the fallback below posts every comment alone,
  so nothing is lost; the decision for that case would be a fixed one-line body
  that ends with a review-mcp marker line.
- **Ids and links** of the posted comments are read back from
  `GET /pulls/{n}/reviews/{id}/comments`. If that read fails, the comments are
  still `posted` and carry the review's link.
- **A 422 for the whole review** is GitHub's answer when any comment cannot be
  placed on the diff (and then it creates nothing). review-mcp then posts each
  comment alone (`POST /pulls/{n}/comments`) to find the ones GitHub refuses; a
  refused one is `unanchorable`, the others are posted. After an authentication
  or rate-limit failure the remaining comments get that error and are not sent.
  The reason is a 422 for the position, but it can also mean a pending review of
  the same user or a stale head commit; each such comment is then reported
  `unanchorable` too (live acceptance may show it).
- **Any other failure of the review** (authentication, rate limit, `404`,
  `5xx`, no answer, an unreadable answer) marks every comment `failed` and
  sends nothing again, because only a 422 proves that nothing was created. A
  `5xx` can arrive after GitHub has written the review, so it is never retried
  comment by comment.
- For `n` comments a successful run costs one `POST` and about `n / 100`
  `GET`s; after a 422 it costs the first `POST` plus at most `n` more.

Running the tool again does not repeat a comment: the checks described in
[Reviewing pull requests](review.md#no-repeated-findings) (by place, then by
fingerprint) and [Suggesting code changes](improve.md#no-repeated-suggestions)
work as on the other providers. GitHub reports the first line of a range
comment (`start_line`) as well as its last, so a later finding anywhere inside
an earlier range is recognised; a skipped one links to the earlier comment.
The reviews appear in the pull request's **Files changed** view as ordinary
reviews of the token's user. They are neither approvals nor change requests,
and `pr_info` leaves them out of the reviewers (see below).

### Ranges

A finding or a suggestion that covers several lines is posted on its whole
range when every line of the range is a new-side line of one hunk: GitHub shows
the comment against `start_line` to `line`. A range that leaves the hunk
is anchored on its first line only (`pr_review`) or listed in the overview only
(`pr_improve`).

### Native suggestion blocks

`pr_improve` posts a native **suggestion block** on GitHub, so a person can
apply the change with **Commit suggestion**:

````text
**Use the constant**

Label: possible issue · Score: 9 of 10

The literal repeats the constant defined above.

```suggestion
    return timeoutSeconds
```
````

The block replaces exactly the lines the comment covers (`start_line` to
`line`), and a comment is posted this way only when all of these hold:

- the suggestion is verified against the head file (or through the patch);
- its whole range is on new-side lines of one hunk;
- the improved code can be re-indented onto the real lines safely.

**The re-indentation rule.** The model quotes `existing_code` and is free to
drop its indentation. A block replaces the real lines when applied, so the
improved code must carry the real indentation. review-mcp compares the real
lines (the head file's, or the patch's new-side lines when the file was not
fetched in full) with the quote after the usual normalisation; they must be
equal. Then:

- Let R be the longest run of leading spaces and tabs that all non-blank real
  lines share, and E the same for the quote, compared byte for byte.
- R equals E: the improved code is used as it is.
- R starts with E and is longer (the model dedented its quote): the missing
  part is put in front of every non-blank line of the improved code; blank and
  whitespace-only lines stay as they are.
- Anything else (the model added indentation, tabs against spaces, white space
  removed from the middle of R): **no suggestion block**. The comment shows the
  fenced `diff` block of Gitea and Bitbucket Server instead, which is only a
  reading aid. A wrongly indented block is never posted.

Only the frame of the quote is corrected; indentation inside the improved code
stays as the model wrote it. The suggestion's own `improved_code` in the tool
result is never changed. Whether a block applies cleanly with GitHub's button
is checked at live acceptance (item Q3).

## `pr_info`

`pr_info` ([Pull request status](pr-info.md)) reads the pull request again and
adds:

- **Reviewers.** The requested reviewers (`requested_reviewers`) and the
  reviews, folded per user: the latest `APPROVED` or `CHANGES_REQUESTED`
  decides; `DISMISSED` and `PENDING` reviews never count (GitHub turns a
  dismissed review itself into a `DISMISSED` one); `COMMENTED` counts only when the user has no decisive
  review. A review is `stale` when its commit is not the head. review-mcp's own
  reviews (a `COMMENTED` review of the token's user with a review-mcp marker in
  its body or in one of its comments) are not reviewers; they are counted under
  `review_mcp_activity`.
- **Teams.** A requested team is listed as `@{owner}/{slug}` with the team's
  name as display name, `requested: true` and state `pending`. No user login
  can look like that, so a team is never confused with a person.
- **Required approvals**, from two sources that are both read:

  | Rulesets (`GET .../rules/branches/{branch}`, read access) | Classic branch protection (`GET .../branches/{branch}/protection`, admin) | `required_approvals` | Note |
  |---|---|---|---|
  | a count R (the largest over all matching `pull_request` rules) | a count C | the larger of R and C | none |
  | a count R | not readable | R | `classic branch protection is not readable with this token; the required count may be higher` |
  | none, or not readable | a count C (0 when the protection has no review requirement) | C | none |
  | none, or not readable | not readable | `null` | `not readable with this token` |

  GitHub answers an unprotected branch and a token without admin rights alike
  with `404` or `403` on the classic endpoint, so "no rule" cannot be told from
  "cannot read", and `0` is reported only when a count was really read.
- **Merge status.** `mergeable` is the pull request's own verdict (`null` while
  GitHub has not computed it, and for a pull request that is not open).
  `merge_blockers` come from its `mergeable_state`:

  | `mergeable_state` | Result |
  |---|---|
  | `clean`, `has_hooks` | no blocker |
  | `unknown`, empty | no blocker (not computed yet; `mergeable` is `null`) |
  | `dirty` | `merge conflict` |
  | `blocked` | `branch rules are not satisfied (for example required reviews, required checks or other restrictions)` |
  | `behind` | `the branch is behind the target branch` |
  | `draft` | `the pull request is a draft` |
  | `unstable` | **no blocker**; the note `some checks that are not required are failing or pending` |
  | any other value | `other merge check` |

  `unstable` is GitHub's "mergeable with non-passing commit status": only
  checks that are *not required* fail or are pending. Failing required checks
  give `blocked`. The state itself is never shown, only the fixed texts.
- **Commits.** A pull request with more than 250 commits gets the commits note
  above.
- **CI results.** `ci` is read from the check runs
  (`GET .../commits/{sha}/check-runs`) and the combined commit status
  (`GET .../commits/{sha}/status`), both followed through the `Link` header (at
  most 10 pages of 100 each, so at most 500 per list) and merged by name; a
  check run wins over a status of the same name. A run that is queued,
  in progress, waiting or requested is `pending`; a completed run is its
  `conclusion` (`cancelled` is `error`, `timed_out` is `failure`,
  `action_required` is `pending`, `stale` and unknown values are `neutral`).
  The permissions are Checks read and Commit statuses read (see
  [Token](#token)). When exactly one of the two lists fails with `auth` or
  `not_found` (a token with only one of the two permissions), the other list
  is used: `ci` keeps the state of the checks that were read and gets the note
  "Only part of the CI results could be read with this token (check runs or
  commit statuses).", and the overview line ends with "; only part of the CI
  results could be read with this token". Both lists failing, or either
  failing with any other class, is `unreadable` with a note.

## The description edit

`pr_describe` with `publish_mode=description` (and `update_title`) sends
`PATCH /pulls/{n}` with only the `title` and/or the `body`; the draft flag, the
base branch and the reviewers are never part of the request. GitHub has no
version to send, so the safety is the re-read rule that Gitea uses: the pull
request is read again right before the write, a description that changed since
the first read is merged once, and a second change refuses with "The pull
request description changed while it was being updated; nothing was written."
([Describing pull requests](describe.md#description-mode)).

On GitHub a draft is a flag, not a title prefix, so replacing the title cannot
change the draft state and the work-in-progress prefix rule does nothing in
practice.

## Repository context

With `context.repo.enabled=true` ([Repository context](repo-context.md)),
review-mcp fetches the pull request head into its cache:

- the clone address is `{github.base_url}/{owner}/{repo}.git` (the **web**
  base, with its context path on GHES; never the API base);
- the ref is `refs/pull/{n}/head`;
- the first authentication scheme is HTTP **Basic** with the user
  `x-access-token` and the token as the password, sent through
  `http.extraHeader` in the environment of the one `git` process (never as an
  argument, never on disk). GitHub does not accept a bare `Bearer` token for
  git over HTTPS. After an HTTP 401 `Bearer` is tried once, which matters only
  for a GHES behind a proxy. The scheme that worked is remembered for the
  process.

## Known gaps

- A repository that was renamed or transferred: GitHub redirects the old URL,
  but its API returns the new names, and a reply or an edit compares the
  comment's repository with the one in your URL. A reply through an old-name URL
  may therefore be `not_found`. Use the current URL. If live acceptance shows it,
  the comparison will use the repository name from the pull request's metadata
  instead.
- GitHub Enterprise Server has not run yet, and nothing here has met a real
  rate limit (it is covered by tests with fake servers); see the checks below.
- A quote that uses spaces where the file uses tabs does not verify: a
  `pr_improve` suggestion whose `existing_code` indents a loop with spaces
  while the file uses tabs is `not_found` and is listed in the overview only,
  never inline. A quote that only drops the indentation is fine (the block is
  re-indented onto the real lines). Matching tabs and spaces loosely is on the
  backlog.

### Still to be checked at live acceptance

The rc.3 acceptance ran on a throwaway pull request on github.com. Its outcome,
and what is left:

| Item | Result | Still to be checked |
|---|---|---|
| Q1 | Passed: a rename is `renamed` with `old_path`, an added file and its line counts are right, and the base is the merge base (`github:merge_base`). | A read or a reply through an old-name URL of a renamed or transferred repository (whether `not_found` appears); not verified, no such repository was available. |
| Q2 | Passed: an open pull request gives `mergeable` and, when the branch is `blocked`, the blocker; `required_approvals` is `null` with "not readable with this token" when the rulesets have no pull request rule and the classic protection needs admin rights; a merged pull request has no blockers. The blocker wording was too narrow (the branch was blocked by a ruleset's update restriction) and now reads as in the [table above](#pr_info). | An open pull request with a failing check that is not required (the `unstable` note without a blocker), and a branch whose rulesets or classic protection give a count. |
| Q3 | Passed with one defect, fixed in rc.4. `pr_review` and `pr_improve` with `publish=true`: one review, comments on the right lines, the overview edited in place. A review **without a body** is accepted; `line` and `start_line` are returned; **Commit suggestion** applied a suggestion block cleanly (one line, tab indentation intact); a block was re-indented onto the real lines when the model's quote dropped the indentation. The defect: a second `pr_review` run posted a reworded finding again; it is now recognised by place ([No repeated findings](review.md#no-repeated-findings)). | The second run on a new pull request with rc.4: no new comment, skipped findings linked. |
| Q4 | Passed: the author's text is unchanged, the marked region is appended and the title is untouched. | |
| Q5 | Simulated only (no rate limit was hit); covered by the tests. | A real rate limit. |

Also open: GitHub Enterprise Server for all of the above, and the code spans of
the model's text shown as code on github.com (rc.4, [Code in the model's
text](review.md#code-in-the-models-text)).

## What is not supported

- **GraphQL.** Thread resolution, and anything else that needs it, is not
  available.
- **Resolving or unresolving a thread**, and submitting an approval or a change
  request. review-mcp never approves; it only comments.
- **GitHub Apps, OAuth flows and installation tokens.** A personal access token
  is the only credential.
- **Webhooks and a bot mode.** review-mcp acts only when a tool is called.
- **Merging, labels, auto-merge** or any write beyond comments, reviews with
  comments, suggestions and the title and description.

See also: [Setup guide](setup.md#github), [Pull request status](pr-info.md),
[Repository context](repo-context.md), [Serve mode](serve.md) and
[Troubleshooting](troubleshooting.md#github).
