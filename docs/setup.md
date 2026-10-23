# Setup guide

This guide takes you from nothing to a first review: install review-mcp, create
tokens, point it at an LLM, register it with an MCP client, and run it once.
It covers the default transport, stdio, where the MCP client starts review-mcp
as a local process. For a shared server see [Serve mode](serve.md).

Every host name below is a placeholder (`your-gitea.example`,
`bitbucket.example.com`, `ghe.example.com`, `gitlab.example.com`,
`llm.example.com`). Replace them with your own. (`https://github.com` is the
public GitHub product and `https://gitlab.com` the public GitLab one, not
placeholders.)

1. [Install](#1-install)
2. [Create tokens](#2-create-tokens)
3. [Choose the LLM endpoint](#3-choose-the-llm-endpoint)
4. [Register with an MCP client](#4-register-with-an-mcp-client)
5. [First run](#5-first-run)
6. [When something fails](#6-when-something-fails)

## 1. Install

Pick one. All three give you the same binary.

### npm

```sh
npx -y @nevzatcirak/review-mcp version
```

The package downloads nothing at install time and runs no install script: the
binary for your platform comes from an optional dependency
(`@nevzatcirak/review-mcp-<os>-<cpu>`). In an MCP client you use the same
`npx -y @nevzatcirak/review-mcp` as the server command (see
[step 4](#4-register-with-an-mcp-client)). Pin the exact version
(`npx -y @nevzatcirak/review-mcp@2.4.0`) so that `npx` does not reuse an older
cached copy; release candidates of the next version are under the `next` tag.

### Release archive

Download the archive for your platform and `checksums.txt` from the
[GitHub Releases page](https://github.com/nevzatcirak/review-mcp/releases).
Archives are named `review-mcp_<version>_<os>_<arch>.tar.gz` (`.zip` on
Windows), for `linux`, `darwin` and `windows` on `amd64` and `arm64`. Each one
contains the binary, `LICENSE`, `NOTICE`, `THIRD_PARTY_LICENSES` and the
README.

Verify the SHA-256 checksum before you run anything from it:

```sh
# Linux
sha256sum --ignore-missing -c checksums.txt
# macOS
shasum -a 256 --ignore-missing -c checksums.txt
```

```powershell
# Windows PowerShell: compare the printed hash with the line in checksums.txt
Get-FileHash .\review-mcp_<version>_windows_amd64.zip -Algorithm SHA256
```

The output must say `OK` for your archive. Then unpack it and put `review-mcp`
on your `PATH`:

```sh
tar -xzf review-mcp_<version>_linux_amd64.tar.gz
./review-mcp version
```

### go install

Requires the Go version named in the `go` line of
[`go.mod`](../go.mod) (Go 1.26 or newer at the time of writing).

```sh
go install github.com/nevzatcirak/review-mcp/cmd/review-mcp@<tag>
```

Replace `<tag>` with a release tag such as `v1.0.0-rc.1`. The binary lands in
`$(go env GOBIN)` or `$(go env GOPATH)/bin`; put that directory on your `PATH`.
A binary installed this way reports the version Go embeds for the tag, and no
commit unless you build from a checkout with `make build`.

## 2. Create tokens

review-mcp acts as you: it uses your tokens, never a service account of its
own. Create one token per provider you use, give it the least access that
works, and keep it in a secret manager or your shell profile, never in a file
that is checked in.

Use a read-only token to start. `pr_review`, `pr_ask`, `pr_describe` and
`pr_improve` need write access only when you pass `publish=true`, and `pr_comment_reply` and
`pr_comment_create` always write.

### What each token needs

| Use | Needs |
|---|---|
| Read only: `pr_comments`, `pr_info`, `pr_review`, `pr_ask`, `pr_describe` and `pr_improve` with `publish=false` | the read scopes below |
| Read and write: `publish=true`, `pr_comment_reply`, `pr_comment_create` | the read scopes plus the write scope |
| `pr_improve` with `publish=true` | the same as `pr_review` with `publish=true`: comment write for the overview (Gitea `write:issue`) and the write scope for the inline comments (Gitea `write:repository`); see [Suggesting code changes](improve.md#permissions) |
| `pr_describe` with `publish=true` and `publish_mode=description` (also `update_title`) | the read scopes plus write access to the pull request itself, not only to its comments (see the `PATCH` and `PUT` rows below) |
| CI results (`pr_info`, and `pr_review` unless `review.ci_status` is false), the guidelines file and the file tree (`pr_review`, `pr_improve`) | Gitea and Bitbucket Server: nothing beyond the read scopes (the commit status or build status, and file contents at the target branch). GitHub: **Commit statuses: Read** and **Checks: Read** for the CI results (fine-grained token; a classic token's `repo` scope covers them), **Contents: Read** for the guidelines and the tree. See the endpoint tables below |
| Issue compliance (`pr_review`, `pr_describe`): reading the issues a pull request names | Gitea: `read:issue`. GitHub: **Issues: Read** (fine-grained token; a classic token's `repo` / `public_repo` covers it). GitLab: the scopes below (`api` reads issues). Bitbucket Server has no issue tracker. Without the permission, the issue is noted as unreadable and the review goes on; `issue_context` needs no permission. See [Issue compliance](review.md#issue-compliance) |
| GitHub, any use | see [GitHub](#github) below: a fine-grained token with Pull requests, Contents and Metadata, or a classic `repo` / `public_repo` token |
| GitLab, any use | see [GitLab](#gitlab) below: a legacy personal access token with the scopes `api` and `read_repository`, or a project or group access token with the role Reporter (read) or Developer (publish) and the same scopes |

> **Every scope in this guide is unconfirmed.** They are derived from the API
> calls the code makes, in the tables below, and each is marked "verify at V1
> acceptance (item A3)": the acceptance run creates tokens with exactly these
> scopes and records every one that was wrong. If a call fails with
> `authentication failed` and you followed the table, you may have found one:
> see [Token scopes](troubleshooting.md#token-scopes).

### Gitea

Create a token under **Settings, Applications, Generate New Token** in your
Gitea (`https://your-gitea.example/user/settings/applications`). Gitea names
the scope categories `repository` and `issue`, each with `read` and `write`.

| Token | Scopes | Status |
|---|---|---|
| Read only | `read:repository`, `read:issue`, `read:user` | verify at V1 acceptance (item A3) |
| Read and write | `write:repository` (posting inline comments as a review; editing the pull request for `pr_describe` with `publish_mode=description`), `write:issue` (posting and editing PR comments), `read:user`; each write scope covers reading as well (add `read:issue` if your Gitea version lists them separately) | verify at V1 acceptance (item A3) |

`read:user` lets review-mcp identify the token's own user (`GET /api/v1/user`),
so that it only ever edits or skips its own comments.

Endpoints the Gitea provider calls (under `{base_url}/api/v1/repos/{owner}/{repo}`, except `GET /api/v1/user`):

| Method and endpoint | Purpose | Scope | Code |
|---|---|---|---|
| `GET /pulls/{n}` | the pull request: title, description, branches, head and merge base | `read:repository` | `internal/provider/gitea/gitea.go` (`GetPullRequest`) |
| `GET /pulls/{n}/commits` | commit messages | `read:repository` | `internal/provider/gitea/gitea.go` (`GetCommitMessages`) |
| `GET /pulls/{n}.diff` | the unified diff | `read:repository` | `internal/provider/gitea/diff.go` (`GetDiff`) |
| `GET /pulls/{n}/files` | the list of changed files | `read:repository` | `internal/provider/gitea/diff.go` (`GetDiff`) |
| `GET /raw/{path}?ref={sha}` | full file contents on both sides, for extra context | `read:repository` | `internal/provider/gitea/diff.go` (`rawPath`, `fetchSide`) |
| `GET /commits/{sha}/status` | the CI results of the head commit (`pr_review`, `pr_info`): the combined commit status, paged | `read:repository` (verify at the 2F acceptance) | `internal/provider/gitea/reads.go` (`GetChecks`) |
| `GET /raw/{path}?ref={base_sha}` and `GET /contents[/{dir}]?ref={base_sha}` | the guidelines file and the listing of the touched directories at the base revision (`pr_review`, `pr_improve`) | `read:repository` (verify at the 2F acceptance) | `internal/provider/gitea/reads.go` (`GetFileAt`, `ListDir`) |
| `GET /pulls/{n}/reviews` and `GET /pulls/{n}/reviews/{id}/comments` | inline review comments (`pr_comments`, reply lookup); before posting inline comments, a check for a pending review of the token's user; after posting, the posted comments' links | `read:repository` | `internal/provider/gitea/comments.go` (`reviewComments`), `internal/provider/gitea/write.go` (`PostInlineComments`) |
| `POST /pulls/{n}/reviews` | inline comments (`publish=true`, and `pr_comment_create` with `file` and `line`), posted as one review with event `COMMENT` on the head commit (one review per comment if that fails) | `write:repository` | `internal/provider/gitea/write.go` (`PostInlineComments`) |
| `DELETE /pulls/{n}/reviews/{id}` | delete a pending review of the token's user that a failed post left behind | `write:repository` | `internal/provider/gitea/write.go` (`deletePending`) |
| `GET /issues/{n}/comments` | PR-level comments (`pr_comments`) | `read:issue` | `internal/provider/gitea/comments.go` (`ListThreads`) |
| `GET /issues/comments/{id}` | find the comment a reply refers to (`pr_comment_reply`); re-read a comment and check its author before editing it | `read:issue` | `internal/provider/gitea/comments.go` (`ReplyToComment`), `internal/provider/gitea/write.go` (`EditComment`) |
| `PATCH /issues/comments/{id}` | edit a PR comment the token's user wrote (the overview of `pr_review` and `pr_improve`, the comment of `pr_describe`) | `write:issue` | `internal/provider/gitea/write.go` (`EditComment`) |
| `PATCH /pulls/{n}` | `pr_describe` with `publish_mode=description`: set the description (the author's text plus the marked region) and, with `update_title`, the title. Only the fields being changed are sent, so reviewers, labels and the base branch are not touched. The pull request is read again with `GET /pulls/{n}` right before | `write:repository`; verify at the v2 acceptance (item N3) | `internal/provider/gitea/write.go` (`UpdatePullRequest`) |
| `GET /api/v1/user` | the token's own user | `read:user` | `internal/provider/gitea/write.go` (`CurrentUser`) |
| `GET /pulls/{n}` (again), `GET /pulls/{n}/reviews`, and `GET /pulls/{n}/reviews/{id}/comments` for the token user's own comment-only reviews | `pr_info`: requested reviewers, each reviewer's state, and the check whether a review of the token's user is review-mcp's own (comment bodies are looked at for markers and never kept) | `read:repository` | `internal/provider/gitea/status.go` (`GetReviewStatus`) |
| `GET /branch_protections` | `pr_info`: the rules are listed and the one for the target branch is picked by name or glob pattern. Optional: a token without the right to read branch protection (it may need repository admin) gets `null` and the note "not readable with this token", never a guess | `read:repository`; may need admin | `internal/provider/gitea/status.go` (`GetReviewStatus`) |
| `POST /issues/{n}/comments` | post a comment: `publish=true` (the overview), `pr_comment_create` without a file, and `pr_comment_reply` (Gitea has no thread reply, so a reply is a new PR-level comment with a quote line) | `write:issue` | `internal/provider/gitea/gitea.go` (`PostComment`) |

`pr_comment_create` adds no endpoint of its own: a PR-level comment needs the
`write:issue` rows above, and an inline one also reads the diff (the
`read:repository` rows) to validate the line and needs `write:repository`.
Every row's scope is "verify at V1 acceptance (item A3)".

### Bitbucket Server / Data Center

review-mcp needs Bitbucket Server or Data Center 7.0 or later and an HTTP
access token. Create it under **Profile picture, Manage account, HTTP access
tokens, Create token** (a personal token), or on the project or repository
under **Settings, Access tokens** (a project or repository token). The token
is sent as `Authorization: Bearer ...`; basic authentication is not supported.

The permission level is chosen when you create the token. Bitbucket tokens have
no per-endpoint scopes.

| Token | Permission | Status |
|---|---|---|
| Read only | Project read or Repository read | verify at V1 acceptance (item A3) |
| Read and write | Repository write | verify at V1 acceptance (item A3) |

Endpoints the Bitbucket Server provider calls (under `{base_url}`, which
includes your context path):

| Method and endpoint | Purpose | Permission | Code |
|---|---|---|---|
| `GET /rest/api/1.0/application-properties` | version probe (7.0 or later), where a failure is ignored; and the token's own user, read from the `X-AUSERNAME` and `X-AUSERID` response headers, where a failure is an error | read | `internal/provider/bitbucketserver/base.go` (`ensureSupported`), `internal/provider/bitbucketserver/write.go` (`CurrentUser`) |
| `GET /rest/api/1.0/projects/{key}/repos/{slug}/pull-requests/{id}` | the pull request | read | `internal/provider/bitbucketserver/bitbucketserver.go` (`GetPullRequest`) |
| `GET .../pull-requests/{id}/commits` | commit messages, and the ancestor walk | read | `bitbucketserver.go`, `base.go` (`ancestorWalk`) |
| `GET /rest/api/latest/projects/{key}/repos/{slug}/pull-requests/{id}/merge-base` | the merge base; a 404 falls back to the ancestor walk | read | `internal/provider/bitbucketserver/base.go` |
| `GET /rest/api/1.0/projects/{key}/repos/{slug}/commits?since=..&until=..` | ancestor walk fallback | read | `internal/provider/bitbucketserver/base.go` (`ancestorWalk`) |
| `GET .../pull-requests/{id}/changes` | the list of changed files | read | `internal/provider/bitbucketserver/diff.go` (`GetDiff`) |
| `GET /rest/api/1.0/projects/{key}/repos/{slug}/raw/{path}?at={sha}` | file contents on both sides; the patch is built from them | read | `internal/provider/bitbucketserver/diff.go` (`rawPath`, `fetchSide`) |
| `GET /rest/api/latest/projects/{key}/repos/{slug}/commits/{sha}/builds`; on a 404 only, `GET /rest/build-status/1.0/commits/{sha}` | the CI results of the head commit (`pr_review`, `pr_info`): the build statuses, paged | read (verify at the 2F acceptance) | `internal/provider/bitbucketserver/reads.go` (`GetChecks`) |
| `GET /rest/api/1.0/projects/{key}/repos/{slug}/raw/{path}?at={base_sha}` and `GET .../browse[/{dir}]?at={base_sha}` | the guidelines file and the listing of the touched directories at the base revision (`pr_review`, `pr_improve`) | read (verify at the 2F acceptance) | `internal/provider/bitbucketserver/reads.go` (`GetFileAt`, `ListDir`) |
| `GET .../pull-requests/{id}/activities` | comment threads (`pr_comments`, reply lookup) | read | `internal/provider/bitbucketserver/comments.go` (`ListThreads`) |
| `GET .../pull-requests/{id}` (again) and `GET .../pull-requests/{id}/merge` | `pr_info`: the reviewers with their status, and whether the pull request can be merged and why not (an open pull request only) | read | `internal/provider/bitbucketserver/status.go` (`GetReviewStatus`) |
| `POST .../pull-requests/{id}/comments` | PR-level comment (`publish=true`, `pr_comment_create` without a file) | write | `internal/provider/bitbucketserver/bitbucketserver.go` (`PostComment`) |
| `POST .../pull-requests/{id}/comments` with `parent` | reply inside a thread (`pr_comment_reply`) | write | `internal/provider/bitbucketserver/comments.go` (`ReplyToComment`) |
| `POST .../pull-requests/{id}/comments` with `anchor` | inline comment on a changed or context line, one request per comment (`publish=true`, and `pr_comment_create` with `file` and `line`) | read (to be confirmed at A3) | `internal/provider/bitbucketserver/write.go` (`PostInlineComments`) |
| `GET .../pull-requests/{id}/comments/{commentId}` | a comment's version and author, read before editing it | read | `internal/provider/bitbucketserver/write.go` (`EditComment`) |
| `PUT .../pull-requests/{id}/comments/{commentId}` | edit a comment the token's user wrote (the overview of `pr_review` and `pr_improve`, the comment of `pr_describe`) | read (to be confirmed at A3) | `internal/provider/bitbucketserver/write.go` (`EditComment`) |
| `PUT .../pull-requests/{id}` | `pr_describe` with `publish_mode=description`: update the description and, with `update_title`, the title. It is a full update: after a fresh `GET`, the request carries the version, the title, the description, the reviewers (by user name) and the draft flag, never the target branch. A stale version is answered with a conflict. The reviewers are read before and after to check that none was dropped | write; verify at the v2 acceptance (item N3) | `internal/provider/bitbucketserver/write.go` (`UpdatePullRequest`) |

`pr_comment_create` uses the comment rows above plus the read rows for the
pull request, its changes and its file contents, which validate the line
against the diff. The permission column says what the code needs; whether Bitbucket accepts a
read-level token for commenting is exactly what item A3 checks. Bitbucket lets
users with repository read permission comment on pull requests, so Repository
read may be enough for posting, inline comments and editing your own comments;
this is to be confirmed at A3. Every row is
"verify at V1 acceptance (item A3)". Personal-repository URLs
(`/users/{user}/repos/...`) use `/users/{user}` in place of `/projects/{key}`
in the endpoints above.

### GitHub

review-mcp works with github.com and with GitHub Enterprise Server through a
personal access token (not an app or an OAuth flow). The full reference is
[GitHub](github.md).

1. Create the token. On the public product open
   `https://github.com/settings/tokens` (**Settings, Developer settings,
   Personal access tokens**); on a GHES use the same page on your own host.
   - **Fine-grained token (recommended).** Choose the resource owner that
     owns the repository, select only the repositories you review, and under
     **Repository permissions** set **Pull requests** to *Read and write*,
     **Contents** to *Read-only*, **Commit statuses** to *Read-only*,
     **Checks** to *Read-only* and **Metadata** to *Read-only* (it is
     selected for you). For read-only use, *Read-only* on Pull requests is
     enough. **Commit statuses** and **Checks** are needed only for the CI
     results (`pr_info`, and `pr_review` unless `review.ci_status` is
     false); with only one of them the CI results are partial with a note,
     without both the CI state is `unreadable` with a note, and everything
     else works.
   - **Classic token.** Select the scope `repo` (private repositories), or
     `public_repo` if you only use public repositories.
2. If your organisation uses SAML single sign-on, authorise the token for it
   on the token's page.
3. Set the web address of your GitHub as the base URL, and the token:
   `REVIEW_MCP_GITHUB_BASE_URL=https://github.com` for the public product, or
   `REVIEW_MCP_GITHUB_BASE_URL=https://ghe.example.com` for a GHES, and
   `REVIEW_MCP_GITHUB_TOKEN`. The base URL has **no default**: GitHub is
   enabled only when you set it. The API address is derived from it
   (`https://api.github.com` for exactly `https://github.com`, otherwise
   `{base_url}/api/v3`); set `REVIEW_MCP_GITHUB_API_URL` only when your API is
   elsewhere.

| Token | Permissions | Status |
|---|---|---|
| Read only | fine-grained: Pull requests read, Contents read, Metadata read; classic: `repo` or `public_repo` | verify at the v2 acceptance (items Q1 to Q4) |
| Read and write | fine-grained: Pull requests read and write, Contents read, Metadata read; classic: the same `repo` or `public_repo` | verify at the v2 acceptance (items Q1 to Q4) |
| CI results (`pr_info`, `pr_review`) | fine-grained: Commit statuses read and Checks read (both: the read fails as a whole when one is missing); classic: the `repo` scope you already have covers them | verify at the 2F acceptance |
| `pr_info` required approvals from classic branch protection (optional) | fine-grained: Administration read; classic: admin rights on the repository | without it the count comes from the rulesets, with a note |

Endpoints the GitHub provider calls (under the API base; `{r}` is
`/repos/{owner}/{repo}`):

| Method and endpoint | Purpose | Permission | Code |
|---|---|---|---|
| `GET {r}/pulls/{n}` | the pull request (title, description, branches, head, state, draft, `mergeable`, `mergeable_state`, requested reviewers); read again for `pr_info` | Pull requests read | `internal/provider/github/github.go` (`GetPullRequest`), `status.go` (`GetReviewStatus`) |
| `GET {r}/compare/{base}...{head}` | the merge base (`github:merge_base`); on any failure the base revision of the pull request is used (`github:base_sha`) | Contents read | `internal/provider/github/github.go` |
| `GET {r}/pulls/{n}/files` and `GET {r}/pulls/{n}/commits` | the changed files and their patches (at most 3000), the commit messages (at most 250), followed through the `Link` header | Pull requests read | `internal/provider/github/diff.go`, `github.go` |
| `GET {r}/contents/{path}?ref={sha}` | the full file contents of both sides, for extra context, as raw content | Contents read | `internal/provider/github/diff.go` |
| `GET {r}/commits/{sha}/check-runs` and `GET {r}/commits/{sha}/status` | the CI results of the head commit (`pr_review`, `pr_info`): the check runs and the combined commit status, followed through the `Link` header (at most 10 pages of 100 each), merged by name | Checks read (check runs), Commit statuses read (status); verify at the 2F acceptance | `internal/provider/github/reads.go` (`GetChecks`) |
| `GET {r}/contents/{path}?ref={base_sha}` and `GET {r}/contents/{dir}?ref={base_sha}` | the guidelines file and the listing of the touched directories at the base revision (`pr_review`, `pr_improve`) | Contents read | `internal/provider/github/reads.go` (`GetFileAt`, `ListDir`) |
| `GET {r}/issues/{n}/comments`, `GET {r}/pulls/{n}/comments`, `GET {r}/pulls/{n}/reviews` | `pr_comments`, reply lookup, `pr_info` | Pull requests read | `internal/provider/github/comments.go`, `status.go` |
| `GET {r}/issues/comments/{id}`, `GET {r}/pulls/comments/{id}`, `GET {r}/pulls/{n}/reviews/{id}` | find the comment a reply or an edit refers to; read a comment and check its author before editing it | Pull requests read | `internal/provider/github/comments.go` (`locate`) |
| `GET /user` | the token's own user | any token | `internal/provider/github/github.go` (`CurrentUser`) |
| `POST {r}/pulls/{n}/reviews` | inline comments of `publish=true` and of `pr_comment_create` with `file` and `line`, as one review with event `COMMENT` on the head commit and no body | Pull requests write | `internal/provider/github/inline.go` (`PostInlineComments`) |
| `POST {r}/pulls/{n}/comments` | one inline comment alone, only after GitHub refused the whole review with a 422 | Pull requests write | `internal/provider/github/inline.go` |
| `GET {r}/pulls/{n}/reviews/{id}/comments` | the ids and links of the posted comments; the markers of the token user's own reviews for `pr_info` | Pull requests read | `internal/provider/github/inline.go`, `status.go` |
| `POST {r}/issues/{n}/comments` | a PR-level comment (`publish=true`, `pr_comment_create` without a file, and the quoting reply to a general comment) | Pull requests write | `internal/provider/github/comments.go` (`PostComment`) |
| `POST {r}/pulls/{n}/comments/{id}/replies` | a reply inside an inline thread | Pull requests write | `internal/provider/github/comments.go` (`ReplyToComment`) |
| `PATCH {r}/issues/comments/{id}`, `PATCH {r}/pulls/comments/{id}` | edit a comment the token's user wrote (the overview of `pr_review` and `pr_improve`, the comment of `pr_describe`) | Pull requests write | `internal/provider/github/comments.go` (`EditComment`) |
| `PATCH {r}/pulls/{n}` | `pr_describe` with `publish_mode=description`: set the description and, with `update_title`, the title; only those fields are sent. The pull request is read again right before | Pull requests write | `internal/provider/github/status.go` (`UpdatePullRequest`) |
| `GET {r}/rules/branches/{branch}` | `pr_info`: required approvals from the rulesets | Metadata read (verify at Q2) | `internal/provider/github/status.go` (`readRequiredApprovals`) |
| `GET {r}/branches/{branch}/protection` | `pr_info`: required approvals from the classic protection. Optional: a token without the right gets a note, never a guess | Administration read | `internal/provider/github/status.go` (`readRequiredApprovals`) |

Every row's permission is derived from the API calls and unconfirmed until the
v2 acceptance. With the repository context enabled, `git` also fetches
`refs/pull/{n}/head` over HTTPS with the same token (Contents read).

### GitLab

review-mcp works with gitlab.com and with self-managed GitLab 16.0 or newer
through a token and the REST API v4. The full reference is [GitLab](gitlab.md).

1. Create the token. Three kinds work, and all are sent as `PRIVATE-TOKEN`:
   - **Personal access token** (**Preferences, Access tokens** in your
     profile). **Use a legacy personal access token with the scopes `api` and
     `read_repository`**: GitLab refused fine-grained personal access tokens
     and the `read_api` scope in testing. `api` covers reading, publishing,
     `pr_comment_reply` and `pr_comment_create`; `read_repository` is for the
     [repository context](repo-context.md). If the tool answers `authentication
     failed` (HTTP 401 or 403) with a fine-grained token or `read_api`, see
     [Authentication failed on GitLab](troubleshooting.md#authentication-failed-on-gitlab).
   - **Project access token** or **group access token** (**Settings, Access
     tokens** of the project or group). Role **Reporter** for reads,
     **Developer** to publish. Scopes as for a personal token. The token's user
     is a bot, and review-mcp's comments are written under its name.
2. Set the web address of your GitLab as the base URL, and the token:
   `REVIEW_MCP_GITLAB_BASE_URL=https://gitlab.com` for the public product, or
   `REVIEW_MCP_GITLAB_BASE_URL=https://gitlab.example.com` (with a context path
   if your instance has one, for example `https://example.com/gitlab`), and
   `REVIEW_MCP_GITLAB_TOKEN`. The base URL has **no default**: GitLab is
   enabled only when you set it. The API address is always `{base}/api/v4`.

| Token | Permissions | Status |
|---|---|---|
| Read only | personal: legacy token with `api` (`read_api` was refused in testing); project or group: role Reporter | verify at the 2C acceptance (items Q1, Q2, Q6, Q7) |
| Read and write | personal: legacy token with `api`; project or group: role Developer | verify at the 2C acceptance (items Q3 to Q5b) |
| Repository context | personal: `read_repository` (with `api`); project or group: Reporter | verify at the 2C acceptance |

Endpoints the GitLab provider calls (under `{base}/api/v4`; `{p}` is
`/projects/{full path}` with the slashes of the path sent as `%2F`, and `{mr}`
is `{p}/merge_requests/{iid}`):

| Method and endpoint | Purpose | Needs | Code |
|---|---|---|---|
| `GET {mr}` | the merge request (title, description, state, draft, `detailed_merge_status`, `diff_refs`, `web_url`, `changes_count`); read again for `pr_info` | read | `internal/provider/gitlab/gitlab.go` (`GetPullRequest`) |
| `GET {mr}/diffs`, `GET {mr}/commits` | the changed files with their hunks (at most 4000), the commit messages (at most 1000), followed through the `Link` header | read | `internal/provider/gitlab/diff.go`, `gitlab.go` |
| `GET {p}/repository/files/{path}/raw?ref={sha}` | the full contents of both sides for extra context; the guidelines file at the base revision | read | `internal/provider/gitlab/diff.go`, `reads.go` (`GetFileAt`) |
| `GET {p}/repository/tree?path={dir}&ref={rev}&pagination=keyset` | the listing of the touched directories at the base revision | read | `internal/provider/gitlab/reads.go` (`ListDir`) |
| `GET {p}/repository/commits/{sha}/statuses` | the CI results of the head commit (the latest status of each name) | read | `internal/provider/gitlab/checks.go` (`GetChecks`) |
| `GET {mr}/discussions` | `pr_comments`, reply lookup, duplicate checks | read | `internal/provider/gitlab/comments.go` |
| `GET {mr}/reviewers`, `GET {mr}/approvals` | `pr_info`: reviewers and their states, approvers, required approvals | read | `internal/provider/gitlab/status.go` |
| `GET /user` | the token's own user (a bot for a project or group token) | read | `internal/provider/gitlab/gitlab.go` (`CurrentUser`) |
| `GET {mr}/notes/{id}` | read a note and check its author before editing it | read | `internal/provider/gitlab/comments.go` (`EditComment`) |
| `POST {mr}/notes` | a merge request comment (`publish=true`, `pr_comment_create` without a file) | write | `internal/provider/gitlab/comments.go` (`PostComment`) |
| `POST {mr}/discussions` | one inline discussion per finding or suggestion (`publish=true`, `pr_comment_create` with `file` and `line`) | write | `internal/provider/gitlab/inline.go` |
| `POST {mr}/discussions/{id}/notes` | a reply inside a thread | write | `internal/provider/gitlab/comments.go` (`ReplyToComment`) |
| `PUT {mr}/notes/{id}` | edit a note the token's user wrote (the overview of `pr_review` and `pr_improve`, the comment of `pr_describe`) | write | `internal/provider/gitlab/comments.go` (`EditComment`) |
| `DELETE {mr}/notes/{id}` | only the guard for a general note that GitLab created because it could not place an inline comment: the note just created, never any other | write | `internal/provider/gitlab/inline.go` |
| `PUT {mr}` | `pr_describe` with `publish_mode=description`: set the description and, with `update_title`, the title; only those fields are sent. The merge request is read again right before | write | `internal/provider/gitlab/update.go` |

Every row's permission is derived from the API calls and unconfirmed until the
2C acceptance. With the repository context enabled, `git` also fetches
`refs/merge-requests/{iid}/head` over HTTPS with the same token
(`read_repository`).

### Give review-mcp the tokens

Pass tokens through the environment only:

| Variable | Provider |
|---|---|
| `REVIEW_MCP_GITEA_TOKEN` | Gitea |
| `REVIEW_MCP_BITBUCKET_SERVER_TOKEN` | Bitbucket Server |
| `REVIEW_MCP_GITHUB_TOKEN` | GitHub |
| `REVIEW_MCP_GITLAB_TOKEN` | GitLab |

Also set the base URL of each provider you use (`REVIEW_MCP_GITEA_BASE_URL`,
`REVIEW_MCP_BITBUCKET_SERVER_BASE_URL`, `REVIEW_MCP_GITHUB_BASE_URL`,
`REVIEW_MCP_GITLAB_BASE_URL`; the Bitbucket and GitLab ones include any
context path, and the GitHub and GitLab ones have no default). A provider without a base URL is disabled. At least one must be
enabled. More about base URLs, CA certificates and the context path is in
[Troubleshooting](troubleshooting.md#base-url-notes).

A server with a private CA needs its PEM bundle, which is used in addition to
the system roots. Each provider has the same two keys:

| Key | Variable | Default | Meaning |
|---|---|---|---|
| `gitea.ca_cert` | `REVIEW_MCP_GITEA_CA_CERT` | none | A PEM bundle for a private CA. |
| `gitea.insecure_skip_verify` | `REVIEW_MCP_GITEA_INSECURE_SKIP_VERIFY` | `false` | Turns TLS verification off; review-mcp warns at startup. Prefer `ca_cert`. |
| `bitbucket_server.ca_cert` | `REVIEW_MCP_BITBUCKET_SERVER_CA_CERT` | none | A PEM bundle for a private CA. |
| `bitbucket_server.insecure_skip_verify` | `REVIEW_MCP_BITBUCKET_SERVER_INSECURE_SKIP_VERIFY` | `false` | Turns TLS verification off; review-mcp warns at startup. Prefer `ca_cert`. |
| `github.ca_cert`, `github.insecure_skip_verify` | `REVIEW_MCP_GITHUB_CA_CERT`, `REVIEW_MCP_GITHUB_INSECURE_SKIP_VERIFY` | none, `false` | As above; see [GitHub](github.md). |
| `gitlab.ca_cert`, `gitlab.insecure_skip_verify` | `REVIEW_MCP_GITLAB_CA_CERT`, `REVIEW_MCP_GITLAB_INSECURE_SKIP_VERIFY` | none, `false` | As above; see [GitLab](gitlab.md). |

`insecure_skip_verify` is for confirming on a throwaway setup that TLS is the
problem. Do not leave it on: every certificate is accepted, so a
man-in-the-middle is not noticed.

## 3. Choose the LLM endpoint

review-mcp works with any OpenAI-compatible chat-completions endpoint. It has
no built-in default for the URL or the model; the window size is read from the endpoint unless you set it.

| Variable | Example | Notes |
|---|---|---|
| `REVIEW_MCP_LLM_BASE_URL` | `https://llm.example.com/v1` | `/chat/completions` is appended to it |
| `REVIEW_MCP_LLM_MODEL` | `your-model-name` | the model name your endpoint expects |
| `REVIEW_MCP_LLM_API_KEY` | (secret) | sent as a bearer token |
| `REVIEW_MCP_LLM_CONTEXT_WINDOW` | `32000` | optional; the real size of the window, in tokens (at least 4096). Unset: read from the endpoint's model list |
| `REVIEW_MCP_LLM_MAX_OUTPUT_TOKENS` | `2000` | optional; how long an answer you allow |

The pull request's title, description and diff are sent to this endpoint when
you call `pr_review`, `pr_ask`, `pr_describe` or `pr_improve`. Use an endpoint you trust with that code.

### Context window auto-detection

Leave `REVIEW_MCP_LLM_CONTEXT_WINDOW` unset and review-mcp asks the endpoint
once per process: `GET {llm.base_url}/models`, then the entry whose `id` is
exactly `llm.model`. It reads the first of these fields that holds a positive
integer, in this order: `max_model_len`, `context_length`, `context_window`,
`max_context_length`. It uses 90 % of that value, rounded down, with a floor of
4096. Training-size fields such as `n_ctx_train` are never used: a server often
serves a smaller context than the model was trained with. `server_info` shows
the resolved value and where it came from.

If the endpoint does not list the model, or lists it without any of those
fields, the call fails with a sentence that names the key to change (see
[Troubleshooting](troubleshooting.md#every-error-sentence)); review-mcp never
guesses. Then set `REVIEW_MCP_LLM_CONTEXT_WINDOW` yourself. A value you set
always wins, and the endpoint is not asked.

Ollama's `/v1/models` does not report the served context. Set
`llm.context_window` to the `num_ctx` you run the model with.

### Local models and slow endpoints

A local model can take minutes on a large prompt. Two settings help:

- `diff.max_tokens` (`REVIEW_MCP_DIFF_MAX_TOKENS`, at least 1000, unset by
  default) caps the diff below what the context window allows, for example
  `24000`. Each model call gets snappier. A pull request that no longer fits
  one call is reviewed in several parts (`review.max_chunks`, default 8), so
  the cap makes each part smaller, not the review shorter; files left after
  the last part are listed as omitted in the coverage section
  ([Large pull requests](review.md#large-pull-requests)).
- Leave `llm.wait_seconds` (`REVIEW_MCP_LLM_WAIT_SECONDS`, default 45) at its
  default. A call that is not done after that long answers with a `job_id` and
  the review continues in the background; `job_result` collects it
  ([Slow endpoints](review.md#slow-endpoints)).

`llm.timeout_seconds` (default 300) bounds one request to the model.

### Diff limits

The `diff.*` keys decide how much of a pull request is read from the provider
and how much of it goes to the model. Each key has an environment variable of
the form `REVIEW_MCP_DIFF_<NAME>`, and a `[diff]` table key in the TOML file.

| Key | Environment variable | Default | Range | Meaning |
|---|---|---|---|---|
| `diff.max_files_full_content` | `REVIEW_MCP_DIFF_MAX_FILES_FULL_CONTENT` | 300 (50 before 2.5.1) | at least 1 | How many changed files get their base and head contents fetched. Every file within the limit costs two content requests to the provider. Gitea, GitHub and GitLab: files beyond the limit are reviewed from their patch, their contents are not fetched (less context, and findings may not anchor to a line). Bitbucket Server: files beyond the limit are **skipped** (`file_limit`), because the patch is built from the contents. The result then names this key in its notes, with the number of files. |
| `diff.max_file_bytes` | `REVIEW_MCP_DIFF_MAX_FILE_BYTES` | 1048576 (1 MiB) | at least 1024 | The largest file content (each side) that is fetched. A larger file is skipped as `size_limit` on Bitbucket Server; on Gitea, GitHub and GitLab it keeps its patch and its contents are not fetched. Also bounds the project guidelines file. |
| `diff.max_diff_bytes` | `REVIEW_MCP_DIFF_MAX_DIFF_BYTES` | 20971520 (20 MiB) | at least `diff.max_file_bytes` | The largest answer read when a provider returns the whole diff of a pull request in one response (Gitea). A larger diff fails the call with the error `too_large`, which names this key. |
| `diff.large_patch_policy` | `REVIEW_MCP_DIFF_LARGE_PATCH_POLICY` | `clip` | `clip` or `skip` | What to do with a file that does not fit the diff budget on its own: `clip` cuts it to fit and marks it, `skip` leaves it out (`too_large`). See [`diff.large_patch_policy`](troubleshooting.md#difflarge_patch_policy). |
| `diff.max_tokens` | `REVIEW_MCP_DIFF_MAX_TOKENS` | unset | at least 1000 | Caps the diff of one model call below what the context window allows. A pull request that no longer fits one call is reviewed in parts (`review.max_chunks`). See [Local models and slow endpoints](#local-models-and-slow-endpoints). |
| `diff.extra_lines_before` | `REVIEW_MCP_DIFF_EXTRA_LINES_BEFORE` | 5 | 0 to 10 | Unchanged lines added before each hunk, from the fetched contents, when the whole diff fits the budget. |
| `diff.extra_lines_after` | `REVIEW_MCP_DIFF_EXTRA_LINES_AFTER` | 1 | 0 to 10 | The same, after each hunk. |
| `diff.skip_extend_extensions` | `REVIEW_MCP_DIFF_SKIP_EXTEND_EXTENSIONS` | `.md,.txt` | each entry starts with `.` | Files whose path ends with one of these extensions get no extra context lines. |
| `diff.max_description_tokens` | `REVIEW_MCP_DIFF_MAX_DESCRIPTION_TOKENS` | 500 | at least 1 | The pull request description is cut to this many tokens in the prompt. |
| `diff.max_commits_tokens` | `REVIEW_MCP_DIFF_MAX_COMMITS_TOKENS` | 500 | at least 1 | The commit messages of `pr_describe` are cut to this many tokens. |
| `diff.ignore_generated_frameworks` | `REVIEW_MCP_DIFF_IGNORE_GENERATED_FRAMEWORKS` | empty | names from the built-in table | Frameworks whose generated files are filtered out (`generated:<framework>`). An unknown name fails startup. |

A pull request with many files: raise `diff.max_files_full_content` when the
result says that files were skipped because of it. The files beyond the limit
cost nothing on Gitea, GitHub and GitLab, so the limit matters most on
Bitbucket Server. Raising it adds two content requests per file, and the
review still honours `diff.max_tokens`, the context window and
`review.max_chunks` ([Large pull requests](review.md#large-pull-requests)).

### Rate limits

A gateway often allows only a few requests per minute for one key, and
parallel focused reviews ([Multi-perspective review](review.md#focused-reviews-and-the-merged-publish))
then fail with HTTP 429. review-mcp keeps its own model calls within a limit
you set, per endpoint and shared by every tool call of the process, so
parallel calls wait for each other instead of failing.

| Key | Environment variable | Default | Meaning |
|---|---|---|---|
| `llm.requests_per_minute` | `REVIEW_MCP_LLM_REQUESTS_PER_MINUTE` | 0 (no limit), 0 to 100000 | The most HTTP requests (every attempt, retries included) started in any 60-second window, per endpoint. |
| `llm.max_concurrent_requests` | `REVIEW_MCP_LLM_MAX_CONCURRENT_REQUESTS` | 0 (unlimited), 0 to 256 | The most requests in flight at once, per endpoint. |
| `llm.max_rate_wait_seconds` | `REVIEW_MCP_LLM_MAX_RATE_WAIT_SECONDS` | 60, 0 to 600 | The longest wait for the endpoint's own rate limit (after HTTP 429 or a reset header) before the call fails or moves to a [fallback model](#fallback-models). It does not bound the waits in review-mcp's own queue (`llm.requests_per_minute`, `llm.max_concurrent_requests`). |
| `llm.max_rate_retries` | `REVIEW_MCP_LLM_MAX_RATE_RETRIES` | 4, 0 to 20 | Retries after HTTP 429 for one model call, counted apart from `llm.max_retries`. |

**Set `llm.requests_per_minute` to your gateway's per-minute limit**, and a
little below it when other clients share the key. Nothing changes for a
setup that sets none of these keys. `server_info` shows all four.

How it works:

- The limit is per endpoint: the base URL and the API key together. Two models
  on the same base URL and key share one limit, because a gateway limits the
  key, not the model. The key itself is never stored or logged; it only picks
  the limit. The limit covers one review-mcp process: a second process (another
  client session) against the same gateway has its own.
- A request starts when fewer than `requests_per_minute` started in the last
  60 seconds and fewer than `max_concurrent_requests` are in flight; otherwise
  it waits, first come first served. This wait is review-mcp's own and is not
  bounded by `llm.max_rate_wait_seconds`: a queue of five parallel reviews on a
  limit of 2 per minute simply takes a few minutes, and the later reviews
  report the delay. It ends when the request may start or the call is
  canceled.
- An HTTP 429 puts the whole endpoint into a cool-down, so every caller waits
  for it instead of each retrying into the limit. Its length is `Retry-After`
  (seconds or a date), else the `x-ratelimit-reset-requests` header (`6s`,
  `1m30s`, `250ms`), else 10 s, then 20 s, 40 s and so on for every 429 in a
  row, at most 600 s. A successful answer that says no request is left
  (`x-ratelimit-remaining-requests: 0`) with a reset also cools the endpoint
  down. The call is retried after the cool-down, up to `llm.max_rate_retries`
  times. A cool-down longer than `llm.max_rate_wait_seconds` is not waited for:
  the call fails with "The endpoint asked for a wait longer than ...", or moves
  to a fallback model. With a window wait and a cool-down at once, only the time
  until the cool-down ends is compared with the key. The 30-second cap on `Retry-After` of other errors does not apply to
  429.
- Every wait stops when the call is canceled. The result reports the waits
  (`metadata.rate_limit_waits`, `metadata.rate_limit_wait_ms`) and, from one
  second of waiting on, adds the note "The LLM endpoint's rate limit delayed
  this run by N s." ([Troubleshooting](troubleshooting.md#rate-limit-and-fallback-notes)).
- When the server instructions and the `multi_perspective_review` prompt are
  shown to a client and `requests_per_minute` or `max_concurrent_requests` is
  set, they add that parallel focused reviews are queued instead of failing.

### Fallback models

Up to three fallback models are tried in order when a model call cannot be
served by the current one. The run stays on the model it fell back to, so a
later call of the same run does not ask the failing model again; a new run
starts at the primary model again. In the file:

```toml
[[llm.fallback]]
model = "second-model"
# base_url = "https://fallback.example/v1"   # default: llm.base_url
# context_window = 128000                    # default: read from the endpoint (like llm.context_window)
# max_output_tokens = 4096                   # default: llm.max_output_tokens
# reasoning_effort = "low"                   # default: unset
# timeout_seconds = 120                      # default: llm.timeout_seconds
```

In the environment, for entry N from 1 to 3:
`REVIEW_MCP_LLM_FALLBACK_<N>_MODEL`, `_BASE_URL`, `_CONTEXT_WINDOW`,
`_MAX_OUTPUT_TOKENS`, `_REASONING_EFFORT` and `_TIMEOUT_SECONDS`, and the secret
`REVIEW_MCP_LLM_FALLBACK_<N>_API_KEY`. Entry N exists when the file has it or
`..._<N>_MODEL` is set; every set variable replaces that field of the file's
entry N. Entries are numbered from 1 without gaps. Temperature, seed, the
retries and the rate limits are the primary's.

**Which key a fallback sends.** A fallback uses its own
`REVIEW_MCP_LLM_FALLBACK_<N>_API_KEY` when it is set. Without one it uses the
primary key **only when its base URL is the primary's** (compared after
normalising scheme, host, default port and a trailing slash); a fallback on
another host without its own key sends **no key at all**, and the request has
no `Authorization` header. The primary key is never sent to another host.
`server_info` shows, for each fallback, the model, the redacted base URL, the
key source (`own`, `primary` or `none`) and the context window.

**Serve mode.** With `serve.llm_key_source = header` every caller sends the
LLM key, so `REVIEW_MCP_LLM_API_KEY` must be unset, and so must every
`REVIEW_MCP_LLM_FALLBACK_<N>_API_KEY`: a server-side fallback key would let
every caller spend the operator's key. Such a fallback uses the caller's key on
the primary's base URL and no key on any other host. With
`llm_key_source = server` the fallback keys may be set ([Serve mode](serve.md)).

**When a call moves to the next model:**

- the model's own retries are used up for a server error (5xx), a timeout or a
  transport failure (the endpoint is unreachable);
- it stays rate limited: its rate-limit retries are used up, or the needed wait
  is longer than `llm.max_rate_wait_seconds` (waits first, then falls back); only the endpoint's cool-down counts, not review-mcp's own queue;
- its answer is still unusable after the one re-ask that the tools make
  (`pr_review`, `pr_improve`, `pr_describe`): the next model gets the original
  prompt once.

**When it does not move on**, and the call ends with the error as before:

- authentication (`llm_auth`, HTTP 401 or 403);
- a refused request (`llm_bad_request`);
- not found (`llm_not_found`): a wrong model name is a configuration error and
  must surface, not hide behind a fallback;
- a request too long for the model's context window (`llm_context_too_long`);
- an unexpected response (`llm_protocol`);
- a canceled call or an expired deadline.

**Size.** Before a fallback is asked, the prompt's estimate plus its maximum
output must fit its context window; otherwise that fallback is skipped for the
call, and the result says so. A fallback whose window is not set is read from
its endpoint like `llm.context_window` ([Context window
auto-detection](#context-window-auto-detection)).

The result reports the models: `metadata.model` is the model that served the
last call, `metadata.models_used` the models that served calls, in order, and
`metadata.fallback_calls` the calls a fallback served; notes say why
([Troubleshooting](troubleshooting.md#rate-limit-and-fallback-notes)). When
every model fails, the error is the first model's, with "All N fallback models
failed as well."

### Repository config

Each reviewed repository can tune its own reviews with a `.review-mcp.toml`
at its root, read at the pull request's target branch. Only an allow-list of
keys is read: no host, model, limit or secret
([Repository config](repo-config.md)). Two operator keys control it; the
repository file cannot set them.

| Key | Environment variable | Default | Meaning |
|---|---|---|---|
| `repo_config.enabled` | `REVIEW_MCP_REPO_CONFIG_ENABLED` | `true` | Read the repository config. With `false` nothing is read, and every result says `repo_config.status` `off`. |
| `repo_config.path` | `REVIEW_MCP_REPO_CONFIG_PATH` | `.review-mcp.toml` | The file's path, relative to the repository root, without `..`, `.` or empty segments. |
| `review.docs_style` | `REVIEW_MCP_REVIEW_DOCS_STYLE` | (empty) | The documentation style of the [`docs` focus](review.md#the-docs-focus), at most 1,000 bytes; a repository config may replace it. |

`server_info` shows all three.

### Issue compliance

`pr_review` checks the pull request against the issue it implements, and
`pr_describe` uses that issue as context
([Issue compliance](review.md#issue-compliance)). The issue comes from the
`issue_context` argument or from the provider's own issue tracker (GitHub,
Gitea, GitLab), found through explicit references in the pull request.
review-mcp never contacts another tracker and holds no tracker credential.
`server_info` shows `reads issues` for each provider that can read them.

| Key | Environment variable | Default | Meaning |
|---|---|---|---|
| `review.issue_compliance` | `REVIEW_MCP_REVIEW_ISSUE_COMPLIANCE` | `true` | Use the issue when there is one. With `false` nothing is read or shown and `issue_context` is ignored. A repository config may set it. |
| `review.max_issue_tokens` | `REVIEW_MCP_REVIEW_MAX_ISSUE_TOKENS` | `2000` | Token budget of all issue text of one prompt together, 0 to 20,000; 0 turns issue compliance off. |

### Related pull requests

`pr_review`, `pr_ask` and `pr_info` take a `related` argument: up to four pull
requests (or repositories, below) read as context, on any configured provider
([Related pull requests](review.md#related-pull-requests-and-repositories)). Each is read with
the token of **its own** provider, so every provider you name a related pull
request on needs its section and a token that can read that repository; a
related pull request on a host that is not configured is never contacted.
Nothing is ever posted to a related pull request, so a read token is enough
for them.

| Key | Environment variable | Default | Meaning |
|---|---|---|---|
| `related.max_tokens` | `REVIEW_MCP_RELATED_MAX_TOKENS` | `8000` | Token budget of all related pull requests of one prompt together, shared equally, 0 to 40,000; 0 turns them off (the argument is accepted, nothing is read, one note). `pr_info` reads no diff and is not limited by it. |
| `related.contract_globs` | `REVIEW_MCP_RELATED_CONTRACT_GLOBS` | (empty) | Globs (as `ignore.glob`) of paths shown first in a related pull request, as API descriptions. A repository config adds to the list. |
| `related.max_identifiers` | `REVIEW_MCP_RELATED_MAX_IDENTIFIERS` | `20` | Most changed identifiers searched in a related repository, 1 to 50. Operator-only. |
| `related.from_repo_config` | `REVIEW_MCP_RELATED_FROM_REPO_CONFIG` | `false` | Read the related repositories that the reviewed repository's `.review-mcp.toml` declares (`[[related.repos]]`), only when the call passes no `related`. Operator-only; off by default because the server then reads another repository on the strength of a file in the repository. |

`related` also takes **repository** URLs, optionally with a branch
([Repository items and consumer impact](review.md#repository-items-and-consumer-impact)).
A repository is read only with `context.repo.enabled` (stdio only), by git,
with the token of its provider; a repository item costs a shallow fetch and a
text search per run. Content of a related repository can reach a comment on
the reviewed pull request, whose audience can differ: name only repositories
whose content you are willing to show there.

### Repository tools

With [repository context](repo-context.md) on, the model can read the pull
request's head commit itself through the endpoint's tool calling
([Repository tools](repo-context.md#repository-tools)). Off by default; the two
keys are operator-only (a repository config cannot set them) and need
`context.repo.enabled`.

| Key | Environment variable | Default | Meaning |
|---|---|---|---|
| `context.repo.max_tool_rounds` | `REVIEW_MCP_CONTEXT_REPO_MAX_TOOL_ROUNDS` | `0` | The most rounds in which the model may call tools, 0 to 6; 0 turns them off. Each round is one model call through the rate limiter, so a review takes more time and tokens. With rounds above 0 and `context.repo.enabled` off, nothing is read and `server_info` shows a warning. |
| `context.repo.max_tool_tokens` | `REVIEW_MCP_CONTEXT_REPO_MAX_TOOL_TOKENS` | `6000` | Token budget of all tool results of one call together, 500 to 40,000. Each result is also cut at 2,000 tokens. |

The endpoint must support tool calling. If it does not, the run is single-pass
with a note, and `server_info` shows `refused` for that model.

### `context_window` and `max_output_tokens`

- Set `context_window` to the window your server really runs, not the largest
  the model supports. An endpoint started with a smaller limit answers
  `the request is too long for the model's context window`.
- Set `max_output_tokens` to what you want an answer to be allowed, well below
  the window. It reserves room for the answer: the diff gets
  `context_window - (max(max_output_tokens, 1000) + 500) - prompt tokens`.
  When you set it, it is also sent to the endpoint as the completion limit.
- Too small a window is not an error until nothing is left for the diff; large
  pull requests are then reviewed in several parts, and files left after
  `review.max_chunks` parts are listed as omitted. `diff.max_tokens` can lower
  the diff budget further.

The derivation is in [Getting started](getting-started.md#how-the-context-window-shapes-the-diff-budget)
and [Reviewing pull requests](review.md).

### `diag review --dry-run`

With the environment set, check a real pull request without calling the model:

```sh
review-mcp diag review https://your-gitea.example/octo/demo/pulls/7 --dry-run
```

It prints a JSON report with the prompt, diff and request token estimates, the
budget (soft and hard limits), and the coverage: which files made it in and
which were omitted or skipped, and why. Nothing is sent to the LLM. If files
you care about are omitted, raise `context_window` (if your model really has
room), add `ignore.*` rules for generated files, or review a smaller pull
request. `diag diff` shows the exact diff text. See
[Reviewing pull requests](review.md).

## 4. Register with an MCP client

Each client below starts review-mcp as a local process over stdio. The server
command is `npx -y @nevzatcirak/review-mcp`; if you installed a binary, use its
path (and the argument `stdio` is optional, since it is the default).

The configuration needs the LLM and provider variables from steps 2 and 3. To
keep tokens out of files, prefer the client's environment-variable references
where it has them; the examples say where it does.

<!--
  Client syntax verified against each client's own documentation on 2026-10-06,
  by fetching the pages:
    Claude Code: https://code.claude.com/docs/en/mcp
      - `claude mcp add [options] <name> -- <command> [args...]`; `--env KEY=value`
        must come before the server name; `--` is mandatory before the command.
      - .mcp.json supports ${VAR} and ${VAR:-default} in command, args, env, url, headers.
    opencode: https://opencode.ai/docs/mcp-servers/
      - top-level key "mcp"; a "local" server has "command" (an array), "environment", "enabled";
      - a "remote" server has "url" and "headers";
      - {env:VAR} substitution in config values.
  The generic mcpServers block is the common convention and is NOT verified
  against any one client; clients differ (see getting-started.md).
-->

### Claude Code

Add the server from the command line. `--env` flags go before the name, and
`--` separates the command:

```sh
claude mcp add review-mcp \
  --env REVIEW_MCP_LLM_BASE_URL=https://llm.example.com/v1 \
  --env REVIEW_MCP_LLM_MODEL=your-model-name \
  --env REVIEW_MCP_LLM_CONTEXT_WINDOW=32000 \
  --env REVIEW_MCP_GITEA_BASE_URL=https://your-gitea.example \
  --env REVIEW_MCP_LLM_API_KEY="$REVIEW_MCP_LLM_API_KEY" \
  --env REVIEW_MCP_GITEA_TOKEN="$REVIEW_MCP_GITEA_TOKEN" \
  -- npx -y @nevzatcirak/review-mcp
```

The shell expands `"$REVIEW_MCP_LLM_API_KEY"` when you run the command, so the
value is written into Claude Code's configuration file. To keep the secret out
of it, put the server in a project `.mcp.json`, where Claude Code expands
`${VAR}` itself when it starts:

```json
{
  "mcpServers": {
    "review-mcp": {
      "command": "npx",
      "args": ["-y", "@nevzatcirak/review-mcp"],
      "env": {
        "REVIEW_MCP_LLM_BASE_URL": "https://llm.example.com/v1",
        "REVIEW_MCP_LLM_MODEL": "your-model-name",
        "REVIEW_MCP_LLM_CONTEXT_WINDOW": "32000",
        "REVIEW_MCP_LLM_API_KEY": "${REVIEW_MCP_LLM_API_KEY}",
        "REVIEW_MCP_GITEA_BASE_URL": "https://your-gitea.example",
        "REVIEW_MCP_GITEA_TOKEN": "${REVIEW_MCP_GITEA_TOKEN}"
      }
    }
  }
}
```

Export the two variables in the environment Claude Code starts from.

### opencode

In `opencode.json` (project) or your global opencode config. A `local` server
takes the command as an array and its variables under `environment`;
`{env:NAME}` reads a variable from the environment opencode runs in:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "review-mcp": {
      "type": "local",
      "command": ["npx", "-y", "@nevzatcirak/review-mcp"],
      "enabled": true,
      "environment": {
        "REVIEW_MCP_LLM_BASE_URL": "https://llm.example.com/v1",
        "REVIEW_MCP_LLM_MODEL": "your-model-name",
        "REVIEW_MCP_LLM_CONTEXT_WINDOW": "32000",
        "REVIEW_MCP_LLM_API_KEY": "{env:REVIEW_MCP_LLM_API_KEY}",
        "REVIEW_MCP_GITEA_BASE_URL": "https://your-gitea.example",
        "REVIEW_MCP_GITEA_TOKEN": "{env:REVIEW_MCP_GITEA_TOKEN}"
      }
    }
  }
}
```

### Any other client

Most clients accept a JSON object like this one. File location, the top-level
key (`mcpServers`, `servers`, `mcp`) and whether `${NAME}` references are
expanded differ per client (not verified for any client other than the two
above); check yours, and if it does not expand references, export the variables
in the environment the client starts from instead of writing tokens in the
file.

```json
{
  "mcpServers": {
    "review-mcp": {
      "command": "npx",
      "args": ["-y", "@nevzatcirak/review-mcp"],
      "env": {
        "REVIEW_MCP_LLM_BASE_URL": "https://llm.example.com/v1",
        "REVIEW_MCP_LLM_MODEL": "your-model-name",
        "REVIEW_MCP_LLM_CONTEXT_WINDOW": "32000",
        "REVIEW_MCP_LLM_API_KEY": "${REVIEW_MCP_LLM_API_KEY}",
        "REVIEW_MCP_GITEA_BASE_URL": "https://your-gitea.example",
        "REVIEW_MCP_GITEA_TOKEN": "${REVIEW_MCP_GITEA_TOKEN}"
      }
    }
  }
}
```

For Bitbucket Server, use `REVIEW_MCP_BITBUCKET_SERVER_BASE_URL` and
`REVIEW_MCP_BITBUCKET_SERVER_TOKEN` instead of (or next to) the Gitea pair.

### With a tracker MCP server

review-mcp reads issues from the git provider only. For an issue in another
tracker, register that tracker's MCP server in the same client next to
review-mcp. The client then fetches the issue the pull request names and
passes it to `pr_review` or `pr_describe` as `issue_context` (key, title,
description, acceptance criteria and URL). The server instructions and the
tool descriptions already tell the client to do so: "If the pull request
names an issue in a tracker you can reach (for example through a tracker MCP
server), fetch it and pass it as issue_context; review-mcp never contacts
trackers itself." When the pull request names a key such as `PROJ-123` and no
`issue_context` was given, the review's notes and `issue.detected_keys` say
which issue to pass.

review-mcp holds no tracker credential: the tracker's MCP server
authenticates on its own, in the client. For Jira, for example:

- **Jira Cloud:** Atlassian's remote MCP server.
- **Jira Data Center / Server:** a community server such as
  `sooperset/mcp-atlassian`, set to read-only mode.

Follow the server's own setup guide, give it read access only, and check that
your client may call both servers in one session. The check is the same
whichever tracker the issue came from; see
[Issue compliance](review.md#issue-compliance).

## 5. First run

Do these in order. Each step checks one more layer, and each stops at the
layer that is broken.

1. **`server_info`.** Ask your client to call the `server_info` tool. It
   reports the version, the commit, the enabled providers and the effective
   non-secret configuration; secrets show only as `set` or `unset`. If the
   status is `config_invalid`, the `problems` list says exactly what to fix;
   correct the environment and restart the server (reload the client).
2. **`diag pr`.** In a terminal with the same environment, check that your
   provider and token can reach a real pull request, without an LLM or a
   client:

   ```sh
   review-mcp diag pr https://your-gitea.example/octo/demo/pulls/7
   ```

   It prints the pull request, its files and the base strategy as JSON. Compare
   the files with the provider's web page. See
   [Troubleshooting](troubleshooting.md#check-connectivity-with-diag-pr).
3. **`pr_review` with `publish=false`.** Ask the client to review the pull
   request and not to publish: call `pr_review` with `pr_url` set and
   `publish` left at its default `false`. You get the review as markdown in
   the client; nothing is written to the pull request. Read the coverage
   section to see what was and was not reviewed. Only when you are happy with
   the result, use `publish=true` (needs the write scope).

Ask a question the same way with `pr_ask` ([Asking questions](ask.md)), get a
title, summary and files walkthrough with `pr_describe` ([Describing pull
requests](describe.md); it only reads unless you pass `publish=true`), get
scored code suggestions that were checked against the head file with
`pr_improve` ([Suggesting code changes](improve.md); it too only reads unless
you pass `publish=true`), and read comment threads with `pr_comments`. Ask which branch a pull request merges
into and who has approved it with `pr_info` ([Pull request status](pr-info.md)).

**Optional: repository context.** To show the model where the symbols a pull
request changes are used elsewhere in the project, set
`REVIEW_MCP_CONTEXT_REPO_ENABLED=true` (stdio only; needs `git` 2.31 or later;
the settings are in [Repository context](repo-context.md#enabling-it)). Reads
never fetch on any supported git: they go through a view with no promisor. Check
`server_info` for `enabled, git <version>` and the coverage section of a review
for the `Repository context` line.

**Optional: incremental review.** To review only the files that changed since
the last published review, set `REVIEW_MCP_REVIEW_INCREMENTAL=true`
(`review.incremental`, default `false`; the `incremental` argument of
`pr_review` replaces it per call). Change detection reads at most
`review.incremental_max_files` files through the provider (`REVIEW_MCP_REVIEW_INCREMENTAL_MAX_FILES`,
default 300, 1 to 5000); a larger pull request is reviewed in full, with a
note. It needs nothing beyond the read scopes, and the first review of a pull
request is always a full one published with `publish=true`
([Incremental review](review.md#incremental-review)).

## 6. When something fails

Every error review-mcp returns is one of a fixed set of sentences, each tied to
a cause and a configuration key to check. The complete table, including the
serve-mode errors, is in
[Troubleshooting: every error sentence](troubleshooting.md#every-error-sentence).
Start there with the exact sentence you received. For anything about tokens and
scopes, see [Token scopes](troubleshooting.md#token-scopes).
