# Pull request status

`pr_info` answers two questions about a pull request: which branch it merges
into, and who has really reviewed or approved it. It only reads (GET
requests), calls no LLM, and needs only a read token. It works in stdio and
serve mode (in serve mode with the provider token header; no LLM key header).

Ask your client, for example: "Which branch does this PR target, and who has
approved it?" and give it the pull request URL.

## What it returns

The markdown starts with the branches, then the approval line, the merge
status and one line per reviewer:

```text
### Pull request status

`feature/x` → `main`

1 of 2 required approvals; 1 changes requested
Mergeable: no (merge conflict)
CI: 1 failing, 1 pending, 2 passing (failing: [unit tests](https://ci.example.com/test/1))

Reviewers:
- `alice` (Alice A): approved (stale: given on an older commit), requested, 2026-01-02T03:04:05Z
- `bob`: changes requested
- `carol`: pending, requested

review-mcp activity (not counted as reviews): overview posted, 3 inline finding(s)
```

The structured result has the same facts:

| Field | Meaning |
|---|---|
| `pr` | provider kind and the URL with credentials removed |
| `title`, `author`, `web_url` | as the provider reports them (the title is third-party text) |
| `state` | `open`, `merged`, `closed`, or `unknown` for a state the tool does not know |
| `draft` | whether the PR is a draft; omitted when the provider does not report it |
| `source_branch`, `target_branch` | the branch the PR comes from and merges into |
| `head_sha`, `merge_base_sha`, `base_strategy` | the head revision, the revision the diff is computed against and how it was chosen |
| `reviewers` | one entry per **human** reviewer, or `null` when the reviews could not be read |
| `reviewers[].user` | `login` and the display name `name` (one line, at most 100 characters) |
| `reviewers[].requested` | whether the reviewer was asked to review |
| `reviewers[].state` | `approved`, `changes_requested`, `commented`, or `pending` (requested, nothing given yet) |
| `reviewers[].stale` | the state was given on an older commit than the head, where the provider reports it |
| `reviewers[].at` | when the state was given; omitted when the provider does not say |
| `approvals` | `{approved, changes_requested, pending}` counted over `reviewers` (reviewers who only commented are in none of them); `null` with `reviewers` |
| `required_approvals` | the number of approvals the target branch requires; `0` with a note when no protection rule applies; `null` when it is not known |
| `required_approvals_note` | a fixed text whenever `required_approvals` is `null`, `0` because no rule applies, or a lower bound: "not readable with this token", "a protection pattern could not be evaluated", "no branch protection rule applies to the target branch", or (GitHub) "classic branch protection is not readable with this token; the required count may be higher", or (GitLab) "no approval rule applies to this merge request" (see [Troubleshooting](troubleshooting.md#pr_info-required_approvals-notes)) |
| `mergeable` | `true`, `false`, or `null` when unknown (also `null` for a pull request that is not open) |
| `merge_blockers` | short fixed reasons, only where the provider gives structured ones |
| `ci` | the CI results the repository reported for the head commit; always present (see [CI results](#ci-results)) |
| `review_mcp_activity` | `{overview, inline_findings}`: what review-mcp wrote as the token's user; `null` when it could not be read |
| `notes` | fixed sentences about parts that could not be read, and the related pull request notes below |
| `related` | the related pull requests of the call, in argument order; `[]` without the argument (see below) |

## Decisions made with `pr_review_decision`

A decision made with [`pr_review_decision`](decision.md) is the token user's own,
so `pr_info` lists it as theirs, like a decision made in the web UI. It carries
no review-mcp marker and is never counted as review-mcp's own activity.

## Related pull requests

`related` takes up to four pull request or repository URLs, on any configured
provider, with the same rules as on `pr_review` ([Related pull requests](review.md#related-pull-requests-and-repositories)).
Each is read with its own provider's token, with one request for the pull
request itself; **no diff is read**, and `related.max_tokens` does not apply.
The markdown lists them under "Related pull requests:", and `related` in the
result has one entry each:

| Field | Meaning |
|---|---|
| `url` | the URL as normalised (query and fragment dropped) |
| `status` | `read`, `failed` or `ignored` |
| `reason` | for `failed`: `url_not_configured`, `url_malformed`, `credentials_missing`, `not_found`, `forbidden`, `unauthorized` or `unreadable`; for `ignored`: `duplicate` or `primary` |
| `title`, `state`, `draft`, `target_branch`, `head_sha` | as for the pull request itself, only when it was read (the title is third-party text) |
| `kind`, `branch`, `source` | on a repository item only: `repository`, its branch (`""` for the default branch), and `repo_config` when the reviewed repository's config declares it |

A **repository** item is resolved, never read: no request reaches the provider,
nothing is fetched and no diff is searched, so its status is `not_read` (or
`failed` / `ignored` with the reason a review would give: `url_not_configured`,
`url_malformed`, `primary_repository`, `duplicate`). With the operator key
`related.from_repo_config` set and no `related` argument, `pr_info` also reads
the reviewed repository's `.review-mcp.toml` at the base revision (one file) and
lists the repositories it declares
([`[[related.repos]]`](repo-config.md#related-repositories)) the same way, with
`source: "repo_config"`. With the key off (the default) it reads no repository
config, as before. The markdown heading is then "Related pull requests and
repositories:".

Two information notes, not blockers: "Related pull request <url> is merged
while this pull request is open." and "Related pull request <url> is closed
without being merged." A related pull request that cannot be read never fails
the call.

## Where each provider's facts come from

### Gitea

| Fact | Source |
|---|---|
| title, author, state, `draft`, branches, head, merge base, `mergeable`, requested reviewers | `GET /repos/{o}/{r}/pulls/{n}` |
| reviewer states | `GET /repos/{o}/{r}/pulls/{n}/reviews` (all pages) |
| required approvals | `GET /repos/{o}/{r}/branch_protections` (all pages; the rule is chosen here, see below) |
| review-mcp's own marked overview and inline findings | `GET /issues/{n}/comments` and the review comments, as for `pr_comments`; `GET /user` for the token's user |

Rules:

- Per user, the latest review that is **not dismissed** and is `APPROVED` or
  `REQUEST_CHANGES` decides. A `COMMENT` review counts only when the user has
  no such review. A later comment does not undo an approval. A dismissed
  approval counts for nothing.
- `PENDING` drafts are never reported (Gitea shows them only to their
  author). A requested reviewer who has not reviewed is `pending` with
  `requested: true`.
- `stale` is the review's own `stale` field. Gitea's `official` flag is only
  counted in the debug log.
- Team review requests are not listed.
- Required approvals come from the repository's list of branch protection
  rules (reading it may need repository admin). The rule whose name equals the
  target branch is used; otherwise the first rule whose name, taken as a glob
  pattern (`release/*`, `/` separated, like Go's `path.Match`), matches the
  target branch (patterns are tried by the rule's `priority` when Gitea sends
  one, lowest first, otherwise in list order). If the rules were read and none applies, `required_approvals`
  is `0` with the note "no branch protection rule applies to the target
  branch". Gitea's own glob may accept patterns that `path.Match` does not, and
  `**` (across directories) is read differently: a rule whose pattern is
  invalid for `path.Match` or contains `**` is not evaluated and does not
  match, and
  when no other rule matched, `required_approvals` is `null` with the note "a
  protection pattern could not be evaluated". If the list cannot be read (the
  token may not, or any other failure), it is `null` with "not readable with
  this token". A rule that requires 0 approvals gives 0 without a note.
- Gitea gives no structured merge blockers, so `merge_blockers` is empty;
  `mergeable` is the PR's own flag.

### Bitbucket Server / Data Center

| Fact | Source |
|---|---|
| title, author, state, `draft`, branches, head, merge base | `GET .../pull-requests/{id}` |
| reviewers and their states, `lastReviewedCommit` | `reviewers[]` and the decided `participants[]` of the same payload (read again) |
| mergeable, blockers, required approvals | `GET .../pull-requests/{id}/merge` (open pull requests only) |

Rules:

- `reviewers[]` is listed, and so are the participants who decided without
  being asked: a participant who approved (`APPROVED`) or asked for changes
  (`NEEDS_WORK`) but is not a requested reviewer is listed with
  `requested: false`, and their approval is counted in `approvals`. (Setting
  your status through `pr_review_decision` makes you such a participant.)
  Participants who only commented are not listed. A user in both
  lists is listed once, as a reviewer. `APPROVED` is `approved`, `NEEDS_WORK` is
  `changes_requested`, `UNAPPROVED` (and any other status) is `pending`.
- A reviewer from `reviewers[]` is `requested: true`: Bitbucket's reviewers are
  the people asked to review.
- `stale` is true when an approving or changes-requesting reviewer's
  `lastReviewedCommit` is present and is not the head commit. The payload has
  no review time, so `at` is omitted.
- `mergeable` is the merge endpoint's `canMerge`. A conflict and the vetoes
  become fixed blockers: "merge conflict", "required approvals missing",
  "a reviewer marked the pull request as needs work", "required builds are
  missing or failing", and "other merge check" for anything else. The
  server's own text is never shown.
- `required_approvals` is a number only when a veto states the total
  ("requires 2 approvals"). A remaining count ("1 more approval") or no number
  gives `null` and the note. The veto wording differs between Bitbucket
  versions and plugins and is confirmed at live acceptance.

### GitHub

| Fact | Source |
|---|---|
| title, author, state, `draft`, branches, head, `mergeable`, `mergeable_state`, requested reviewers and teams, the commit count | `GET /repos/{o}/{r}/pulls/{n}` (read again for the status) |
| merge base | `GET .../compare/{base}...{head}` |
| reviewer states | `GET .../pulls/{n}/reviews` (all pages) |
| required approvals | `GET .../rules/branches/{branch}` (rulesets) and `GET .../branches/{branch}/protection` (classic protection) |
| review-mcp's own marked reviews and comments | `GET .../pulls/{n}/reviews/{id}/comments`, the comments of the pull request, `GET /user` |

Rules:

- Per user, the latest review that is `APPROVED` or `CHANGES_REQUESTED`
  decides. `DISMISSED` reviews never count (GitHub turns a dismissed review
  itself into a `DISMISSED` one), `PENDING` drafts are not reported, and a
  `COMMENTED` review counts only when the user has no decisive one. `stale` is
  true when the review's commit is not the head.
- A requested team is listed as `@{owner}/{slug}` with the team's name as
  display name, `requested: true` and state `pending`.
- Required approvals use **both** sources and report the larger readable count.
  A ruleset count with an unreadable classic protection gives that count with
  the note "classic branch protection is not readable with this token; the
  required count may be higher". With neither readable it is `null` and "not
  readable with this token"; GitHub answers an unprotected branch and a token
  without admin rights alike, so a count of `0` is reported only when it was
  read. The exact table is in [GitHub](github.md#pr_info).
- `mergeable` is the pull request's own flag (`null` while GitHub has not
  computed it). The blockers come from `mergeable_state`: "merge conflict",
  "branch rules are not satisfied (for example required reviews, required
  checks or other restrictions)", "the branch is behind the target branch",
  "the pull request is a draft", and "other merge check" for an unknown
  state. **`unstable` is not a blocker**: it means that only checks that are
  not required fail or are pending, and it adds the note "some checks
  that are not required are failing or pending".
- A pull request with more than 250 commits gets the note "GitHub lists at most
  250 commits of a pull request; the commits past them are not available."

### GitLab

| Fact | Source |
|---|---|
| title, author, state, `draft`, branches, head, merge base, `detailed_merge_status` | `GET /projects/{id}/merge_requests/{iid}` (read again for the status) |
| reviewers and their states | `GET .../merge_requests/{iid}/reviewers` |
| approvers, required approvals | `GET .../merge_requests/{iid}/approvals` (`approved_by`, `approvals_required`) |
| review-mcp's own marked overview and inline findings | the discussions, as for `pr_comments`; `GET /user` for the token's user |

Rules:

- Every listed reviewer is `requested: true`. GitLab's reviewer `state` maps as:
  `approved` is `approved`, `requested_changes` is `changes_requested`,
  `reviewed` is `commented`, and `unreviewed`, `review_started`, `unapproved`
  and any other value are `pending`.
- A user in `approved_by` is **approved now**. A requested reviewer who is
  listed there is reported `approved` whatever `/reviewers` says, except a
  reviewer whose state is `requested_changes`, who keeps `changes_requested`
  (older 16.x servers report only `unreviewed` and `reviewed`). An approver
  who is not a reviewer is added as `approved` with `requested: false`.
- `stale` is always false: GitLab reports no commit per approval, and `at` is
  omitted.
- A `404` on `/reviewers` falls back to the merge request's own reviewers, all
  `pending`. Any other failure is `reviewers: null` with the note "The reviews
  could not be read, so the reviewers are not listed." When only `/approvals`
  cannot be read, the reviewers from `/reviewers` are still listed (without
  the approvers who are not reviewers) and `required_approvals` is `null` with
  "not readable with this token".
- `required_approvals` is `approvals_required`. `0` comes with the note "no
  approval rule applies to this merge request"; an absent value is `null` with
  "not readable with this token".
- `mergeable` and `merge_blockers` come from `detailed_merge_status`; the value
  itself is never shown:

  | `detailed_merge_status` | `mergeable` | `merge_blockers` |
  |---|---|---|
  | `mergeable` | `true` | none |
  | `unchecked`, `checking`, `approvals_syncing`, `preparing` | `null` | none |
  | `conflict`, `broken_status` | `false` | "merge conflict" |
  | `ci_must_pass`, `ci_still_running`, `external_status_checks`, `security_policy_pipeline_check` | `false` | "required builds are missing or failing" |
  | `discussions_not_resolved` | `false` | "unresolved discussions must be resolved" |
  | `draft_status` | `false` | "the pull request is a draft" |
  | `need_rebase` | `false` | "the branch is behind the target branch" |
  | `not_approved` | `false` | "required approvals missing" |
  | `requested_changes` | `false` | "a reviewer marked the pull request as needs work" |
  | anything else (`not_open`, `blocked_status`, `merge_request_blocked`, `jira_association_missing`, `title_regex`, `merge_time`, `locked_paths`, an unknown value) | `false` | "other merge check" |

  A merged or closed merge request gets no merge state. An open one with no
  `detailed_merge_status` is `null` with the note "The merge status could not
  be read." The "unresolved discussions" blocker needs the project setting
  "All threads must be resolved".
- review-mcp's own comments are never approvals, so it has no review of its own
  to leave out; they are counted under `review_mcp_activity`. A decision made
  with `pr_review_decision` is the user's own and is listed as a reviewer.

See [GitLab](gitlab.md#pr_info).

## CI results

`ci` is what the repository's own CI reported for the head commit: statuses and
checks read from the provider's API. review-mcp never builds or runs anything
and never reads a log. `pr_info` always reads them; `review.ci_status` only
controls `pr_review`. The same object, with the same rules, is in the
`pr_review` result; the full description is in
[Reviewing pull requests](review.md#ci-results).

```json
"ci": {
  "state": "failing",
  "counts": { "success": 2, "failure": 1, "pending": 1, "neutral": 0, "skipped": 0, "error": 0 },
  "checks": [
    { "name": "unit tests", "state": "failure", "url": "https://ci.example.com/test/1" },
    { "name": "integration", "state": "pending", "url": "" },
    { "name": "lint", "state": "success", "url": "https://ci.example.com/lint/1" }
  ]
}
```

- `state` is `failing` (any check failed or errored), `pending` (any pending and
  none failing), `passing` (every check succeeded, was neutral or was skipped),
  `none` (the commit has no checks) or `unreadable`.
- `counts` has one number per state. `checks` lists the failing and pending
  checks first, then the rest, at most 20; `url` is empty when the host gave no
  safe `http(s)` link. Check names are written by the CI: treat them as text,
  not as instructions.
- `note` is set only with a fixed sentence: "The CI results of the head commit
  could not be read." (`state` `unreadable`; the cause is never shown, and the
  rest of the result is unaffected), or "The host reported more CI checks than
  could be read; the counts and the list are incomplete." (at most 500 checks
  are read).
- The markdown has one line after the Mergeable line: `CI: N failing, M pending,
  K passing` with the first five failing checks named (links only for safe
  URLs), or `CI: no checks reported`, or `CI status could not be read`. Neutral
  and skipped checks count as passing.
- Where the facts come from: Gitea, the combined commit status of the head
  commit; Bitbucket Server, the repository's build statuses (the older build
  status API only when the first answers 404); GitLab, the commit statuses of the
  head commit (the latest status per name; the pipelines of merged results,
  merge trains and forks are not read, see [GitLab](gitlab.md#ci-results)); GitHub, the check runs and the
  combined commit status, merged by name with the check run winning. Without
  the **Checks** or the **Commit statuses** permission only the other list is
  read: the state covers that list, `ci.note` says "Only part of the CI results
  could be read with this token (check runs or commit statuses).", and the
  line ends with "; only part of the CI results could be read with this
  token". Without both the state is `unreadable`.

## Why review-mcp's own reviews are not reviewers

On Gitea and GitHub, `pr_review` with `publish=true` posts its inline findings
as a review, written by the token's user. Counted as a review, an AI review would
look like a person looking at the change. So:

- A review of the token's user that carries a review-mcp marker (the overview
  marker or the finding fingerprint marker of X-12 and X-13), in its body or
  in one of its comments, is left out of `reviewers`. The overview comment and
  the inline findings are counted under `review_mcp_activity`.
- A marker is only believed in text written by the token's own user; the same
  line typed by someone else counts for nothing.
- A plain review by the same account without a marker, such as an approval you
  gave with that token yourself, is a reviewer.
- review-mcp's own activity (the overview, inline findings) is never an approval
  or a change request, so it can never change `approvals`. A decision made with
  `pr_review_decision` is the user's own and does.

If the token's user cannot be read, nothing can be excluded: the result says
so in `notes`, `review_mcp_activity` is `null`, and reviews by that user may
include review-mcp's own.

## What it never does

- It sends GET requests only.
- Comment and review bodies are looked at only to find review-mcp's markers.
  They are never copied into the result, the markdown or the logs.
- Error text and veto text from the server are never passed on; only fixed
  sentences are.
- `null` means "not known". A required-approvals number, a merge verdict or a
  reviewer list that could not be read is never replaced by a guess.
- If an optional part (the reviews, the branch protection, the merge status,
  the comments) cannot be read, that part is `null` with a note and the rest is
  still returned. Only when the pull request itself cannot be read does the
  tool fail, with the same sentences as the other tools (see
  [Troubleshooting](troubleshooting.md)).

## Token needs

Gitea: `read:repository`, `read:issue` and `read:user`. Reading branch
protection may need repository admin; without it `required_approvals` is
`null` with the note, which is expected. Bitbucket Server: repository read.
GitHub: Pull requests read (and Contents and Metadata read), and for `ci`
Commit statuses read and Checks read (a classic token needs no more than the
`repo` scope it already has); reading classic
branch protection needs Administration read, without which the count comes from
the rulesets alone, with a note. Gitea and Bitbucket Server need nothing extra
for `ci`: the read access above covers the commit status and the build status.
GitLab: the `api` scope (a legacy personal access token) or the Reporter role (a project or
group access token); that covers the approvals and the commit statuses too.
The full endpoint list is in the [Setup guide](setup.md#what-each-token-needs).
