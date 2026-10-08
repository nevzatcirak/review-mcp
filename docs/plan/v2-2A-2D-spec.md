# v2 phases 2A and 2D — Provider contract suite and `pr_describe`: Specification

| | |
|---|---|
| Release | `v2.0.0-rc.1` (prerelease, npm `next`); `latest` moves only at `v2.0.0` (design note Y-0) |
| Design input | `docs/design/v2-design.md` §2, §3 (Y-1…Y-7). This spec is binding; where they differ, it wins. |
| Work packages | **2A:** WP-2a (contract suite), WP-2b (`PRRef` and capabilities) · **2D:** WP-2c (describe engine), WP-2d (publishing), WP-2e (docs and polish) |
| Branch / PR | `v2-describe` from `main`, one draft PR "v2: provider contract and pr_describe". **Not merged before `v1.1.0` is tagged.** |
| Protocol | As in v1.1: one PR comment per package (report, canaries with mutation and failure text, gates, DESIGN-QUESTIONs, not verified), then stop. Gates in the `golang:1.26.0` container on the named volume with golangci-lint v2.14.0; every commit verified alone; Windows CI checked before each report. No merges, tags or publishing by the session. |
| Model guidance | WP-2a and WP-2c: Opus. Others: Sonnet. Two-round rule. |

## 0. Decisions (added to the decisions doc in the package that implements them)

- **X-24 — Provider contract suite (Y-1).**
- **X-25 — `PRRef.Namespace` may hold several segments; capabilities, not provider names, drive behaviour (Y-2, Y-3).**
- **X-26 — `pr_describe` (Y-4…Y-7)**, including the marked-region rule for editing descriptions.

## 1. WP-2a — Provider contract suite (Opus)

1. **Package** `internal/provider/contract` (test-only code under `_test.go` plus a small exported `Fixture` interface in a non-test file, so each provider package can implement it in its own tests).
   ```go
   // Fixture serves one synthetic pull request through a provider's fake
   // server and returns a Provider pointed at it.
   type Fixture interface {
       Kind() provider.Kind
       Serve(t *testing.T, pr Spec) (provider.Provider, provider.PRRef)
   }
   ```
   `Spec` describes, in provider-neutral terms: title, description, author, branches, head and base SHAs, files (path, old path, change type, hunks, head/base content, binary flag, too-large flag), comment threads (general and inline, replies, resolved flag, authors), the token user, reviewers with states.
2. **`Run(t, f Fixture)`** executes the contract cases. Minimum set:
   - metadata round trip; base strategy is one of the provider's documented values;
   - file list: modified, added, deleted, renamed with edits (old path kept), binary → skipped `binary`, too large → `size_limit`, beyond the file cap → `file_limit`;
   - hunks and content match the spec byte for byte (LF), including a file without trailing newline;
   - threads: general before inline, replies in order, resolved hidden only where the provider reports resolution (capability `ThreadResolution`, WP-2b);
   - `ReplyToComment` result `in_thread` matches the capability;
   - `EditComment` refuses a comment not written by the token user, before any write request;
   - `PostInlineComments` on an added line, a context line and an out-of-hunk line: posted, posted, unanchorable;
   - `GetReviewStatus` folding (approved, changes requested, dismissed/stale where supported, own marked review excluded);
   - `FileLineURL` for a modified and a renamed file;
   - errors: 401, 403, 404, 500 and a network failure map to the provider error classes; no error string contains the token or any response body text (the fake server embeds a sentinel in every error body).
3. **Gitea and Bitbucket Server** implement `Fixture` in their test packages and call `contract.Run`. Existing tests stay.
4. **Canaries:**
   - **[canary]** drop rename detection in the Gitea provider: only Gitea's contract run fails, naming the rename case.
   - **[canary]** let a Bitbucket error carry the response body: the sentinel case fails.
5. **Not in scope:** new providers. The suite must be written so that GitHub and GitLab (2B, 2C) only add a `Fixture`.

Commit: `test(provider): add a contract suite that every provider runs`

## 2. WP-2b — `PRRef` and capabilities (Sonnet)

1. `PRRef.Namespace` documentation: may contain `/` (nested groups). Every place that builds a URL, a cache key or a log field from it escapes per segment. Audit and test: Gitea and Bitbucket parsers still reject a `/` in their namespace (their URL shapes have exactly one segment); a unit test proves a multi-segment namespace survives the X-22 cache key and `logging.RedactURL` unchanged.
2. `Capabilities` gains `SuggestionBlocks`, `QuickActions`, `ThreadResolution`, `DescriptionEdit` (bools). Values: Gitea `ThreadResolution=true`, `DescriptionEdit=true`; Bitbucket Server `ThreadResolution=true`, `DescriptionEdit=true`; all others false.
3. **Slash sanitisation by capability.** The P5 rule (a published line starting with `/` gets a leading space) moves from `pr_ask` into the provider-neutral publishing path and applies to every published body when `QuickActions` is true. Today no provider sets it, so output is unchanged (golden check); a test with a fake capability proves it applies to overview, inline, reply and describe bodies.
4. **[canary]** apply the sanitisation only to `pr_ask` again: the capability test fails for the overview body.

Commit: `refactor(provider): allow nested namespaces and drive behaviour by capabilities`

## 3. WP-2c — `pr_describe` engine (Opus)

1. **Prompt.** Port PR-Agent's `pr_description_prompts.toml` at the revision pinned in `docs/research/pr-agent-porting-map.md` (update the map with the describe section, as was done for review and ask). Keep the YAML schema: `type` (list; values `Bug fix`, `Tests`, `Enhancement`, `Documentation`, `Other`), `description` (string of up to 4 bullets), `title`, `pr_files` (list of `filename`, `changes_title`, `changes_summary`, `label`). Drop upstream's `changes_diagram`, ticket and AI-metadata blocks, and record the deviations in the template header. NOTICE lists the adapted file.
2. **Inputs:** PR title, description, branch names, commit messages (`GetCommitMessages`, capped), and the diff from `diffpipe` (same budget rules, X-15 window, `diff.max_tokens`). No discussion block (describing the change, not the conversation). Repository context: not used.
3. **Parts (Y-6).** When the diff leaves files out and `review.max_chunks > 1`, use `PrepareChunks` exactly as `pr_review`. Each part asks for `pr_files` only (a part-mode variant of the template). Then **one reduce call** with no diff: inputs are the PR title/description/branches/commits plus the merged `pr_files` (filename, changes_title, changes_summary), and it returns `title`, `type`, `description`. If the reduce call fails: `type` and `title` are nil, `description` is the concatenated `changes_title` lines, and the fixed note "The summary could not be generated; the walkthrough lists the described files." is added. Failed parts follow X-19 (files become not described, fixed class note).
4. **Validation.** `type` values outside the enum are dropped with a note; `pr_files` entries for a path not in the reviewed set are dropped (counted in a note); duplicate paths keep the first. Labels are free text, capped at 40 characters, single line.
5. **Honesty (Y-7).** Every changed file is either described, listed under "Not described" with its reason (coverage category), or filtered on purpose. The X-18 banner applies with "described" wording: "Partial description: R of T changed files were described. N files were not described (see Coverage)."
6. **Structured result.** `title`, `type[]`, `description`, `files[]` (`path`, `title`, `summary`, `label`), `coverage` (the standard object plus `model_calls`, `failed_parts`), `notes[]`, `metadata`. Output schema registered like `pr_review`, including the stdio running-status branch (X-16 jobs) and `job_result` support.
7. **Tool.** `pr_describe` in stdio and serve; arguments `pr_url`, `publish` (default false), `publish_mode` (`comment` default, `description`), `update_title` (default false), `output_language` (as other tools). Read-only annotations when `publish=false` is the default path; the description states that publishing writes to the PR.
8. **Tests:** goldens for one-call and three-part runs (fake LLM), reduce failure, a failed part, enum and path validation, banner wording, schema test, `job_result` round trip.
   - **[canary]** let a part describe a file that was not in its diff: the validation test fails.
   - **[canary]** skip the reduce call and keep a part's title: the reduce test fails.

Commit: `feat(describe): add pr_describe with parts and a reduce step`

## 4. WP-2d — Publishing (Sonnet)

1. **`publish_mode=comment`.** One comment with marker `[//]: # (review-mcp:describe:v1)`, found and edited in place on later runs with the X-12 rule (marker plus author check; older duplicates noted, never deleted). Rendering per provider profile like the overview.
2. **`publish_mode=description`** (requires `DescriptionEdit`, else the fixed sentence "This provider does not support editing the pull request description; use publish_mode=comment."):
   - The managed region is exactly the lines from `[//]: # (review-mcp:describe:start)` to `[//]: # (review-mcp:describe:end)` inclusive.
   - First run: the region is appended after the author's text, separated by one blank line. Later runs: only the region is replaced. Text before and after it is byte-identical (test with CRLF, trailing spaces, emoji, an existing fenced block).
   - Two or more start markers, an end before a start, or a missing end: refuse with "The pull request description contains a damaged review-mcp region; fix or remove it and run again." Nothing is written.
   - `update_title=true` replaces the title with the generated one; otherwise the title is untouched.
   - **Concurrency:** re-read the PR right before writing. Bitbucket Server sends its `version`; a `409` is retried once on the fresh text. Gitea has no version: if the re-read body differs from the body the region was computed on, recompute once; if it changed again, refuse with "The pull request description changed while it was being updated; nothing was written."
   - Provider methods: `UpdatePullRequest(ctx, ref, UpdatePR{Title *string; Description *string; Version string})`, validated before any request; covered by the contract suite (WP-2a gains the case).
3. **Fixed sentences** for auth failures reuse the existing ones (the token lacks write permission).
4. **Tests:** both modes on both providers via the contract fixtures; idempotence (second run with the same answer writes the same bytes); damaged-region refusal; concurrency on both providers.
   - **[canary]** rewrite the whole description instead of the region: the byte-identity test fails.
   - **[canary]** drop the re-read: the concurrency test fails.

Commit: `feat(describe): publish as an edited comment or inside a marked description region`

## 5. WP-2e — Docs and polish (Sonnet)

- `docs/describe.md`: what is sent, parts and the reduce step, both publish modes, the region markers and how to remove them, permissions per provider, troubleshooting sentences.
- README tool row, setup scopes (`DescriptionEdit` needs write on the PR), CHANGELOG `2.0.0-rc.1` section (Unreleased until tagged), X-24…X-26 in the decisions doc, NOTICE and the porting map.
- v1.1 backlog item folded in: the PowerShell stderr note in troubleshooting.

Commit: `docs: document pr_describe and the provider contract`

## 6. In-use acceptance (`v2.0.0-rc.1`, new record issue)
- **N1** `pr_describe` without publish on a small and on a large PR (parts): every changed file is described or listed as not described; the summary reads as the whole PR.
- **N2** `publish_mode=comment` twice: one comment, edited in place.
- **N3** `publish_mode=description` on a PR with author text: the author text is unchanged byte for byte (compare before/after in the UI history), the region is replaced on the second run.
- **N4** Edit the description by hand while a run is in progress (or simulate it): nothing is overwritten.
- **N5** The contract suite runs in CI for both providers (recorded from CI, not live).
