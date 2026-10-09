# Changelog

All notable changes to review-mcp are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [2.0.0-rc.2] - Unreleased

Second v2 release candidate, built on 2.0.0-rc.1 (the npm dist-tag `next`;
`latest` moves only at 2.0.0). One new tool, `pr_improve`. Nothing in the
existing tools or in the configuration is removed or renamed.

### Added

- `pr_improve` (X-27), a tool in stdio and serve mode that suggests code changes
  for a pull request with your LLM: for each suggestion the existing code, the
  improved code, a label and a score from 0 to 10 given by a second model call,
  the self-review. The prompts are adapted from PR-Agent's `/improve` prompts
  (see `NOTICE`). The guide is [Suggesting code changes](docs/improve.md).
  - Arguments: `pr_url`, `output_language`, `publish` (default `false`) and, in
    stdio mode, `wait_seconds`. The structured result has `suggestions`,
    `coverage`, `notes`, `metadata` and, when publishing was requested,
    `publish`; it is in the output schema and in `job_result` (`pr_improve` is
    a background job in stdio mode like the other LLM tools, and counts toward
    the same job limit; the running status says "suggestion job"). Not marked
    read-only, because `publish=true` writes; not destructive. The server now has
    ten tools in stdio mode and nine in serve mode.
  - Sent to the LLM: the PR's title, today's date, source and target branch,
    description (cut to `diff.max_description_tokens`), the numbered diff (the
    form `pr_review` sends, with new-file line numbers), the discussion block
    (`review.max_discussion_tokens`) and, when enabled, the repository context.
    The self-review call gets the part's numbered diff and that part's
    suggestions, and **neither the discussion nor the repository context**. Not
    sent: commit messages, tokens.
  - Large pull requests: when the diff leaves files out and `review.max_chunks`
    is above 1, the files are improved in parts, packed as `pr_review` packs
    them. Each part asks for at most `improve.max_suggestions_per_part`
    suggestions for its own files and has its own self-review call. A failed
    part makes its files not reviewed (`model_call_failed`), as in a review.
  - Self-review (Y-9): a suggestion scoring below `improve.min_score` is dropped
    and counted in the note "N suggestions were dropped by the self-review score
    (below 7)." A suggestion that got no score is kept and shown as unscored,
    never dropped: when a part's self-review call fails ("Part I's suggestions
    were not scored (the self-review call failed)."; for one call "The
    suggestions were not scored (the self-review call failed)."), or when the
    answer does not score it ("N suggestions got no usable self-review score and
    are kept unscored."). Entries are matched to suggestions by a
    `suggestion_number` added to upstream's schema, checked against the file and
    the summary; "N self-review entries did not match a suggestion and were
    ignored."
  - Ranking: one global ranking over all parts, the scored suggestions by score
    (ties: part order, then the model's order), then the unscored ones; duplicates
    (same file, summary and existing code) are dropped on that ranking; the
    result is capped at `improve.max_suggestions`, so the cap never keeps a lower
    score from an earlier part and cuts a higher score from a later one.
  - Validation: entries without a file, a summary or the code, entries for a
    file the call was not shown, and entries whose improved code equals the
    existing code are dropped; labels are one line of at most 40 characters;
    each drop is counted in a note.
  - Verification before anchoring (Y-10): the quoted code must equal the head
    file at the lines the self-review gave (after normalising trailing white
    space, CRLF and common indentation); otherwise a unique match elsewhere in
    the file corrects the range ("N suggestion line ranges were corrected."),
    and no match (`not_found`) or several (`ambiguous`) leave the suggestion
    unverified. For a file whose head content was not fetched, a given range is
    verified through the patch when every line of it is a new-side line of the
    patch and equals the quoted code; there is no search in that mode, and
    anything else is `head_unavailable`. Unverified suggestions stay in the
    result and the overview, marked "not anchored: the quoted code was not found
    at the given lines" (or "... the head file was not available to check the
    quoted code"), and are never posted inline.
  - `coverage.model_calls` counts the suggestion calls; `metadata.llm_calls`
    counts every completion, the self-review calls and re-asks included, and
    `metadata.self_review_calls` the self-review part of it.
- Publishing `pr_improve` (X-27).
  - One overview comment whose last line is `[//]: # (review-mcp:improve:v1)`,
    found by that marker and its author and edited in place on later runs; older
    duplicates are noted and never deleted. It has a table of the suggestions
    (label, file with a line link, summary, score, status), the full text of
    those without an inline comment, the coverage and the notes.
  - An inline comment only for a verified suggestion whose whole range is on
    new-side lines of one hunk of the provider's diff. The comment is a summary,
    the explanation and a fenced `diff` block from the existing to the improved
    code (Gitea, Bitbucket Server). A native suggestion block is rendered when a
    provider has the `SuggestionBlocks` capability; no provider sets it yet, so
    this is exercised only with a fake capability, and the GitHub and GitLab
    providers will turn it on.
  - Each inline comment ends with `[//]: # (review-mcp:suggestion:<key>)`. The
    already-posted key is a hash of the file, the normalised existing code and
    the normalised improved code, **without the summary**, so a rerun whose
    model reworded the summary still recognises its own comment; a marker with
    the older key (file, summary and existing code) is recognised as well. An
    already-posted suggestion is `skipped_duplicate`.
  - Each suggestion gets an `anchor` (`status` `posted`, `skipped_duplicate`,
    `unanchorable` or `failed`, with `line`, `comment_id`, `url`, `error`), and
    the result a `publish` object (`published`, `comment_id`, `url`, `error`,
    `updated` and `inline` with the four counts).
  - If the PR's comments or the token's user cannot be read, no inline comment is
    posted (it could repeat one): the overview is posted, the suggestions that
    would have been posted are `failed`, and a note says so. Publishing never
    fails the run.
  - Needs comment write (Gitea `write:issue`) for the overview and the write
    scope for inline comments (Gitea `write:repository`); see the
    [Setup guide](docs/setup.md#2-create-tokens).
- Settings `improve.max_suggestions` (`REVIEW_MCP_IMPROVE_MAX_SUGGESTIONS`,
  default 8, 1 to 30), `improve.max_suggestions_per_part`
  (`REVIEW_MCP_IMPROVE_MAX_SUGGESTIONS_PER_PART`, default 4, 1 to 10) and
  `improve.min_score` (`REVIEW_MCP_IMPROVE_MIN_SCORE`, default 7, 0 to 10; 0 keeps
  every scored suggestion). They are shown in `server_info`.
- `review-mcp diag improve <PR_URL> [--dry-run] [--show-prompt] [--json]`. It
  never publishes.
- `provider.InlineResult.Reason` (`posted`, `unanchorable` or `failed`), set by
  both providers and checked by the contract suite. A server's refusal of one
  comment's request (a 4xx answer that is not an authentication, not-found or
  rate-limit failure, and on Gitea a 500, its answer for a position outside the
  diff) is `unanchorable`; authentication, rate-limit, transport and unknown
  outcomes are `failed`. `pr_review` ignores the field and its output is
  unchanged.
- Docs: [Suggesting code changes](docs/improve.md) and a `pr_improve` section in
  the troubleshooting guide with every note and fixed sentence.

### Changed

- The discussion block of `pr_review` now leaves out the token user's comments
  that carry the marker of **any** review-mcp tool, not only `pr_review`'s own.
  Before, the `pr_describe` comment and the `pr_improve` overview and inline
  suggestions of the same token would have been shown to the review model as
  threads written by people. The prompt changes only on a pull request that has
  such comments; the review output otherwise is unchanged.
- `job_result`, the job limit and the tool descriptions name `pr_improve` next to
  `pr_review`, `pr_ask` and `pr_describe`.
- The discussion rendering, the fingerprint and the head-content line helpers
  moved from `internal/review` to `internal/llmrun` and the Markdown helpers of
  the describe renderer to `internal/mdutil`, shared with `pr_improve`; review
  and describe output is unchanged.

## [2.0.0-rc.1]

First v2 release candidate, built on 1.1.0 (the npm dist-tag `next`;
`latest` moves only at 2.0.0). One new tool, `pr_describe`, and the groundwork
that later providers need. Nothing in the existing tools or in the
configuration is removed or renamed.

### Added

- `pr_describe` (X-26), a tool in stdio and serve mode that describes a pull
  request with your LLM: a title, the change types (`Bug fix`, `Tests`,
  `Enhancement`, `Documentation`, `Other`), a short summary and a walkthrough of
  the changed files (path, label, one-line title and summary). The prompts are
  adapted from PR-Agent's `/describe` prompts (see `NOTICE`). The guide is
  [Describing pull requests](docs/describe.md).
  - Arguments: `pr_url`, `output_language`, `publish` (default `false`),
    `publish_mode` (`comment`, the default, or `description`), `update_title`
    (default `false`) and, in stdio mode, `wait_seconds`. The structured result
    has `title`, `type`, `description`, `files`, `coverage`, `notes`,
    `metadata` and, when publishing was requested, `publish`; it is in the
    output schema and in `job_result` (`pr_describe` is a background job in
    stdio mode like `pr_review` and `pr_ask`, and counts toward the same job
    limit). Not marked read-only, because `publish=true` writes; not
    destructive.
  - Sent to the LLM: the PR's title, description (cut to
    `diff.max_description_tokens`), source and target branch, commit messages
    (numbered, cut to `diff.max_commits_tokens`) and the plain diff. Not sent:
    comments, repository context, tokens.
  - Large pull requests (Y-6): when the diff leaves files out and
    `review.max_chunks` is above 1, the files are described in parts, packed as
    `pr_review` packs them. Each part describes only its own files; one more
    call, which carries no diff, writes the title, the types and the summary
    from the parts' walkthrough. If that call fails, `title` and `type` are
    `null`, the description is the files' titles and a note says "The summary
    could not be generated; the walkthrough lists the described files." A
    failed part makes its files not described (`model_call_failed`), as in a
    review.
  - Honesty (Y-7): every changed file is described, listed under "Not
    described" with its reason, or filtered on purpose. A file the model was
    shown but did not describe is listed with the new skip reason
    `not_returned`; no placeholder entry is written for it. A partial
    description leads with "Partial description: R of T changed files were
    described. N files were not described (see Coverage)."; the walkthrough
    entry of a clipped file ends with "(partial: only part of this file was
    shown)" and still counts as not described.
  - Validation: types outside the list are dropped, walkthrough entries for a
    file the call was not shown are dropped (in parts: the part's own files),
    the first entry of a path wins, labels are one line of at most 40
    characters; each is counted in a note.
  - `coverage.model_calls` counts the calls with a diff; `metadata.llm_calls`
    counts every completion, the summary call and re-asks included.
- Publishing `pr_describe` (X-26).
  - `publish_mode=comment`: one comment whose last line is
    `[//]: # (review-mcp:describe:v1)`, found by that marker and its author and
    edited in place on later runs; older duplicates are noted and never
    deleted.
  - `publish_mode=description`: the pull request description gets a region
    between `[//]: # (review-mcp:describe:start)` and
    `[//]: # (review-mcp:describe:end)`, appended after the author's text and one
    blank line on the first run and replaced on later runs. The author's text is
    never changed (byte for byte, CRLF and trailing spaces included). To remove
    the region, delete both marker lines and everything between them. Marker
    lines inside a fenced code block count. A description with a damaged region
    is refused and nothing is written.
  - `update_title=true` replaces the title with the generated one. On Gitea the
    default draft prefixes `WIP:` and `[WIP]` are kept in front of the new title
    when the pull request is a draft, and a generated one is removed, so the
    draft state does not change.
  - Concurrent edits are never overwritten: the pull request is read again right
    before the write, the region is recomputed once if the description changed
    (on Bitbucket Server a version conflict uses the same retry), and a second
    change ends with "The pull request description changed while it was being
    updated; nothing was written."
  - Bitbucket Server updates a pull request with a full `PUT`; review-mcp sends
    the reviewers (by name) and the draft flag back and compares the reviewers
    and their states before and after, with a note when one changed or could not
    be checked.
  - When nothing was described, description mode writes nothing ("Nothing was
    described, so the pull request description was not changed."); comment mode
    publishes the coverage.
  - Needs write access to the pull request on Gitea and Bitbucket Server; see
    the [Setup guide](docs/setup.md#2-create-tokens).
- `review-mcp diag describe <PR_URL> [--dry-run] [--show-prompt] [--json]`. It
  never publishes.
- Provider contract suite (X-24). `internal/provider/contract` holds the tests
  every provider must pass (metadata, file list and change types, hunks,
  threads and replies, ownership check on edit, inline results, review status,
  line URLs, error classes with no token or server text in an error, and the
  pull request update), run against fake servers for Gitea and Bitbucket
  Server. A new provider adds only a fixture. A guard fails the build if a
  production package imports it.
- Nested namespaces and capabilities (X-25). `PRRef.Namespace` may hold several
  segments, and what a provider does is driven by `Capabilities`, never by its
  name:
  - New capabilities: `SuggestionBlocks`, `QuickActions`,
    `InlineThreadResolution` and `GeneralThreadResolution` (resolution is split
    by thread kind), and `DescriptionEdit`. Gitea: inline resolution and
    `DescriptionEdit`; Bitbucket Server: both resolution flags and
    `DescriptionEdit`; no provider has the others yet.
  - Slash sanitisation: with `QuickActions`, every published body (overview,
    inline, reply, new comment, describe) gets a space in front of a line that
    starts with `/`. No provider sets it, so published output is unchanged;
    `pr_ask` keeps sanitising on every provider.
  - Nested namespaces are escaped per segment in clone URLs, and the
    repository-context cache stores a nested namespace as one directory with its
    segments joined by `+`.
- Docs: [Describing pull requests](docs/describe.md), a `pr_describe` section in
  the troubleshooting guide, and a note on capturing the debug log in Windows
  PowerShell (`2> file` wraps the first stderr line in a `NativeCommandError`
  record; redirect through `cmd /c` instead).

### Changed

- A Gitea or Bitbucket Server URL with an escaped slash (`%2F`) in the owner,
  project or repository segment is now refused as "the URL matches a configured
  provider but is not a pull request URL". Both URL shapes have exactly one
  segment there, and the escaped slash used to be accepted.
- `Provider.GetDiff` documents the two valid shapes of a file the limits cut
  (listed with `not_fetched_*` statuses and a patch, or skipped with
  `size_limit` or `file_limit`), and `FilePatch.Patch` documents that the
  `\ No newline at end of file` marker is present only when the host provides it
  (Bitbucket Server does not). Behaviour is unchanged.
- `job_result`, the job limit and the tool descriptions name `pr_describe`
  next to `pr_review` and `pr_ask`. The server has nine tools in stdio mode and
  eight in serve mode (the serve guide said six).
- The Provider interface gains `UpdatePullRequest` (Gitea `PATCH`, Bitbucket
  Server `PUT`), and the overview-marker logic shared by `pr_review` and
  `pr_describe` moved to `internal/llmrun`; review output is unchanged.

## [1.1.0]

Second stable release. The code is the same as `1.1.0-rc.1`; the release
candidate was validated in use (acceptance record #15, in-use mode). The
sections below list everything that went into 1.1.0 since 1.0.0.

### Highlights

- Large pull requests are reviewed in parts, up to `review.max_chunks` model
  calls, and the answers are merged into one review; listed deletions count as
  reviewed.
- `pr_info` reports the target branch, the human reviewers and approvals,
  required approvals and merge blockers, keeping review-mcp's own activity
  apart.
- Optional repository context (stdio only, off by default) gives the model
  uses of the changed symbols elsewhere in the repository, from a
  credential-safe local cache.
- `pr_ask` always includes the files a question names.

## [1.1.0-rc.1]

First v1.1 release candidate, built on 1.0.0. Three additions:

- Large pull requests no longer lose most of their files: the files that do not
  fit one model call are reviewed in further calls, up to a configurable number,
  and the answers are merged into one review.
- `pr_info` answers which branch a pull request targets and who has really
  reviewed or approved it, keeping review-mcp's own reviews apart.
- Optional repository context (stdio only, off by default): uses of the changed
  symbols elsewhere in the repository are found in a credential-safe local cache
  and given to the model as read-only context.

### Added

- Reviews in parts (X-19). When the diff does not hold every reviewable file,
  `pr_review` reviews the rest in further model calls ("parts") and merges the
  answers.
  - `review.max_chunks` (`REVIEW_MCP_REVIEW_MAX_CHUNKS`, default 8, 1 to 32)
    sets the most parts; `1` reviews in one call, as in v1.0.
  - `review.max_total_findings` (`REVIEW_MCP_REVIEW_MAX_TOTAL_FINDINGS`,
    default 10, at most 50) caps the merged findings; when set it must be at
    least `review.max_findings`, and the cap is the larger of the two. Findings
    beyond it are counted in a note.
  - The parts run one after another. Each has its own timeout, YAML repair and
    re-ask, and a line in the prompt that says which part it is; a part that
    fits in full is sent with extended context. Progress reports
    `calling model (part I of N)`; in stdio mode a long review becomes a
    background job as before.
  - Merging: a finding an earlier part already returned is dropped (by its
    fingerprint); the effort is the highest; tests count if any part found
    them; security and performance concerns are joined, prefixed with the part
    when more than one part has one. "No concerns" is shown only when every
    part said so: a part that did not answer leaves the field empty, with a
    note naming the part.
  - A part whose model call fails does not fail the review: its files are
    skipped with reason `model_call_failed`, the review is partial and
    publishable, and a note says "Part I of N failed (<class>); its files were
    not reviewed." with a fixed class, never the error text. Only when every
    part fails does the review fail, with the first part's error.
  - A file too large for a part of its own under `diff.large_patch_policy =
    "skip"` is skipped with reason `too_large`.
  - `coverage` in `pr_review`, `pr_ask` and `job_result` (and in every output
    schema) gains `model_calls` and `failed_parts`. The coverage section says
    "Reviewed in N model calls." when there was more than one, in the tool
    text and the published overview.
  - The partial hint names `review.max_chunks` when every allowed part was
    used and files were still left.
- `pr_ask` admits the files the question names first (X-21): a changed file
  whose full path, or base name of at least 5 characters, appears in the
  question as a whole word (case-sensitive) goes before the others. Filters
  still apply. `pr_ask` keeps one model call.
- Repository context (X-22), opt-in and stdio only. With
  `context.repo.enabled` (`REVIEW_MCP_CONTEXT_REPO_ENABLED`, default false),
  `pr_review` and `pr_ask` fetch the pull request head into a local cache with
  the system `git` (2.31 or later), search where the symbols the diff changes
  are used, and add the best uses to the prompt as one budgeted block. It is a
  startup error in serve mode.
  - Nine `context.repo.*` settings: `enabled`, `cache_dir`, `idle_days` (7),
    `max_cache_mb` (2048), `max_repo_mb` (500), `fetch_timeout_seconds` (60),
    `max_symbols` (20), `max_hits_per_symbol` (5) and `max_tokens` (2000, 200
    to 16000), all in `server_info`.
  - The token reaches `git` only in the environment of the fetch process, never
    in an argument, on disk or in a log; `git` gets an allowlisted environment
    and an empty `HOME`, so your own git configuration is not read; the clone
    URL is pinned to the configured provider and checked against `insteadOf`;
    searches run offline.
  - The block comes after the discussion and before the diff and yields to the
    diff. With `review.max_chunks` above 1 its budget is reserved up front, so a
    review that fitted one call can become two parts (counted in
    `coverage.model_calls`); each part gets the symbols of its own files and its
    own block, and uses in files another part reviews are marked.
  - `coverage.repo_context` (`status`, `reason`, `symbols`, `references`,
    `files`) in every output schema, and a `Repository context` line in the
    coverage section when it is on. Failures are notes with a fixed reason
    (`auth`, `not_found`, `timeout`, `too_large`, `sha_mismatch`, `redirect`,
    `git_failed`, `git_unavailable`, `busy`, `cache_unusable`, `unsupported`,
    `budget`, `nothing_to_review`), never a failed review. The progress stage
    `fetching repository context` precedes `preparing diff`.
- `review-mcp diag cache [--prune]` lists and sweeps the repository cache.
- `diag review --repo-context=on|off` overrides `context.repo.enabled` for one
  run; `--json` prints the structured result; `--dry-run` also shows the
  block's tokens and the symbols searched.
- `tools/evalrepo`, an evaluation harness (not shipped): reviews a list of pull
  requests without and with repository context and writes both results and a
  blind rating sheet (shuffled with a printed seed, an opaque sample id instead
  of the mode, the mapping in `key.csv`) under a directory you give it, never
  into the cache.
- Docs: [Repository context](docs/repo-context.md), and sections in the review,
  ask, serve and troubleshooting guides.
- `server_info` lists the two new `review.*` settings.
- `pr_info` (X-23), a read-only tool (stdio and serve; no LLM call, a read token
  is enough) that answers which branch a pull request merges into and who has
  reviewed or approved it. It returns the title, author, state, draft flag,
  source and target branch, head and merge-base revisions, the human
  `reviewers` (state, requested, stale, time), `approvals` counted over them,
  `required_approvals`, `mergeable` with `merge_blockers`, and
  `review_mcp_activity`.
  - review-mcp's own marked overview, inline findings and AI reviews are
    reported under `review_mcp_activity` and never count as reviewers or
    approvals; a plain review by the same account does.
  - Gitea: the latest non-dismissed approval or changes request of each user
    decides, a requested reviewer without a review is `pending`, `stale` comes
    from the review. Bitbucket Server: `reviewers[]` statuses, `stale` from
    `lastReviewedCommit`, merge status and fixed blockers from the merge
    endpoint (an unknown veto is "other merge check", never server text).
  - `required_approvals` is a number, `0` with the note "no branch protection
    rule applies to the target branch", or `null` with the note "not readable
    with this token" or "a protection pattern could not be evaluated" (Gitea
    rules are read from the rules list and matched to the branch by name or
    glob pattern); any optional part that cannot be read is `null` with a
    fixed note, and the tool fails only when the pull request itself cannot be
    read.
  - Docs: [Pull request status](docs/pr-info.md), the token checklist in the
    setup guide (Gitea needs `read:repository`, `read:issue` and `read:user`;
    reading branch protection may need repository admin and is optional).
- Docs: "Large pull requests" in the review guide, files the question names in
  the ask guide, long reviews in serve mode, and "The review took several
  minutes" and "Part I of N failed" in the troubleshooting guide.

### Changed

- A deleted file whose removed lines are left out by design and whose name is
  in the diff's list of deleted files now counts as reviewed (X-20): the model
  was told it is deleted. It is listed under "Deleted (listed by name)" and in
  `coverage.deleted_listed` (every output schema). Only deletions the budget
  cut count as not reviewed, so such pull requests are no longer reported as
  partial because of them.
- `diff.max_tokens` now makes each part smaller rather than leaving the files
  out: the files that no longer fit go to further parts.

## [1.0.0]

First stable release. The code is the same as `1.0.0-rc.4`; the release
candidates were validated in use (acceptance record #8, in-use mode). The
sections below list everything that went into 1.0.0.

### Highlights

- Tools: `server_info`, `pr_comments`, `pr_comment_reply`, `pr_comment_create`,
  `pr_review`, `pr_ask` and, over stdio, `job_result`.
- Providers: Gitea and Bitbucket Server (Data Center); any OpenAI-compatible
  LLM endpoint; per-user tokens, never logged or persisted; no telemetry.
- Reviews publish one overview comment, edited in place on later runs, and
  inline comments on the changed lines; existing discussion is taken into
  account so issues are not repeated.
- A partial review says so first and never claims anything about the files it
  did not review.
- The context window is read from the endpoint when it is not configured;
  long calls over stdio continue as background jobs.
- stdio by default; `serve` runs one shared HTTP server with credentials in
  request headers.
- Distribution: GitHub release archives with provenance and npm packages
  (`@nevzatcirak/review-mcp`) published with trusted publishing.

## [1.0.0-rc.4]

Fourth release candidate. It makes a partial review say so: a review that did
not cover every changed file no longer reads like a clean bill of health. It is
validated by the V1 acceptance run (section H4) before v1.0.0.

### Added

- Partial-coverage honesty (X-18). A result is partial when at least one
  reviewable changed file was omitted by the diff budget, clipped, skipped by
  the provider for size or file limit, or unreadable; files filtered by the
  ignore rules, binary files and files without a text change do not count.
  - `coverage` in `pr_review`, `pr_ask` and `job_result` (and in every output
    schema) gains `partial`, `reviewed_files`, `total_files` and
    `not_reviewed_files`, with `reviewed_files + not_reviewed_files ==
    total_files`.
  - A partial result leads with a fixed line, before everything else: "Partial
    review: N of M changed files were reviewed. K files were not reviewed (see
    Coverage); nothing is concluded about them." (`pr_ask`: "Partial answer: N of
    M changed files were used for this answer. ..."). In the MCP text it is the
    first line; in the published overview it is directly under the heading
    (Gitea: a warning blockquote, Bitbucket Server: a bold line) and stays after
    the overview is edited in place.
  - Notes add how to cover every file: "To review every file, raise or unset
    diff.max_tokens, or use a model with a larger context window." when
    `diff.max_tokens` is the limit that applied, else "To review every file, use
    a model with a larger context window."
  - The tool descriptions of `pr_review`, `pr_ask` and `job_result` tell the
    client model to report how many files were not reviewed and never to say
    that they have no issues.
- Docs: "Partial reviews" in the review and ask guides and "The review says
  partial" in the troubleshooting guide.

### Changed

- In a partial result every "no concerns" statement is limited to what was
  read: "No security concerns identified in the reviewed files", "No
  performance concerns identified in the reviewed files" and "No key issues
  found in the reviewed files". Complete results read as before.
- The npm publish job now verifies every package version on the registry after
  publishing (retries for up to 30 minutes) and fails, naming the packages, when
  one is missing or was staged instead of published. Nothing is republished.

## [1.0.0-rc.3]

Third release candidate. It is for slow and local models: the context window can
come from the endpoint, long reviews no longer hit the client timeout, and the
diff can be capped. It is validated by the V1 acceptance run (section J) before
v1.0.0.

### Added

- The context window from the endpoint: `llm.context_window` is now optional.
  When it is unset, review-mcp asks `GET {llm.base_url}/models` once per
  process and uses 90 % of the window the entry for `llm.model` reports (the
  first of `max_model_len`, `context_length`, `context_window`,
  `max_context_length`; 4096 at least). Training-size fields are never used. A
  set value always wins, and when the endpoint reports no window the call fails
  with a sentence naming `llm.context_window`. Ollama does not report the served
  context, so set it there. `server_info` shows the resolved value and its
  source, and `diag diff --context-window N` works without an LLM.
- Background jobs for long calls (stdio only). `pr_review` and `pr_ask` wait at
  most `wait_seconds` (argument or `llm.wait_seconds`,
  `REVIEW_MCP_LLM_WAIT_SECONDS`, default 45, 0 to 600) and then answer with a
  running status and a `job_id`; the run continues in the server, and
  `publish=true` still posts. The new seventh tool `job_result` returns the
  finished result exactly as the original tool would have, or the running status
  again. At most 4 jobs run at once; results are kept for 30 minutes, at most
  64, in memory only. A fast run returns exactly what it did before. Serve mode
  has no jobs and ignores `wait_seconds`.
- `diff.max_tokens` (`REVIEW_MCP_DIFF_MAX_TOKENS`, at least 1000, unset by
  default) caps the diff budget below the context window, for `pr_review` and
  `pr_ask`. Files that no longer fit are listed in the coverage section. `diag
  diff` and `diag review --dry-run` report which limit applied
  (`budget.limit`).

### Changed

- `llm.timeout_seconds` defaults to 300 (was 120), which local models need on
  large prompts.
- `pr_review`, `pr_ask` and `job_result` declare a root `oneOf` output schema
  (the result or the running status). If a client rejects it, a later candidate
  drops the schema in stdio and keeps the structured content (acceptance J2).

## [1.0.0-rc.2]

Second release candidate. It adds conversation-aware reviews: findings on their
lines, one overview comment that is edited in place, and awareness of what the
pull request discussion already says. It is validated by the V1 acceptance run
(section I) before v1.0.0.

### Added

- Inline findings: with `publish=true`, each finding that falls on a changed or
  context line of the diff is also posted as an inline comment on that line.
  - Gitea gets one review per run (event `COMMENT`, on the head commit);
    Bitbucket Server gets one comment per finding, with the line type computed
    from the diff and never guessed.
  - Findings that cannot be placed on a line stay in the overview, and a note
    says how many.
  - `inline_findings` (tool argument) and `review.inline_findings`
    (`REVIEW_MCP_REVIEW_INLINE_FINDINGS`, default true) switch it.
- A persistent overview: the published review is one comment per pull request
  and token user, found again by a hidden marker and by its author, and edited
  in place on later runs instead of piling up. It shows the run time, the head
  commit, a findings index that links each finding to its inline comment, and
  the coverage. A marker in someone else's comment is never adopted.
  `review.persistent_overview` (`REVIEW_MCP_REVIEW_PERSISTENT_OVERVIEW`,
  default true) switches it.
- A performance field in the review (`review.require_performance`,
  `REVIEW_MCP_REVIEW_REQUIRE_PERFORMANCE`, default true), next to security: "No
  performance concerns identified", or what the model found.
- Discussion awareness: `pr_review` reads the pull request's comment threads and
  gives them to the model as untrusted data, so it does not repeat what is
  already raised. A finding already posted by review-mcp on the same pull
  request (matched by a fingerprint) is not posted again. The overview shows how
  many points were already discussed. `review.max_discussion_tokens`
  (`REVIEW_MCP_REVIEW_MAX_DISCUSSION_TOKENS`, default 1500, 0 turns it off)
  bounds the block; its text is never logged.
- `pr_comment_create`, a sixth tool: posts a new comment, PR-level or on a
  changed line (`file` and `line`). A line that is not part of the diff is
  refused with a fixed sentence and never posted at PR level instead.
- `server_info` lists the new `review.*` settings.
- Provider write surface: the token's own user, editing a comment the token's
  user wrote (refused for anyone else's), and inline comment posting, on Gitea
  and Bitbucket Server.
- `docs/release.md` describes npm trusted publishing.

### Changed

- `pr_review` with `publish=true` posts an overview and inline comments instead
  of one flat comment; the tool description says so. The token now needs the
  scopes listed in the setup guide for reviews and comment editing, and Gitea
  tokens also need `read:user` (all marked "verify at A3").
- `pr_comment_reply` and `pr_comment_create` refuse a body with a line that
  looks like a review-mcp marker, so a comment of ours can never be mistaken
  for an overview or a posted finding.
- The README quick start shows Bitbucket Server and a locally hosted LLM
  endpoint, and the `@next` tag for release candidates.
- The comment URLs in the `pr_comment_create` and `pr_comment_reply` results
  are no longer redacted: they are returned as built from the configured base
  URL and the comment id, so Gitea links keep `#issuecomment-N` and Bitbucket
  Server links keep `?commentId=N`. Logs stay redacted.

### Fixed

- `pr_comment_reply` to an inline (review) comment on Gitea no longer fails
  with a protocol error: Gitea answers the issue-comment lookup with 204 and no
  body for such a comment, and the reply now falls back to the review comments.
- Test fixtures whose directory names differed only by case are renamed. They
  broke checkouts on macOS and Windows and are rejected by the Go module proxy.
  A CI step now fails when two tracked paths are equal under case folding.
- npm packages are published with trusted publishing (GitHub OIDC) and
  provenance; the long-lived npm token is no longer used.

## [1.0.0-rc.1]

First release candidate. It is validated by the V1 acceptance run before v1.0.0.

### Added

- Tool surface over MCP (stdio by default), five tools:
  - `server_info`: version, enabled providers and the effective non-secret
    configuration; secrets are shown only as set or unset.
  - `pr_comments`: lists a pull request's comment threads.
  - `pr_comment_reply`: replies to a comment (in the thread on Bitbucket
    Server; as a quoting PR-level comment on Gitea).
  - `pr_review`: structured review of a pull request with any
    OpenAI-compatible LLM endpoint, optionally published as a PR comment.
  - `pr_ask`: free-text questions about a pull request, grounded in its title,
    description and diff, optionally published as a PR comment.
- Gitea and Bitbucket Server (Data Center 7.0 or later) providers.
- A token-budgeted diff pipeline: file filtering, extra context, compression
  to fit `llm.context_window`, and honest coverage reporting.
- Serve mode (`review-mcp serve`): the same tools over stateless streamable
  HTTP for a shared deployment.
  - Credentials arrive per request in `X-Review-MCP-Gitea-Token`,
    `X-Review-MCP-Bitbucket-Server-Token` and `X-Review-MCP-LLM-API-Key`;
    nothing is kept after a request.
  - Optional bearer access token, TLS or an explicit insecure-HTTP opt-in,
    Host and Origin checks, a concurrency limit (`server_busy`) and `GET /healthz`.
- `diag` commands for connectivity and budget checks (`pr`, `diff`, `review`,
  `ask`, `comment`, `comments`, `reply`) that never print secrets.
- Distribution:
  - GitHub Release archives for linux, darwin and windows on amd64 and arm64,
    with `checksums.txt` and the third-party license bundle.
  - npm packages (`@nevzatcirak/review-mcp` and one optional platform package
    per target) that run without an install script.
  - `go install github.com/nevzatcirak/review-mcp/cmd/review-mcp@<tag>`.
  - A `Dockerfile` for serve mode (no published image in v1).
- Documentation: a setup guide, a serve-mode guide, and guides for review and
  ask.

[2.0.0-rc.2]: https://github.com/nevzatcirak/review-mcp/releases/tag/v2.0.0-rc.2
[2.0.0-rc.1]: https://github.com/nevzatcirak/review-mcp/releases/tag/v2.0.0-rc.1
[1.1.0]: https://github.com/nevzatcirak/review-mcp/releases/tag/v1.1.0
[1.1.0-rc.1]: https://github.com/nevzatcirak/review-mcp/releases/tag/v1.1.0-rc.1
[1.0.0]: https://github.com/nevzatcirak/review-mcp/releases/tag/v1.0.0
[1.0.0-rc.4]: https://github.com/nevzatcirak/review-mcp/releases/tag/v1.0.0-rc.4
[1.0.0-rc.3]: https://github.com/nevzatcirak/review-mcp/releases/tag/v1.0.0-rc.3
[1.0.0-rc.2]: https://github.com/nevzatcirak/review-mcp/releases/tag/v1.0.0-rc.2
[1.0.0-rc.1]: https://github.com/nevzatcirak/review-mcp/releases/tag/v1.0.0-rc.1
