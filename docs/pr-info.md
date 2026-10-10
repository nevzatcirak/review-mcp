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
| `required_approvals_note` | a fixed text whenever `required_approvals` is `null`, `0` because no rule applies, or a lower bound: "not readable with this token", "a protection pattern could not be evaluated", "no branch protection rule applies to the target branch", or (GitHub) "classic branch protection is not readable with this token; the required count may be higher" (see [Troubleshooting](troubleshooting.md#pr_info-required_approvals-notes)) |
| `mergeable` | `true`, `false`, or `null` when unknown (also `null` for a pull request that is not open) |
| `merge_blockers` | short fixed reasons, only where the provider gives structured ones |
| `review_mcp_activity` | `{overview, inline_findings}`: what review-mcp wrote as the token's user; `null` when it could not be read |
| `notes` | fixed sentences about parts that could not be read |

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
| reviewers and their states, `lastReviewedCommit` | `reviewers[]` of the same payload (read again) |
| mergeable, blockers, required approvals | `GET .../pull-requests/{id}/merge` (open pull requests only) |

Rules:

- Only `reviewers[]` is listed. People who took part but are not reviewers
  are not. `APPROVED` is `approved`, `NEEDS_WORK` is `changes_requested`,
  `UNAPPROVED` (and any other status) is `pending`.
- Every listed reviewer is `requested: true`: Bitbucket's reviewers are the
  people asked to review.
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
  "required reviews or checks are not satisfied", "the branch is behind the
  target branch", "the pull request is a draft", and "other merge check" for
  an unknown state. **`unstable` is not a blocker**: it means that only checks
  that are not required fail or are pending, and it adds the note "some checks
  that are not required are failing or pending".
- A pull request with more than 250 commits gets the note "GitHub lists at most
  250 commits of a pull request; the commits past them are not available."

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
- review-mcp never approves or requests changes, so its own activity can never
  change `approvals`.

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
GitHub: Pull requests read (and Contents and Metadata read); reading classic
branch protection needs Administration read, without which the count comes from
the rulesets alone, with a note.
The full endpoint list is in the [Setup guide](setup.md#what-each-token-needs).
