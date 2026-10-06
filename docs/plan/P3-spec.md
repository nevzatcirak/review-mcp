# P3 — Diff Pipeline and Token Budgeting: Specification

| | |
|---|---|
| Phase | P3 (phase-plan.md): the porting-critical heart of the project |
| Work packages | WP-PR-3a (NOTICE, embedded data, file filter) · WP-PR-3b (offline token estimator and budget) · WP-PR-3c (hunk model, context extension, renderers) · WP-PR-3d (assembly: ranking, admission, coverage) · WP-PR-3e (`diag diff` + docs) |
| Binding inputs | `docs/design/v1-design-decisions.md`: DQ-1, DQ-2, DQ-3, DQ-4, DQ-5, DQ-10, X-3, X-7, §5. Porting map §A and §B: these are the source of truth for upstream behaviour, and section C for the render formats the prompts depend on. |
| Builds on | P2 (`provider.FilePatch`, `provider.DiffOptions.Include`). Start P3 from `main` after PR #2 is merged. If PR #2 is still open, branch from `p2-providers` and rebase onto `main` once it merges. |
| Protocol | Same as P1/P2. Read the §0 preamble of `docs/plan/P1-spec.md` first. Branch `p3-diff-pipeline`, draft PR, one PR comment per package, and `[architect review]` comments are the owner's instructions. |
| Model guidance | 3c and 3d are the most porting-sensitive code in v1. Start those packages with the stronger model, not Sonnet (the model-selection rule for design-heavy parts). |

## 0. Upstream parity rules (read before any code)

1. **Upstream is the oracle, not the template.** Behaviour must match PR-Agent at `8e5a9295973b24af4b70cafd0b660a230811ef9e` (tag v0.47.0) for:
   - context extension (static only, DQ-1);
   - deletion handling;
   - every render format;
   - the admission order;
   - the soft and hard budgets;
   - the omitted-file sections.

   Clone upstream into a sibling directory (never into the repo) and read the functions named in porting map §A/§B. **Do not copy code.**
2. **Literal contract strings may be reproduced exactly:** marker and section headers such as `## File: '…'`, `__new hunk__`, `__old hunk__`, the deleted-file line, `Additional added files (insufficient token budget to process):`, `Additional modified files (insufficient token budget to process):`, `Deleted files:` and `...(truncated)`. The prompts (P4) depend on them. Every such literal lives in one Go file with a comment naming the upstream function and pointing to NOTICE.
3. **Parity oracle (strongly recommended, time-box about 1 hour):**
   - Set up a throwaway Python virtualenv **outside the repo** with the pinned upstream installed.
   - Run upstream's pure functions on our fixture inputs to produce the expected outputs:
     - `extend_patch`, with dynamic context **off**;
     - `decouple_and_convert_to_hunks_with_lines_numbers`;
     - `handle_patch_deletions` / `omit_deletion_hunks`;
     - the plain render.
   - Commit those outputs as golden files under `testdata/upstream/` with a README naming the commit and the command used. They are MIT data and are attributed through NOTICE.
   - If the setup does not work within the time box, write goldens by hand from reading upstream, say so in the report, and mark those goldens "hand-derived".
4. **Deliberate deviations** (keep them, and cite the decision in a code comment):
   - static context only (DQ-1);
   - local language ranking from patch bytes (DQ-2);
   - no model registry; the context window comes from config (DQ-3);
   - one estimator with a factor applied to every count (DQ-5);
   - `large_patch_policy` semantics for v1 (§4.5).

---

## 1. WP-PR-3a — NOTICE, embedded data, file filter

### 1.1 NOTICE (X-7) — the **first commit** of P3, before any adapted data lands
- Append a section "PR-Agent license" containing PR-Agent's MIT notice: the copyright line `Copyright (c) 2026 The PR Agent`, verbatim from the pinned upstream LICENSE, and the full permission and warranty text.
- Add a list "Files containing material adapted from PR-Agent". Fill it in as packages land: the 3a data files, the 3c literal-strings file and the golden testdata.

### 1.2 Embedded data (`internal/filter/data/`)
- Generate the data from the pinned upstream settings files:
  - from `generated_code_ignore.toml`: the `[generated_code]` table, framework name → globs;
  - from `language_extensions.toml`: `[bad_extensions].default` (only `default`; `extra` is not ported because there is no config key in §5) and `[language_extension_map_org]`.
- Format: Go source files (maps and slices). Each file starts with a header comment naming the upstream file and the commit, and pointing to NOTICE.
- Add a `go test` that re-checks the counts recorded in the header, to guard against accidental edits.
- The hardcoded upstream lockfile and minified names are ported as data too (porting map §A step 3):
  - lockfiles: `package-lock.json`, `yarn.lock`, `pnpm-lock.yaml`, `composer.lock`, `Gemfile.lock`, `poetry.lock`, `go.sum`, `.terraform.lock.hcl`, `uv.lock`, `Cargo.lock`, `Pipfile.lock`, `mix.lock`, `pubspec.lock`, `bun.lockb`;
  - suffixes: `.min.js`, `.min.css`, `.js.map`, `.ts.map`, `.css.map`.

### 1.3 Filter (`internal/filter`)
- `func New(cfg *config.Config) (*Filter, error)`, `func (f *Filter) Include(path string) bool` (plugs into `provider.DiffOptions.Include`), and `func (f *Filter) Explain(path string) (included bool, reason string)` for diag.
- Exclusion reasons: `ignore_glob`, `ignore_regex`, `generated:<framework>`, `bad_extension`, `lockfile_or_minified`.
- **Globs:** `ignore.glob` and the selected generated-code globs. Match with `github.com/bmatcuk/doublestar/v4` (MIT) against the full slash-separated path.
  - Add a test proving that `**/x.pb.go` matches both `x.pb.go` at the root and `a/b/x.pb.go`. This is upstream's root-variant behaviour.
- **Regex:** `ignore.regex`, compiled as `^(?:pattern)`. This keeps Python `re.match` semantics: anchored at the start, not at the end.
- **Extensions:** compare the lowercase last dot-suffix against `bad_extensions`.
- **Errors:** invalid globs or regexes cannot reach `New`, because config validation rejects them first (§1.4). `New` still returns an error rather than failing open.

### 1.4 Config validation additions (`internal/config`)
- `ignore.glob`: every pattern must pass `doublestar.ValidatePattern`.
- `diff.ignore_generated_frameworks`: every name must exist in the embedded table. The error lists the valid names.
- These replace the P1 placeholder comment "semantic validation is added in P3".
- **[canary]** An unknown framework name and a malformed glob each produce a startup problem.

### 1.5 Language classification (`internal/filter/lang.go`)
- Port upstream's matcher (porting map §A step 4):
  1. the exact basename;
  2. then progressively longer dotted suffixes, for multi-dot extensions;
  3. exact match first, then a unique case-folded match;
  4. no match → `Other`.
- `func Language(path string) string`.

---

## 2. WP-PR-3b — Token estimator and budget (`internal/tokens`)

### 2.1 Estimator
- **Library:** `github.com/tiktoken-go/tokenizer` (MIT) with **`o200k_base`**. Its vocabularies are compiled into the binary.
  - **Forbidden:** any tokenizer that downloads BPE files at runtime, for example `pkoukk/tiktoken-go` without an offline loader. No network access, ever (leak-first stance, X-8).
- Initialize lazily, once (`sync.Once`), so binaries that never count tokens pay nothing at startup.
- `func Raw(text string) int` is the plain BPE count.
- `func Estimate(text string, factor float64) int` returns `ceil(Raw(text) × (1 + factor))`, where `factor` is `llm.token_estimate_factor`.
  - **Every budget decision uses `Estimate`** (DQ-5). This deliberately deviates from upstream, which inflates only non-OpenAI models.
- **[canary]** Run `Raw` with `HTTPS_PROXY`/`HTTP_PROXY` pointed at a closed local port and with no network. It must still return the known count for a fixed fixture string. Record that fixture's expected `Raw` count in the test, cross-checked against upstream `tiktoken` `o200k_base` in the parity environment.

### 2.2 Budget (`type Budget`)
- **Inputs:**
  - `ContextWindow`: `llm.context_window`, required (DQ-3).
  - `MaxOutputTokens *int`: `llm.max_output_tokens`.
  - `PromptTokens`: the estimate of the rendered prompt scaffolding with an empty diff. P4 computes it; P3 receives it as an input.
  - `Factor`.
- **Derived values (DQ-4):**
  - `HardReserve = max(MaxOutputTokens or 0, 1000)`
  - `SoftReserve = HardReserve + 500`
  - `SoftLimit = ContextWindow − SoftReserve − PromptTokens`
  - `HardLimit = ContextWindow − HardReserve − PromptTokens`

  These are the diff-content budgets, and they may be ≤ 0.
- `func (b Budget) RequireCapacity() error` returns the sentinel `ErrDoesNotFit` (usable with `errors.Is`) when `SoftLimit ≤ 0`. This is the `FallbackEligible` seam of DQ-9.
- `func RequestTokens(system, user string, factor float64) int` returns `Estimate(system) + Estimate(user) + 3×16`, which accounts for two messages and the reply framing.

### 2.3 Clipping
- `func Clip(text string, maxTokens int, factor float64, deleteLastLine bool) string`:
  1. If `maxTokens ≤ 0`, return `""`.
  2. If the text already fits, return it unchanged.
  3. Otherwise make a heuristic cut: `chars = 0.9 × len/estimate × maxTokens`, at a rune boundary. If `deleteLastLine` is set, cut back to the last `\n`. Append `\n...(truncated)`.
  4. Then **verify by exact recount** and shrink (by 10% steps) until the result fits. Never return text that exceeds `maxTokens`.
- **[canary]** For a pathological input (a long line of multi-byte characters with no newlines) the result must fit. Prove this by removing the verification loop.
- `func ClipDescription` and `func ClipCommits` wrap `Clip` with `diff.max_description_tokens` and `diff.max_commits_tokens`. P4 will use them.

---

## 3. WP-PR-3c — Hunk model, context extension, renderers (`internal/patch`)

### 3.1 Hunk model (DQ-10)
- `func ParseHunks(patch string) ([]Hunk, error)` parses a hunk-only patch, as produced by the P2 providers.
- Types:
  - `Hunk{ OldStart, OldLen, NewStart, NewLen int; Section string; Lines []Line }`
  - `Line{ Op byte; Text string }`, with `Op` one of `' '`, `'+'`, `'-'`, `'\\'`. `Text` keeps the original line ending.
- Omitted counts mean 1, as in git. `\ No newline at end of file` lines are kept in the model.
- **Every view is produced by rendering from this model.** No view is produced by string-stripping another view.

### 3.2 Static context extension (DQ-1)
- `func Extend(hunks []Hunk, base, head *string, before, after int) []Hunk` uses upstream `extend_patch` semantics with dynamic context **off**:
  - `before`/`after` come from `diff.extra_lines_before`/`after`; config already enforces the cap of 10.
  - No extension for files whose name ends with one of `diff.skip_extend_extensions`.
  - No extension when the needed content is nil, meaning it was not fetched.
  - Hunk start lines must match the file content, with upstream's validation and its "mini match" retry. Pre-hunk context must be equal in base and head. Never read past EOF.
  - Header numbers are rewritten. The `@@ … @@ <section>` text is preserved.
  - **On any doubt, return the unextended hunk.**
- **Goldens:** the oracle (§0.3) covers at least these cases:
  - an ordinary hunk;
  - a hunk at the start of the file;
  - a hunk at EOF;
  - two hunks whose extensions would overlap;
  - a mismatching hunk header (must **not** extend; **[canary]**);
  - a CRLF file;
  - a `.md` file (skipped).

### 3.3 Deletion handling (compressed path only)
- Port `handle_patch_deletions` and `omit_deletion_hunks`:
  - A deleted file has its patch dropped and only its name kept, for the `Deleted files:` list.
  - Otherwise, hunks that contain no `+` lines are removed.

### 3.4 Renderers
- `RenderPlain(file)` is upstream plain mode: the `## File: '<path>'` header, then the patch with a blank line before each `@@`.
- `RenderDecoupled(file, numbered bool)` is upstream's `decouple_and_convert_to_hunks_with_lines_numbers` format, byte for byte:
  - the `__new hunk__` block, with new-file line numbers when `numbered`;
  - the `__old hunk__` block, unnumbered and only when the hunk has `-` lines;
  - `\ No newline` lines dropped;
  - the deleted-file line;
  - the skip rules for malformed pseudo-hunks.
- **Unreadable notice** (amended after the P3 review, D3/D4):
  - Render the notice only when the provider's `HeadStatus` is `fetch_failed` **and** the patch has no hunks. This matches upstream's own trigger at `8e5a929`.
  - A file with a valid patch is rendered normally. It stays unextended, because its head content is missing.
  - The wording is upstream's "could not be read … flag it for manual review" text, with the product name changed to "review-mcp" (D4).
- All literal strings live in `internal/patch/literals.go` (§0.2).
- **[canary]** The numbered render of a fixture must equal the oracle golden byte for byte. Prove it by introducing an off-by-one in the line numbering.

---

## 4. WP-PR-3d — Assembly (`internal/diffpipe`)

### 4.1 API
```go
type Mode int // ModePlain (pr_ask), ModeNumbered (pr_review)
type Input struct {
    Files    []provider.FilePatch
    Skipped  []provider.SkippedFile // from the provider: filtered, binary, limits, fetch_failed
    Mode     Mode
    Budget   tokens.Budget
    Diff     config.Diff
}
type Prepared struct {
    Text       string
    FastPath   bool
    Tokens     int      // Estimate(Text)
    Included   []string // paths, in output order
    Omitted    Omitted  // Added, Modified (incl. renamed), Deleted []string: dropped for budget
    Clipped    []string // paths included in clipped form (§4.5)
    Skipped    []provider.SkippedFile // passed through, untouched
}
func Prepare(in Input) (*Prepared, error)
```

### 4.2 Language ranking (DQ-2)
- Classify each file with `filter.Language`. A group's weight is the sum of its files' patch byte lengths.
- Sort groups by weight, descending; ties are broken by language name. `Other` always comes last.
- Within a group, keep the provider's order for the fast path.

### 4.3 Fast path
- Extend every file (§3.2) and render it in `Mode`, in group order. Deleted files get the same treatment as in upstream's extended-diff path.
- If `Estimate(joined) < SoftLimit`, return it with `FastPath = true`. Nothing is omitted.

### 4.4 Compressed path (porting map §A step 8, upstream-exact except for §0.4)
1. No extension: use the raw patches. Apply deletion handling (§3.3).
2. Render and estimate each file.
3. Within each language group, sort by token count **descending**. Group order is preserved.
4. **Admission loop:**
   - Once the running total exceeds `HardLimit`, every remaining file is skipped outright.
   - Otherwise a file is admitted if and only if `running + tokens + separator ≤ SoftLimit`.
   - Rejected files are recorded in `Omitted` by change type.
5. Join the admitted files and **recount exactly**. If the result exceeds `SoftLimit`, binary-search the longest *verified* fitting prefix. Counts are not assumed to be additive. Cut files move to `Omitted`.
6. Append the omitted-file sections in upstream order (added, modified, deleted), each clipped with `tokens.Clip`. Append a section only when the exact recount still fits within `HardLimit`, and only when at least 10 tokens of headroom remain.

### 4.5 `diff.large_patch_policy` in v1 (deliberate rule, X-3 single call)
- Upstream applies this policy only when packing multiple calls; v1 makes a single call.
- **v1 rule:** if the admission loop admits **no file at all** while at least one non-deleted file exists:
  - `clip`: include the top-ranked file clipped to `SoftLimit` (`deleteLastLine = true`, marker appended), and record it in `Clipped`.
  - `skip`: include nothing.
- Then, if the diff text is still empty, `Prepare` returns `tokens.ErrDoesNotFit`, and P4 reports it honestly.
- **[canary]** A PR containing one file larger than the budget produces a clipped, non-empty diff under `clip`, and `ErrDoesNotFit` under `skip`.

### 4.6 Accounting invariant
- Every input file (`Files` plus `Skipped`) appears in **exactly one** of `Included`, `Omitted.*`, `Clipped` or `Skipped`.
- **[canary]** A property-style test with randomized file sets and budgets checks the invariant. Prove it by making the loop silently drop one rejected file.

### 4.7 Determinism
- The same input gives byte-identical output. No map iteration order may leak into the result.
- A test runs `Prepare` 50 times on the same input and compares the outputs.

---

## 5. WP-PR-3e — `diag diff` and docs

- `review-mcp diag diff <PR_URL> [--mode plain|numbered] [--prompt-tokens N]` (the default for `N` is 1500, documented as an approximation until P4 measures the real prompts):
  1. Run provider → filter → `Prepare`.
  2. Print a JSON header to stdout with:
     - `budget{context_window, soft_limit, hard_limit, prompt_tokens, factor}`;
     - `fast_path`, `tokens`;
     - `included`, `omitted`, `clipped`, `skipped` (with reasons);
     - `filtered` (path and reason from `Explain`);
     - `elapsed_ms`.
  3. Print a line `--- prepared diff ---`, then the text.
- The diff content goes **only to stdout**, never to logs.
- Errors use the X-6 sentences. `ErrDoesNotFit` gets a fixed sentence telling the user to raise `llm.context_window` or narrow the PR.
- `docs/troubleshooting.md` gains a section on reading `diag diff`: the fast path versus the compressed path, the omitted lists, and the filter reasons. `docs/getting-started.md` gains a section on how `llm.context_window` and `llm.max_output_tokens` shape the diff budget.

## 6. Architect review checklist (per package)
- The usual checklist: full diff read, gates re-run, at least one canary re-proven independently.
- **3a:** NOTICE contains the verbatim copyright line and the list of files. The data counts match upstream.
- **3b:** no network path exists anywhere in the tokenizer dependency graph. Show `go mod graph` for the new modules in the report.
- **3c/3d:** the goldens are oracle-generated or explicitly marked "hand-derived". Every §0.4 deviation has a decision comment. The literals are in one file.
- New modules allowed in P3: `github.com/bmatcuk/doublestar/v4`, `github.com/tiktoken-go/tokenizer`. Nothing else without a DESIGN-QUESTION.

## 7. Live acceptance (owner)
Use the P2 test instances.

1. **Small PR:**
   - `diag diff --mode plain` reports `fast_path: true`.
   - Spot-check one hunk against the web UI: 5 extra lines before and 1 after, correct line numbers in `--mode numbered`.
2. **Oversized PR:** set `REVIEW_MCP_LLM_CONTEXT_WINDOW=8000` on a PR with many files (for example a dependency bump plus code changes).
   - `fast_path: false`.
   - Included files are ordered by language group, then by size.
   - Every file is accounted for in exactly one list.
   - The omitted sections appear at the end of the text.
3. **Filtering:**
   - A PR that touches `vendor/…`, `package-lock.json` and an image file lists all three under `filtered` with the right reasons.
   - Adding `REVIEW_MCP_DIFF_IGNORE_GENERATED_FRAMEWORKS=protobuf` filters a `.pb.go` file.
4. **One huge file:** with `REVIEW_MCP_LLM_CONTEXT_WINDOW=4096` and a single-file PR larger than the budget, `clip` yields a truncated diff and `skip` yields the "does not fit" error.

Post the redacted results on the P3 PR. The merge happens after the record exists.

## 8. Planned commits (branch `p3-diff-pipeline`)
1. `docs(notice): add PR-Agent MIT notice for adapted material`
2. `feat(filter): add embedded file-classification data, filter and language matcher`
3. `feat(config): validate ignore globs and generated-framework names`
4. `feat(tokens): add offline o200k token estimator, budget and clipping`
5. `feat(patch): add hunk model, static context extension and renderers`
6. `feat(diffpipe): add language ranking, budget admission and coverage accounting`
7. `feat(cli): add diag diff command`
8. `docs: document diff budgeting, filtering and diag diff`
