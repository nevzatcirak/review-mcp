# v2.0.0-rc.4 — Fixes from the rc.3 acceptance: Specification

| | |
|---|---|
| Release | `v2.0.0-rc.4` (prerelease, npm `next`); phase 2F moves to rc.5 and 2C (GitLab) to rc.6 (owner decision 2026-10-11: the fixes ship as their own package) |
| Origin | In-use acceptance #19 on the throwaway PR #20 (comment "Q1–Q5 results"): defects 1–3 |
| Work packages | WP-4a (`pr_review` duplicates), WP-4b (code spans), WP-4c (blocker wording, docs, CHANGELOG) |
| Branch / PR | `v2-rc4-fixes` from `main`; draft PR "v2: fixes from the rc.3 acceptance" |
| Protocol | As for #16–#18: acknowledge every new `[architect review]` (match `## [architect review]` headings too) before each package; one report per package; canaries with mutation and failure text, rerun by the lead one at a time with a tree-hash check; every commit gated alone; Windows CI; never merge, tag or publish. |
| Model guidance | WP-4a and WP-4b: Opus. WP-4c: Sonnet. |

## 0. Decisions
- **X-29 — An inline finding is a duplicate of review-mcp's own open finding at the same place.** Replaces the text fingerprint as the primary rule (the fingerprint stays as a second, exact rule).
- **X-30 — Code spans in model text survive escaping.** Every renderer that escapes model prose keeps the model's well-formed inline code spans as code spans.

## 1. WP-4a — `pr_review` duplicates (Opus)

**Defect (#19, all providers, since v1.0):** the already-posted check compares the X-13 fingerprint (file + header + content text). A model rewords a finding between runs, so the second run posts it again. The model does not see its own earlier findings (own marked threads are kept out of the discussion block, deliberately).

1. **Own finding threads.** From the threads `pr_review` already reads, collect review-mcp's own inline finding comments: inline threads, a comment by the token's user (`provider.IsUser`) whose last line is a finding marker (`ParseFingerprintMarker`), with its path, line (and, where the provider reports it, the start of its range), its URL, and whether the thread is outdated.
2. **Overlap rule.** Before posting, a finding whose resolved anchor (`Path`, `Line`…`EndLine`, or `Line` alone) **overlaps** an own finding comment's place on the same path is `skipped_duplicate`, unless that thread is `Outdated` (the code under it changed, so a finding there may be new). Overlap is inclusive on both ends: the ranges `[a.Line, max(a.Line, a.EndLine)]` and `[c.Start or c.Line, c.Line]` share a line. Resolved threads count (a person closed it; posting again is noise). The exact fingerprint rule stays as a second check.
3. **Link to the earlier comment (backlog item "duplicate-skipped finding link", closed by this WP).** A skipped finding gets `inline_url` = the earlier comment's URL, and the overview links it like a posted one. `inline_status` stays `skipped_duplicate`.
4. **Note.** The existing duplicates note is kept; its count now includes overlap skips.
5. **`provider.Thread` start line.** If a provider can report the start line of a range comment (GitHub `start_line`), add `StartLine` (0 = single line) to `provider.Thread` and fill it for GitHub; Gitea and Bitbucket leave 0. Contract: a trait-checked case for providers that report ranges.
6. **Tests:** two runs on a fake where the second answer rewords both findings (the #19 shape: one single-line, one range 14–19 over earlier comments on 14 and 19) → `posted: 0, skipped_duplicate: 2`, both linked to the earlier comments; an outdated own thread does not suppress; another user's comment with a copied marker does not suppress; a finding on a line with no own comment is posted; a resolved own thread suppresses. Run the same test on all three provider fakes.
7. **[canary]** restore fingerprint-only matching: the reworded test posts 2. **[canary]** ignore `Outdated`: the outdated case is skipped.

Commit: `fix(review): skip a finding where review-mcp already has an open finding comment`

Not in this WP (recorded for 2F with the V-9 benchmark): a prompt block listing review-mcp's own earlier findings so the model does not spend findings on them.

## 2. WP-4b — Code spans in model text (Opus)

**Defect (#19):** model text is escaped with `mdutil.EscapeControl`/`Escape`/`EscapeGFM`, which escape every backtick, so `` `code` `` is published as `` \`code\` `` and shows literal backticks; with `EscapeGFM`, `<` inside it also becomes `&lt;`, which a code span would show verbatim. Seen in the overview, the inline comments and the description.

1. **`mdutil.EscapeKeepingCodeSpans(s string, esc func(string) string) string`.** Scan each line (spans never cross a line break) for CommonMark code spans: an opening run of N backticks closed by the next run of exactly N backticks on the same line. Each span is re-emitted with `mdutil.CodeSpan` of its content (with CommonMark's one-space stripping when the content starts and ends with a space and is not only spaces), so the delimiter stays safe; the text between spans goes through `esc`. A backtick run without a matching closer is escaped as text. Content inside a span is never HTML-escaped (code spans show entities verbatim, and GitHub/Gitea/Bitbucket do not interpret HTML inside them).
2. **Use it** wherever model prose is escaped for publishing or for the client text: `pr_review` (overview fields, key issues, inline bodies), `pr_describe` (summary, walkthrough titles and summaries, title in the body), `pr_improve` (summary, content, why), `pr_ask` answers if they go through the same escapers. Paths, URLs, labels and anything that is not model prose keep their current helpers (`Literal`, `CodeSpan`, `Inline`). Inventory every call site in the report.
3. **Safety unchanged:** text outside spans is escaped exactly as today; a span cannot open HTML, a link, a heading or a table (its content is inert, its delimiter adapts); list markers kept by `EscapeBullets` stay.
4. **Goldens:** every golden whose model text has backticks changes (expected); list them. No golden without backticks in model text may change.
5. **Tests:** spans kept; unmatched backtick escaped; a span with `<script>`, `|`, `*` and `[x](y)` inside stays inert; double-backtick spans with a single backtick inside; a span at line start that would otherwise look like a heading or list marker; CRLF; a span across a line break is not a span.
6. **[canary]** escape every backtick again: the span tests and the changed goldens fail. **[canary]** HTML-escape inside spans: the `<` case fails.

Commit: `fix(render): keep the model's inline code spans when escaping its text`

## 3. WP-4c — Blocker wording, docs, CHANGELOG (Sonnet)

1. **GitHub `blocked`:** a branch can be blocked by rules other than reviews and checks (#20 was blocked by a ruleset's update restriction). Change `provider.BlockerRequirements` to "branch rules are not satisfied (for example required reviews, required checks or other restrictions)". Bitbucket Server and Gitea keep their own blockers. Update tests and docs that quote it.
2. **Docs:** `docs/review.md` (duplicates are recognised by place, linked to the earlier comment; outdated threads do not suppress), `docs/troubleshooting.md` (the note and statuses), `docs/github.md` (the live results: body-less review accepted, `Commit suggestion` verified, the blocker wording), and the "Still to be checked" table updated with the #19 outcomes.
3. **Decisions doc:** X-29 and X-30 rows and sections with "Settled on PR (architect)" bullets.
4. **CHANGELOG:** `## [2.0.0-rc.4] - Unreleased` above rc.3: Fixed (duplicates, code spans, blocker wording), Changed (skipped duplicates link to the earlier comment).
5. **Backlog moved, not done here:** tab/space-tolerant verification for `pr_improve` quotes (#19 item 4).

Commit: `docs: document the rc.4 fixes`

## 4. Acceptance (in use, after `v2.0.0-rc.4`)
On a new throwaway PR in this repository (architect opens it with the owner's go-ahead): two `pr_review` runs with `publish=true` → the second posts nothing new and links its skipped findings; the overview, an inline comment and a `pr_describe` description show code spans as code. Repeat one review run on a personal Gitea or Bitbucket instance if available.
