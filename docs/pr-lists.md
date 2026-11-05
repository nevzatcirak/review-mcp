# Pull request lists

Two tools list the open pull requests that concern you, across every
configured provider:

- `pr_list_review_requests` lists the pull requests that ask you for a review.
  With `include_assigned` it also lists the ones assigned to you.
- `pr_list_authored` lists the pull requests you opened.

"You" is the user the provider token belongs to. Both tools only read pull
request metadata (GET requests): no diff, no file, no comment, no description,
no LLM call and no write. They work in stdio and serve mode (in serve mode with
the provider token headers; no LLM key header).

Ask your client, for example: "Which pull requests are waiting for my review?"
or "Which of my pull requests are still open?". Pass an item's `url` to
[`pr_review`](review.md), [`pr_info`](pr-info.md) or [`pr_ask`](ask.md).

## Arguments

| Argument | Tool | Meaning |
|---|---|---|
| `provider` | both | restrict the list to one provider: `gitea`, `bitbucket_server`, `github` or `gitlab`; default every configured provider. A provider that is not configured is refused before any request. |
| `limit` | both | the most pull requests to list, 1 to 100, default 30. A value outside the range is refused, never clamped. |
| `include_assigned` | `pr_list_review_requests` | also list the pull requests assigned to you, default `false`. Bitbucket Server has no assignees (see [Assignees](#assignees)). |

The same lists are available without a client:
`review-mcp diag list review-requests|authored` (see [The diag command](#the-diag-command)).

## What it returns

The markdown is a list, one line per pull request, newest update first:

```text
## Open pull requests that ask for your review

- `gitea` [octo/demo!7](https://gitea.example.com/octo/demo/pulls/7) `Fix the cache key` by `alice` · draft · updated 2026-11-04 · as reviewer
- `gitlab` [group/sub/project!42](https://gitlab.example.com/group/sub/project/-/merge_requests/42) `Add a retry` by `bob` · target `main` · updated 2026-11-03 · as reviewer

Providers:
- `gitea`: ok, 1 listed
- `gitlab`: ok, 1 listed
```

Notes, when there are any, follow under "Notes:".

The link text is `namespace/repo!number` (a GitLab nested group keeps its full
path) and the link goes to the pull request URL. The title, the author and the
target branch are third-party text: control characters and line breaks are
removed, the title is cut at 200 characters and the author at 100, and they
appear only inside code spans. The list is a list and not a table so that no
title can break a cell.

The structured result has the same facts:

| Field | Meaning |
|---|---|
| `role` | `review_requests` or `authored` |
| `items` | the pull requests, newest update first, at most `limit` |
| `items[].provider` | `gitea`, `bitbucket_server`, `github` or `gitlab` |
| `items[].url` | the web URL of the pull request, **built** from the configured host, the namespace, the repository and the number; a URL in the host's answer is never passed on. It is accepted by the other pull request tools. |
| `items[].namespace`, `repo`, `number` | where the pull request is (a GitLab nested group is one `namespace`, `group/sub`) |
| `items[].title`, `author` | as the host reports them (third-party text, cleaned and cut as above) |
| `items[].target_branch` | the branch the pull request merges into; `null` where the host's list does not carry it (GitHub and Gitea). It is never fetched item by item. |
| `items[].draft` | whether the pull request is a draft; `null` where the host does not report it (see [Limits by host](#limits-by-host)) |
| `items[].updated_at` | the last update (UTC) |
| `items[].roles` | why it is listed: `reviewer`, `assignee`, `author`; both `reviewer` and `assignee` when you are both |
| `items[].my_status` | your own review status where the list carries it (Bitbucket Server: `approved`, `needs_work`, `unapproved`); `null` otherwise. Never another person's status. |
| `providers` | one entry per provider asked, in the order `gitea`, `bitbucket_server`, `github`, `gitlab` |
| `providers[].status` | `ok`, `failed`, `unsupported` or `no_token` |
| `providers[].reason` | for `failed`: a fixed reason (see [Why a provider failed](#why-a-provider-failed)); `null` otherwise |
| `providers[].count` | how many of this provider's pull requests are in `items` (the counts add up to the length of `items`) |
| `providers[].has_more` | the host has more pull requests than `items` shows |
| `notes` | fixed sentences about what the list cannot show (see [Notes](#notes)) |

A provider never empties or silently shortens the list: one that fails is
listed with its reason and a note, and the others are listed as usual. An empty
list says "No open pull requests ask for your review." or "You have no open
pull requests." only when every provider answered `ok`; otherwise it says "No
pull requests were listed." and the provider lines tell why.

The items are sorted by `updated_at`, newest first, then by provider, then by
URL, and cut at `limit`.

## Which pull requests are listed

Open pull requests only; merged and closed ones are not listed. Draft pull
requests are listed and flagged.

### What "review requested" means

| Host | A pull request is listed while |
|---|---|
| GitHub | the review is requested of you or of one of your teams, **until you have submitted a review**. A request ends when you review, and a team request is included. |
| Bitbucket Server | you are a reviewer. The list carries your own status (`my_status`). |
| GitLab | you are a requested reviewer of the merge request. |
| Gitea | you are a requested reviewer, or one of your teams is. A request ends when you approve or reject; a comment review does not end it. |

On GitHub a fixed note says so (see [Notes](#notes)).

### Assignees

`include_assigned: true` runs a second query for the pull requests assigned to
you and merges the two lists by URL. A pull request that is both assigned to you
and awaiting your review is listed once, with both roles in `roles`. Bitbucket
Server has no assignees: its entry stays `ok`, only review requests are listed
there, and a note says so. If one of the two queries of a provider fails and the
other works, the provider is `failed` with the reason, the items of the query
that worked are kept, and a note names the role that is missing.

## The request each provider gets

One request per query. The identity is the token's: no user name is sent, except
where the host's API needs one (GitHub and GitLab), and then it comes from the
token's own user.

| Provider | Minimum | Request |
|---|---|---|
| Bitbucket Server / Data Center | 4.10 (the endpoint is documented from there) | `GET /rest/api/1.0/dashboard/pull-requests?state=OPEN&role=REVIEWER` (or `AUTHOR`)`&order=NEWEST&start=0&limit=<limit>`. No `user` parameter is sent. |
| GitLab | 16.0 (the minimum of the GitLab provider) | `GET /api/v4/merge_requests?state=opened&order_by=updated_at&sort=desc&per_page=<limit>&page=1` plus `scope=all&reviewer_id=<your id>` (reviewer), `scope=assigned_to_me` (assignee) or `scope=created_by_me` (author). `reviewer_id` exists since 13.8. Only the reviewer query asks `GET /user` first. |
| GitHub | github.com; GitHub Enterprise Server 3.18 is the one version whose documentation was read (3.10 to 3.17 are not confirmed) | `GET /search/issues?q=is:pr is:open archived:false <qualifier>:<login>&sort=updated&order=desc&per_page=<limit>` with `review-requested:` (reviewer), `assignee:` or `author:`. The login is `github.self_login` when set, else `GET /user`. |
| Gitea | 1.14 (see [Limits by host](#limits-by-host)) | `GET /api/v1/version`, then `GET /api/v1/repos/issues/search?type=pulls&state=open&review_requested=true&page=1&limit=<limit>` (or `assigned=true`, or `created=true`). |

Every provider is asked for at most `limit` items, one page; there is no second
page. Each request goes through the provider's own client: its authentication,
redirect guard, response size caps and fixed error sentences. The providers
are asked in parallel.

## Limits by host

### Which pull requests a full page holds

When more pull requests match than `limit`, the host decides which ones the
page holds, and review-mcp then sorts that page by `updated_at`:

- **Bitbucket Server** is asked with `order=NEWEST`. No `order` value of the
  dashboard is documented as "by last update", so the page holds the newest
  created pull requests, sorted by update afterwards.
- **Gitea** has no sort parameter on its issue search. The order is by creation,
  newest first, in every version since 1.14. The page holds the newest
  **created** pull requests, which are not always the most recently updated.
- **GitHub** and **GitLab** are asked to sort by update, newest first.

Whenever a host has more, `has_more` is set and the "more pull requests" note is
added. Raise `limit` (at most 100) or name a provider.

### Gitea 1.14 and Forgejo

Before 1.14, Gitea's issue search ignores the role filters and would list every
open pull request the token can see, other people's included. So a list first
asks `GET /api/v1/version`, and below 1.14 refuses the list with
`unsupported_version` before any search is sent. The check reads the version
afresh on every call.

- A Forgejo version of the form `7.0.1+gitea-1.21.11` is compared by its
  `+gitea-A.B` part.
- Any other `X.Y[.Z][-suffix][+meta]` is compared by `X.Y`; a major of 2 or more
  counts as new enough.
- A version that cannot be read (an empty value, `development`, a missing field,
  a failed version request) does **not** stop the list: it goes ahead. The only
  trace is a debug log line without the version text.

The `draft` flag comes with Gitea 1.22; before that it is `null`. Up to Gitea
1.25 the page size is capped at the server's `MAX_RESPONSE_ITEMS` (50 by
default), which `has_more` covers.

### Bitbucket Server

`draft` is in the REST API from 8.17; on an older server it is `null`. Your own
status (`my_status`) is found from the identity headers of the same response, so
it needs no second request; without them it is `null`.

### GitHub search limit

The list uses GitHub's search API. Its rate limit is separate from the core one
and is **30 requests a minute**; the list is sent once and **not retried**. A
403 or 429 with an exhausted rate limit or a `Retry-After` header is the reason
`rate_limited`. A bare 403 is `forbidden`. The search returns at most **1,000
results**, which is far above `limit`. A search that GitHub reports as
incomplete sets `has_more`. GitHub Enterprise Server turns rate limits off by
default.

A `github.self_login` or `GET /user` login that could change the query (anything
but letters, digits and `-`, at most 39 characters, optionally followed by
`[bot]`) is refused before the search.

### GitLab `reviews_for_me`

GitLab 18.4 added `scope=reviews_for_me`. It does not exist in 16.0, the
minimum, so it is **not used**; `scope=all&reviewer_id=<your id>` works on every
supported version.

## Tokens without a person

Some tokens do not belong to a person. What each list returns for them:

| Token | Result |
|---|---|
| GitLab project or group access token | The token acts as a bot user and `GET /user` returns that bot. The lists filter on the bot, so they are normally empty, never another person's merge requests. (From reading the GitLab source, not from documentation.) |
| GitHub installation token (`ghs_`) | `/search/issues` is allowed for it and `GET /user` is not. Without `github.self_login` the login cannot be found and the list fails with `auth`. With it (the CI mode default is `github-actions[bot]`) the search runs for that login and lists only what is requested of, assigned to or opened by that bot. Which repositories the search covers for an installation token is not confirmed. |
| Bitbucket Server project or repository HTTP access token (since 7.18) | **Not verified.** What the dashboard returns for such a token is not documented. |
| Gitea | There are no tokens without a person; the filters always apply to the token's owner. |

## Notes

The notes are fixed sentences. A `%s` or `%d` below stands for a provider name
(`Gitea`, `Bitbucket Server`, `GitHub`, `GitLab`), a reason or a count.

| Note | When |
|---|---|
| `More pull requests exist than this list shows; raise limit (at most 100) or name a provider.` | the cut dropped pull requests, or a provider has more than its page held |
| `%s could not be listed (%s); the list does not include its pull requests.` | a provider failed; its fixed reason is the second value |
| `%s could not list its %s (%s); the list does not include them.` | with `include_assigned`, one of a provider's two queries failed and the other worked; the values are the provider, the missing role (`review requests` or `assigned pull requests`) and the reason |
| `Bitbucket Server has no assignees; only review requests are listed there.` | `include_assigned` on Bitbucket Server |
| `On GitHub, a review request ends once you have submitted a review, and requests to your teams are included.` | GitHub is in a review request list and answered `ok` |
| `%d pull requests from %s were left out because their repository could not be matched to the configured host.` | a provider's pull requests were left out because their repository path did not validate (on GitHub also: was not on the configured host); once per provider, with the count |

A provider with the status `unsupported` (the host cannot list the role) or
`no_token` has no note; its line under "Providers" shows it. `no_token` is
serve mode only (see [Serve mode](serve.md#pull-request-lists)).

### Why a provider failed

A failed provider carries one fixed reason, never the host's own text. The
reason follows the class of the provider's error:

| Provider error | Reason |
|---|---|
| `auth` (HTTP 401, or without a status) | `auth` |
| `auth` with HTTP 403 | `forbidden` |
| `rate_limited` | `rate_limited` |
| `transport`, timed out (or the call's deadline passed) | `timeout` |
| `transport`, anything else | `unavailable` |
| `upstream` (a 5xx answer) | `unavailable` |
| `unsupported_version` (Gitea before 1.14) | `unsupported_version` |
| everything else: `not_found`, `protocol`, `too_large`, any other class, an error that is not a provider error | `bad_response` |

`bad_response` includes a 404. A list endpoint answers 404 on a supported
version only when the base URL or its context path is wrong, or a proxy in front
of the host swallows the path; so check the base URL (see
[Troubleshooting](troubleshooting.md#pull-request-lists)). A provider that is
enabled but could not be built is `failed` with `unavailable`. On Gitea before
1.14 the error carries the sentence `Gitea 1.14 or newer is required to list pull requests`; the list shows the
reason `unsupported_version`.

### Argument errors

A call that is refused before any request answers with a fixed sentence:

| When | Sentence |
|---|---|
| `limit` is not a whole number from 1 to 100 | `limit must be a whole number from 1 to 100` |
| `provider` is not one of the four kinds | `provider must be one of gitea, bitbucket_server, github or gitlab` |
| `provider` names a kind that is not configured | `the %s provider is not configured; configured providers: %s` |
| `include_assigned` on `pr_list_authored` | `include_assigned applies to pr_list_review_requests only` |
| no provider is configured | `no provider is configured: server_info lists the configured providers` |

In the `provider` row the two `%s` are the kind and the list of configured kinds.

## Serve mode

Both tools are registered in serve mode. A provider whose token header the
request lacks is skipped before any request: its status is `no_token` and
nothing reaches that host. See [Serve mode](serve.md#pull-request-lists).

## The diag command

```sh
review-mcp diag list review-requests [--provider KIND] [--limit N] [--include-assigned] [--json]
review-mcp diag list authored [--provider KIND] [--limit N] [--json]
```

It runs the same engine over the configured providers and prints the markdown
the tool returns, or with `--json` the structured result. Exit 0 when no
provider failed (an `unsupported` or `no_token` provider is not a failure), 1
when any provider is `failed` (or no provider is configured), 2 on a usage error
(including an argument the engine refuses).

## What it never does

- It reads no diff, file, comment or description, and calls no LLM.
- It writes nothing.
- It lists no merged or closed pull request.
- It follows no second page.
- It never passes on a URL, name or text from a response beyond the cleaned
  title and author, and it logs only counts (never a title, name, namespace,
  URL or token).
