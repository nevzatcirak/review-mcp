# P9 — Partial-coverage honesty and publish verification: Specification

| | |
|---|---|
| Phase | P9 (added 2026-10-07, owner decision; in v1.0 scope, ships as `v1.0.0-rc.4`) |
| Work packages | WP-PR-9a (partial-coverage honesty) · WP-PR-9b (npm publish verification) |
| Origin | (1) On `rc.3`, a review of a large PR covered 5 of 62 changed files because of a diff budget. The client model summarised the result as "no major issues", although 57 files were never reviewed. The owner's priority, recorded here: **completeness and correctness come before speed.** (2) The `rc.3` npm publish job succeeded, but one platform package version never appeared on the registry. |
| Binding inputs | X-3 (coverage section), X-6, X-8, X-12 (overview), X-16 (jobs). |
| Builds on | `main` at `dc14e75` or later. Branch `p9-honesty`. |
| Protocol | Same as P8. One PR comment per package. Stop after each. No tags. |
| Model guidance | Sonnet for both packages. |
| Not in scope | Chunked review of large PRs. That is v1.1 (`docs/design/v1.1-repo-context.md` §6). |

## 0. Decisions (added to the decisions doc in 9a)
- **X-18 — A partial review says so first.**
  - A result is **partial** when at least one reviewable changed file was:
    - omitted by the budget;
    - clipped;
    - skipped by the provider because of its size;
    - unreadable.

    Files filtered by ignore rules or generated-file rules do **not** make a review partial. They were excluded on purpose, and the coverage section lists them.
  - A partial result leads, in every rendering and before anything else, with a fixed banner sentence. The structured result carries the counts.
  - Every "no concerns" statement is scoped to the reviewed files.

## 1. WP-PR-9a — Partial-coverage honesty (X-18)
1. **Structured result.**
   - `coverage` gains:
     - `partial` (bool);
     - `reviewed_files` (int): fully included files;
     - `total_files` (int): every changed file except those filtered on purpose;
     - `not_reviewed_files` (int): omitted, clipped, provider-skipped and unreadable files.
   - The same fields are added in `pr_review`, `pr_ask` and `job_result`, and in the output schemas.
2. **Client profile (MCP text).** When partial, the **first line**, before the `## PR Review` heading, is:
   > `**Partial review: <reviewed_files> of <total_files> changed files were reviewed. <not_reviewed_files> files were not reviewed (see Coverage); nothing is concluded about them.**`

   `pr_ask` uses the same sentence with "answer" in place of "review": "…files were used for this answer…".
3. **Provider profile (published overview).**
   - Gitea: the same sentence as a blockquote warning line directly under the heading, prefixed with ⚠️.
   - Bitbucket: the same sentence as a bold line.
   - It is also present after an in-place overview edit (X-12).
4. **Scoped "no concerns".**
   - When partial, the security and performance "No" renderings read "No security concerns identified in the reviewed files" and "No performance concerns identified in the reviewed files".
   - A run without findings reads "No key issues found in the reviewed files", not just an empty list.
5. **Tool descriptions.** Append this sentence to the `pr_review`, `pr_ask` and `job_result` descriptions:
   > "If the result says the review is partial, tell the user how many files were not reviewed and never state that those files have no issues."
6. **Note.** Partial results also add, to the notes, the hint "To review every file, raise or unset diff.max_tokens, or use a model with a larger context window." The hint mentions `diff.max_tokens` only when that cap was the limit that applied.
7. **Tests.**
   - Partial versus complete runs, for both tools and all three profiles.
   - Filtered-only runs are **not** partial.
   - A clipped file alone makes a run partial.
   - The banner is the first line (client) and comes right after the heading (overview).
   - The scoped "no concerns" wording.
   - Structured counts add up: `reviewed_files + not_reviewed_files == total_files`.
   - **[canary]** Remove the banner: the client-profile test must fail.
   - **[canary]** Count clipped files as reviewed: the accounting test must fail.
8. **Docs.**
   - `docs/review.md` and `docs/ask.md` get a "Partial reviews" section.
   - The troubleshooting entry "the review says partial" explains which setting limits coverage.
   - Add X-18 to the decisions doc.

Commit: `feat(review): lead partial reviews with a coverage banner and scope "no concerns" to reviewed files`

## 2. WP-PR-9b — npm publish verification
1. **Script change.** After publishing all packages, `npm/scripts/publish-packages.mjs` verifies each `<name>@<version>` with `npm view`.
   - It retries for up to 5 minutes, with backoff (10 s, 20 s, 40 s, …).
   - The job fails when any package version is still missing. The failure lists the missing packages and suggests checking npmjs.com for a pending staged publish or a package-level publishing requirement.
   - It never republishes.
2. **Staged state.** If the npm CLI reports that a version was staged rather than published, the step fails with the same message instead of passing. Detect this from the CLI output text; match conservatively.
3. **Tests (node:test).**
   - All present: pass.
   - One missing after the retries: fail, with the package named.
   - Present only after two retries: pass.
   - A staged-output fixture: fail.
4. **Docs.** `docs/release.md` gains "If a platform package is missing after release".

Commit: `ci: verify every npm package version on the registry after publishing`

## 3. Release
After the merge, the owner tags `v1.0.0-rc.4`. Add `CHANGELOG.md` `1.0.0-rc.4` in 9a's commit, or in a docs commit that closes the PR.

## 4. Live acceptance (appended to section H of `v1-acceptance.md`)
- **H4** With `diff.max_tokens` low enough to omit files:
  - the first line of the opencode answer source (the tool text) is the partial banner;
  - the published overview carries it;
  - the client model's reply mentions the files that were not reviewed.
