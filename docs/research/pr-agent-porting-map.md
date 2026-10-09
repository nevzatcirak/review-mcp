# PR-Agent Porting Map for review-mcp

| | |
|---|---|
| Upstream project | [PR-Agent](https://github.com/The-PR-Agent/pr-agent) (MIT) |
| Pinned revision | tag **v0.47.0**, commit **8e5a9295973b24af4b70cafd0b660a230811ef9e** (2026-10-02) |
| Clone date | 2026-10-06 |
| Study scope | review-mcp v1: `pr_review` + `pr_ask`, Gitea + Bitbucket Server (Data Center), any OpenAI-compatible LLM endpoint, stdio-first, per-user secrets, no telemetry, no embedded URL/endpoint/model defaults |
| Status | WP-PR-0 deliverable — pre-implementation research; drives all later implementation work packages |

All `path:function @ 8e5a929` references in this document point into the pinned
revision above. No upstream source code is included in this repository; the
upstream clone used for the study lives outside the repo and is not committed.

> **Note on the checkout:** the pinned tree carries code beyond "classic"
> PR-Agent (notably `pr_agent/algo/token_budget.py`, which absorbs
> `get_max_tokens`/`clip_tokens` from `utils.py` and adds `AttemptTokenBudget`,
> plus review chunking in `pr_reviewer.py`). This document describes the code
> exactly as it exists at commit `8e5a929`.

## Executive summary

**What was studied.** The seven areas that decide porting quality: (A) the diff
compression pipeline, (B) token budgeting, (C) prompt assembly and output
schemas for `/review` and `/ask`, (D) output parsing and the repair path,
(E) the suggestion→line anchoring pipeline of `/improve` (deferred to v2,
documented now), (F) the Gitea and Bitbucket Server provider implementations
behind the provider abstraction, and (G) the full configuration surface. Each
section below follows the same structure: Mechanism, Key code, Data flow,
Porting notes (Go), Config knobs, Risks, and DESIGN-QUESTION blocks for
decisions the architect must make.

**The five most important porting insights:**

1. **The compression pipeline is a set of exact contracts, not heuristics.**
   The ordering rules (language-group order, then token-count descending
   admission), the soft/hard output buffers (1500/1000 tokens), and the marker
   strings (`## File: '<name>'`, `__new hunk__`/`__old hunk__`,
   `...(truncated)`, the "Additional added/modified files" lists) are contract
   text shared with the prompts and the anchoring logic. The port must keep
   them byte-compatible and treat the ordering as part of the spec — changing
   it silently changes which files reviewers see.

2. **Token budgeting must be rebuilt without LiteLLM.** Upstream resolves
   model context windows from a static ~400-entry registry plus LiteLLM
   metadata and silently clamps everything to a 32000-token default. None of
   that carries over: the honest Go posture is one tiktoken `o200k_base`
   estimator, a configurable safety factor (default 0.3), and a required,
   validated `max context tokens` config value (which also expresses the ~250k
   deployment cap). Keep the "heuristic shrink → exact recount" discipline;
   BPE counts are not additive.

3. **YAML + the repair chain is the main correctness lever, not polish.**
   The `/review` schema is prompt-prose (no provider JSON mode), and arbitrary
   OpenAI-compatible endpoints — our explicit target — misformat YAML far more
   than frontier models. The 12-tactic `try_fix_yaml` chain, the open-fence
   prompt tail, the strict-but-warn-only schema validation, and "temperature
   never changes on retry" are the battle-tested package to port.

4. **Both target providers diverge sharply from GitHub assumptions, and one
   divergence is a security finding.** Gitea splits a whole-PR `.diff` with a
   lossy hand-rolled scanner (renames and spaced paths break — our port should
   use a real unified-diff parser instead). Bitbucket Server has no usable
   server-side diff at all: upstream downloads full base/head files and
   regenerates patches locally with a version-gated base-SHA strategy
   (merge-base ≥8.16 / ancestor walk 7.0–8.15 / first-parent below). And
   upstream derives the Bitbucket **server URL from the pasted PR URL**, then
   sends the configured token there — in review-mcp's per-user-token model
   this is a credential-exfiltration vector. **review-mcp must pin provider
   base URLs from config and reject PR URLs pointing elsewhere.**

5. **The config surface collapses by ~85%, and some upstream subsystems are
   deliberately not ported at all.** Of upstream's 594-line, ~40-section
   configuration, five sections plus three embedded data files matter for v1
   (~25 keys). Secrets become env-only (rejected from config files), all
   config is validated at startup with token-free error messages, upstream's
   embedded `gitea.com`/model-name defaults are dropped per project rules, and
   the entire telemetry subsystem (`[otel]`, `pr_agent/telemetry/`) is omitted
   with no stub — review-mcp has no telemetry.

**DESIGN-QUESTION index (26 total).** Numbers below are document order; titles
are as they appear in each section.

| # | Area | Title |
|---|---|---|
| 1 | A | Dynamic context in v1 |
| 2 | A | Language-ranking fallback for Bitbucket Server |
| 3 | B | Model context-window source |
| 4 | B | Output reserve shape |
| 5 | B | Accurate counting endpoint |
| 6 | C | Output format of pr_review MCP tool |
| 7 | C | YAML vs JSON as the model output format |
| 8 | D | Repair-chain scope for v1 |
| 9 | D | Parse-failure retry semantics |
| 10 | E | Diff representation for the Go port |
| 11 | E | Anchor acquisition — second LLM pass vs direct |
| 12 | E | Verification source of truth |
| 13 | E | Gitea publication granularity and range anchors |
| 14 | E | Bitbucket Server lineType correctness |
| 15 | E | Cross-run dedup for Gitea/BBS |
| 16 | F.1 | Capability surface for v1 |
| 17 | F.2 | Gitea per-file patch source |
| 18 | F.2 | Inline publish granularity on Gitea |
| 19 | F.3 | Bitbucket Server diff acquisition |
| 20 | F.3 | Minimum supported Bitbucket DC version |
| 21 | F.3 | Basic auth support |
| 22 | G | G-1: Repo-local config file in v1? |
| 23 | G | G-2: Config file — have one at all, and in what format? |
| 24 | G | G-3: Env naming scheme |
| 25 | G | G-4: Per-call overrides vs static config |
| 26 | G | G-5: Temperature/parameter stripping for strict endpoints |

**Cross-section conflicts the architect must reconcile** (sections were
studied independently; their recommendations disagree in four places):

- **Fallback models:** sections B and D assume the fallback-model chain is
  ported (and D's DQ-9 builds on it); section G recommends dropping
  `fallback_models` for v1 (single model, fail honestly). Decide with DQ-9.
- **Dynamic context:** section A's DQ-1 recommends static-only extension in
  v1; section G's table marks `allow_dynamic_context` as keep. DQ-1 governs.
- **Temperature default:** sections C/D carry upstream's `temperature=0.2`
  default; section G's DQ-26 recommends sending sampling parameters only when
  explicitly set (no compiled default). Decide with DQ-26.
- **Detailed error output:** section D recommends porting the sanitized
  error-classifier with detailed errors on by default; section G drops
  `publish_error_details`. The sanitization allowlist itself is wanted either
  way (it is the token-leak firewall); decide only the verbosity default.

---

## A. Diff compression pipeline

**Mechanism**

1. **Collect changed files.** Each git provider's `get_diff_files()` builds `FilePatchInfo` objects (`base_file`, `head_file`, `patch` = hunk-only unified diff, `filename`, `edit_type`, `tokens=-1`, `content_fetch_failed`). Providers call `filter_ignored(files, platform)` *before* building patch info, so ignored files never enter the pipeline.
2. **Ignore filtering** (`file_filter.py:filter_ignored`): patterns come from three sources, all compiled to regexes and applied with `re.match` (anchored at string start):
   - `[ignore].regex` — raw regex list (default empty).
   - `[ignore].glob` — glob list translated via `fnmatch.translate`; a glob starting with `**/` additionally gets a root-level variant (pattern without the `**/` prefix). Default in `ignore.toml`: `['vendor/**']`.
   - `config.ignore_language_framework` (default `[]`) — a list of generator names; each name indexes the `[generated_code]` table in `generated_code_ignore.toml` (e.g. `protobuf` → `**/*.pb.go` …, `go_gen` → `**/*_gen.go` …), whose globs are translated the same way.
   Invalid regexes are skipped with a warning (fail-open: those files reach the model). Matching is per-platform (field extraction differs; Azure strips a leading `/` before matching).
3. **Bad-extension filtering + generated-file hardcodes** (`language_handler.py:filter_bad_extensions` / `is_valid_file`): drops files whose last dot-suffix (case-insensitive) is in `[bad_extensions].default` (binary/asset types: `bin`, `png`, `zip`, `csv`, `lock`-adjacent, …, defined in `language_extensions.toml`), plus `[bad_extensions].extra` when `config.use_extra_bad_extensions=true`. Independent of config, it hard-drops exact lockfile basenames (`package-lock.json`, `yarn.lock`, `pnpm-lock.yaml`, `composer.lock`, `Gemfile.lock`, `poetry.lock`, `go.sum`, `.terraform.lock.hcl`, `uv.lock`, `Cargo.lock`, `Pipfile.lock`, `mix.lock`, `pubspec.lock`, `bun.lockb`) and suffixes `.min.js`, `.min.css`, `.js.map`, `.ts.map`, `.css.map`.
4. **Language ordering** (`language_handler.py:sort_files_by_main_languages`): the provider's `get_languages()` map (language → byte size; non-numeric values stripped) is sorted by size descending. Files are classified by filename against `language_extension_map_org` (from `language_extensions.toml`) using a matcher that tries the exact basename, then progressively longer dotted suffixes (supports multi-dot extensions), exact match first, then unique case-folded match. Output is an ordered list of `{"language": name, "files": [...]}` groups — repo's dominant language first — with a final `"Other"` bucket for unmatched files (de-duplicated by filename). If the provider reports no languages, everything lands in one `"Other"` group.
5. **Extended diff (fast path)** (`pr_processing.py:pr_generate_extended_diff`): iterates files in language order; for each patch calls `extend_patch` to widen every hunk with extra context:
   - `config.patch_extra_lines_before` (default **5**) and `config.patch_extra_lines_after` (default **1**), each capped to `MAX_EXTRA_LINES = 10` by `cap_and_log_extra_lines`.
   - Files whose name ends with any `config.patch_extension_skip_types` (default `[".md", ".txt"]`) are not extended.
   - **Dynamic context** (`config.allow_dynamic_context=true`, `config.max_extra_lines_before_dynamic_context=10`): looks up to 10 lines above the hunk for the line containing the hunk's section header (the `@@ ... @@ <header>` text, typically the enclosing function/class); if found and the pre-hunk lines are identical in old and new file, the context starts at that header line and the header text is removed from the `@@` line (it is now in-band). Otherwise falls back to the static `patch_extra_lines_before`.
   - Safety checks: hunk start line must match the original file content (`check_if_hunk_lines_matches_to_file`, with multi-encoding tolerance), extension never reads past EOF, and pre-hunk extra lines must be equal in old/new file (else it retries from a later offset — "mini match" — or drops the extension for that hunk). On any exception the unextended patch is returned.
   - Hunk headers are rewritten as `@@ -<extended_start1>,<extended_size1> +<extended_start2>,<extended_size2> @@ <section_header>`; added context lines are prefixed with a single space (context-line syntax).
6. **Patch decoration — two render modes:**
   - *Plain mode* (`add_line_numbers_to_hunks=False`): `\n\n## File: '<filename>'\n\n` header + the patch with a blank line inserted before each `@@` hunk.
   - *Decoupled line-numbered mode* (`git_patch_processing.py:decouple_and_convert_to_hunks_with_lines_numbers`, used by `/review` and `/improve`): same `## File:` header, then per hunk the original `@@` header followed by a `__new hunk__` block — the new-file content (context + `+` lines) each prefixed with its **new-file line number** (`start2 + i`) — and, only when the hunk has `-` lines, an `__old hunk__` block with the removed/context lines, **without** line numbers. `\ No newline at end of file` markers are dropped; a `@@`-prefixed line that is not a valid hunk header causes that pseudo-hunk to be skipped with a warning; a deleted file renders as `\n\n## File '<filename>' was deleted\n`.
   - Optional AI metadata (`config.enable_ai_metadata`, default false): inserts `### AI-generated changes summary:` under the `## File:` header from a prior `/describe` run.
   - A file with `content_fetch_failed` renders an explicit "This file could not be read … flag it for manual review" notice instead of being silently dropped.
7. **Fit test**: total = `token_handler.prompt_tokens` + max(count(joined), count(joined.strip())) (`_count_raw_and_stripped_tokens` — counts both because either form may be rendered). If `total − prompt_tokens < soft_token_budget` the **full extended diff** is returned single-shot and no pruning happens.
8. **Compression** (`pr_generate_compressed_diff`), entered only when over the soft budget; extra context lines are *not* used here (patches are the raw provider patches):
   - **Rank**: within each language group files are sorted by their token count **descending** (biggest first); the language-group order from step 4 is preserved across groups. This ordering is the drop priority: iteration order decides who gets admitted while budget remains.
   - **Deletions**: `handle_patch_deletions` — a file with no new content and `edit_type` DELETED/UNKNOWN gets `patch=None` and goes to `deleted_files_list` (name only); otherwise `omit_deletion_hunks` strips hunks that contain no `+` lines (deletion-only hunks), keeping hunks with any addition.
   - Each surviving patch is (optionally) converted to the line-numbered form, counted, and stored in `file_dict[filename] = {patch, tokens, edit_type}`.
   - **Admission loop** (`generate_full_patch`): iterate `file_dict` in sorted order. If accumulated diff tokens already exceed the **hard** budget, every further file is skipped outright ("File was fully skipped, no more tokens"). Otherwise a file is admitted iff `running_total + patch_tokens + separator ≤ prompt_tokens + soft_budget`; rejects go to `remaining_files_list` (file kept whole — **no partial clipping on this path**). After the loop the concatenation is re-counted exactly; if it overflows the soft budget, `_find_verified_fitting_prefix_length` binary-searches the longest *verified* fitting prefix of admitted patches (token counts are not assumed monotone under concatenation) and the cut files move to `remaining_files_list`.
   - **Clipped-content markers** (appended after the diff, in this order, only if ≥ `delta_tokens = 10` tokens of headroom against prompt+hard budget remain): `Additional added files (insufficient token budget to process):` + names, then `Additional modified files (…)` + names (MODIFIED and RENAMED), then `Deleted files:` + names. Each section is itself clipped with `clip_tokens` (marker `\n...(truncated)`) and only appended if the exact recount still fits (`_append_metadata_section`).
9. **Large-PR handling:**
   - *Single-shot vs multi*: `get_pr_diff(..., large_pr_handling=True)` (the `/describe` flow) returns `""` when compression produced more than one chunk, signalling the caller to switch to the multi-call prompt. With `large_pr_handling`, `pr_generate_compressed_diff` loops `generate_full_patch` up to `pr_description.max_ai_calls` (default **4**) minus one reserved summarize call minus the first iteration, consuming `remaining_files_list` each round → a list of chunk-patch-lists.
   - *Chunk packing* (`get_pr_multi_diffs` → `_pack_pr_multi_diffs`, `max_calls` default **5**; `/review` chunking passes `pr_reviewer.max_number_of_calls` = **3**): first tries the full extended diff single-shot; otherwise re-sorts files (tokens desc per language), rebuilds `file_dict`, then packs first-fit in order: a patch bigger than the whole budget is clipped (`large_patch_policy` — config default `"clip"`, code fallback `"skip"`) via `clip_tokens(delete_last_line=True)` and re-verified, else skipped; groups are formed additively (sum of per-patch counts + `\n` separators), then each rendered group is exactly re-counted and, if over, split at a verified fitting prefix. Packing stops at `max_calls`; unpacked files are reported as `remaining_files_list`. The last chunk is `.strip("\r\n")`-ed only when every packable file was placed.
   - *Review chunking* (this fork; `pr_reviewer.enable_large_pr_chunking=false` default): chunks are sent concurrently (`asyncio.gather`), per-chunk YAML verdicts are validated and merged; on model fallback, oversized pending chunks are re-split at `^## File: '` boundaries against the new model's budget, and previously-omitted files are re-tried into pending chunks.
10. **File-type treatment summary**: *deleted* → name-only under `Deleted files:`; *renamed* → treated like modified (EDIT_TYPE.RENAMED lands in the "Additional modified files" list when unprocessed); *binary/asset* → excluded by `[bad_extensions]`; *generated* → excluded by hardcoded lockfile/minified names + opt-in `[generated_code]` globs; *ignored* → excluded by `[ignore]` regex/glob before processing; *unreadable* → explicit in-diff notice.

**Key code**

- `pr_agent/algo/file_filter.py:filter_ignored` / `translate_globs_to_regexes` @ 8e5a929
- `pr_agent/algo/language_handler.py:filter_bad_extensions`, `is_valid_file`, `build_language_file_matcher`, `sort_files_by_main_languages` @ 8e5a929
- `pr_agent/algo/types.py:FilePatchInfo`, `EDIT_TYPE` @ 8e5a929
- `pr_agent/algo/pr_processing.py:get_pr_diff`, `pr_generate_extended_diff`, `pr_generate_compressed_diff`, `generate_full_patch`, `_append_metadata_section`, `_find_verified_fitting_prefix_length`, `get_pr_multi_diffs`, `_pack_pr_multi_diffs`, `get_pr_diff_multiple_patchs`, `_unreadable_file_notice`, `cap_and_log_extra_lines` (constants `OUTPUT_BUFFER_TOKENS_SOFT_THRESHOLD=1500`, `OUTPUT_BUFFER_TOKENS_HARD_THRESHOLD=1000`, `MAX_EXTRA_LINES=10`, `DELETED_FILES_`, `MORE_MODIFIED_FILES_`, `ADDED_FILES_`) @ 8e5a929
- `pr_agent/algo/git_patch_processing.py:extend_patch`, `process_patch_lines`, `check_if_hunk_lines_matches_to_file`, `handle_patch_deletions`, `omit_deletion_hunks`, `decouple_and_convert_to_hunks_with_lines_numbers`, `extract_hunk_lines_from_patch`, `to_hunk_only_patch`, `RE_HUNK_HEADER` @ 8e5a929
- `pr_agent/settings/ignore.toml`, `pr_agent/settings/generated_code_ignore.toml`, `pr_agent/settings/language_extensions.toml` (`[bad_extensions]`, `[language_extension_map_org]`) @ 8e5a929
- `pr_agent/tools/pr_reviewer.py:_prepare_prediction`, `_prepare_chunked_prediction`, `_resize_pending_review_chunks` @ 8e5a929 (consumer of the pipeline)

**Data flow**

- Input: provider diff (per-file unified patches), base/head file contents, repo language map, settings.
- Intermediate: `List[FilePatchInfo]` → ignore/bad-extension filtered list → `[{language, files}]` ordered groups → extended patch strings (+ per-file `file.tokens`) → `file_dict: {filename: {patch, tokens, edit_type}}` (token-desc order) → admitted `patches` + `remaining_files_list` + `deleted_files_list` → final diff string (`## File:` sections, optional `__new hunk__`/`__old hunk__` blocks, trailing added/modified/deleted name lists) or list of chunk strings.
- Output shapes: `str` (single call), `(str, remaining_files)`, `PreparedPRDiff` (diff + `file_dict` + per-model budget, reused by review chunking to avoid re-fetch/re-count), or `List[str]` chunks (+ remaining files).

**Porting notes (Go)**

- Natural package split: `diff/filter` (ignore + bad-extension + generated), `diff/lang` (grouping/ordering), `diff/patch` (extend, deletion handling, line-numbered rendering — pure functions over strings, highly unit-testable), `diff/pack` (budget admission + chunk packing), with `FilePatchInfo`-equivalent struct in a shared `types` package. Keep render format byte-compatible with PR-Agent prompts (`## File: '<name>'`, `__new hunk__`/`__old hunk__`, marker strings) since ported prompts anchor on them.
- Python `fnmatch.translate` glob→regex semantics do not match Go's `path.Match` (no `**`). Use `doublestar` or compile globs to regex yourself; replicate the `**/`-prefix root-level variant and the fail-open skip of invalid patterns (log + continue).
- `re.match` is implicitly anchored at the start only — in Go use `^(?:pattern)` without `$` unless the translated glob already ends with `\Z` equivalent.
- Repo language map: Gitea has a languages API; Bitbucket Server does not — plan a fallback that derives language ordering from the changed files' extensions (byte counts), or accept the single-"Other"-group path (the code already handles an empty language map).
- The multi-encoding fallbacks (`decode_if_bytes`, encoding retries in hunk validation) are Python-isms; in Go, treat file content as UTF-8 with an invalid-byte tolerance and skip extension when validation fails — the code's posture is always "on doubt, return the unextended patch".
- `_count_raw_and_stripped_tokens` (max of raw/stripped) and the verified-prefix binary search exist because BPE counts are not additive/monotone under concatenation; keep both behaviors.
- Review chunk-resize/merge and `PreparedPRDiff` reuse are fork-specific refinements; port only if pr_review adopts chunking in v1.

**Config knobs**

| upstream key | default | keep/drop/rename for Go |
|---|---|---|
| `ignore.regex` | `[]` | keep (`ignore.regex`) |
| `ignore.glob` | `['vendor/**']` | keep (`ignore.glob`); keep the vendor default — it's generic |
| `config.ignore_language_framework` + `[generated_code]` table | `[]` / builtin globs | keep; ship the generated-code glob table as embedded data |
| `[bad_extensions].default` / `.extra` + `config.use_extra_bad_extensions` | builtin list / `false` | keep as embedded default list + user-extendable `ignore.extensions` |
| `config.patch_extra_lines_before` | `5` (cap 10) | keep (`diff.extra_lines_before`), keep the cap |
| `config.patch_extra_lines_after` | `1` (cap 10) | keep (`diff.extra_lines_after`) |
| `config.allow_dynamic_context` | `true` | see DESIGN-QUESTION below |
| `config.max_extra_lines_before_dynamic_context` | `10` | with dynamic context, keep |
| `config.patch_extension_skip_types` | `[".md",".txt"]` | keep (`diff.skip_extend_extensions`) |
| `config.large_patch_policy` | `"clip"` (code fallback `"skip"`) | keep; make the default explicit — recommend `clip` |
| `config.enable_ai_metadata` | `false` | drop for v1 (depends on /describe output) |
| `pr_description.max_ai_calls` | `4` | drop (no /describe in v1) |
| `pr_reviewer.enable_large_pr_chunking` | `false` | keep as `review.chunking.enabled` if chunking is in scope |
| `pr_reviewer.max_number_of_calls` | `3` | keep with chunking (`review.chunking.max_calls`) |
| `get_pr_multi_diffs(max_calls=…)` | `5` (code) | fold into the same chunking config |
| `[language_extension_map_org]` | builtin map | keep as embedded data (needed for language grouping) |

**Risks**

- The extend/validate logic is the subtlest part (off-by-one on 1-based hunk starts, EOF capping, old/new context equality, dynamic-context header hoisting). Port it with table-driven tests ported from `tests/unittest` and canary cases (deliberately mismatched hunk headers must *not* extend).
- Admission order means one huge low-priority-language file can still starve smaller files only through the hard-budget early-skip; dropping the exact ordering (language-group, then tokens desc) changes which files reviewers see, i.e. silent quality regressions. Treat ordering as part of the spec.
- Fail-open ignore handling (invalid pattern → file is sent to the model) is a deliberate choice; document it, or a Go port that fails closed will silently review fewer files.
- The marker strings (`Additional modified files …`, `...(truncated)`, `__new hunk__`) are contract text with the prompts; renaming them breaks ported prompt logic and line-anchoring of findings.
- Bitbucket Server lacking a languages endpoint makes the "main language first" ranking provider-dependent; without a fallback, the Go port's file ordering will differ between providers.

DESIGN-QUESTION: Dynamic context in v1
The dynamic-context feature (hoist context start to the enclosing function/class header, up to 10 lines) adds real review quality but is the most intricate code in `extend_patch`, needs the head-file content, and has several bail-out branches. Options: (a) port fully in v1; (b) v1 ships static extra-lines only (`allow_dynamic_context` reserved, default false) and dynamic context comes in v2; (c) port but default off until live-validated. Recommendation: (b) — static extension reproduces most of the value, shrinks the clean-room surface, and the config key can keep upstream's name for later.

DESIGN-QUESTION: Language-ranking fallback for Bitbucket Server
No languages API there. Options: (a) compute language sizes locally from changed-file extensions; (b) use the single "Other" group (upstream's no-languages path) on that provider; (c) rank purely by token count, ignoring language. Recommendation: (a), computed from the diff file list via the embedded extension map — cheap, deterministic, provider-agnostic.

## B. Token budgeting

**Mechanism**

1. **Tokenizer selection** (`token_handler.py:TokenEncoder`): tiktoken. `encoding_for_model(model)` only when the model name contains `"gpt"`; **everything else uses `o200k_base`**, and any error also falls back to `o200k_base`. The encoder for the configured primary model is cached as a lock-guarded singleton (model+encoder read/written atomically); fallback models get a fresh, uncached encoder.
2. **Counting** (`TokenHandler.count_tokens`): default is a pure tiktoken estimate (`encoder.encode(text, disallowed_special=())`). With `force_accurate=True` it goes through `litellm.acount_tokens` for a provider-native count (Anthropic/Azure/Bedrock/Vertex/Gemini routing, request-local api_key/api_base resolution, 9 MB `CLAUDE_MAX_CONTENT_SIZE` guard, `config.ai_timeout`=120s); if LiteLLM only produced a local estimate or errored, and the model is not an OpenAI model with a key, the tiktoken estimate is inflated by `ceil(estimate × (1 + config.model_token_count_estimate_factor))`, factor default **0.3** — i.e. non-OpenAI models are padded +30% when an accurate count is unavailable. The diff pipeline itself uses only the plain estimate; `force_accurate` is used at final request-fit checks elsewhere.
3. **Prompt scaffolding cost** (`TokenHandler.__init__` / `_get_system_user_tokens`): at construction the tool's system and user **Jinja2 templates are rendered with the real vars** (StrictUndefined; diff variable still empty) and counted → `prompt_tokens`. Every budget below subtracts this, so the split is: fixed scaffolding (counted exactly) vs diff content (gets the remainder) vs output reserve (constant). `for_model(model)` returns a handler re-bound to a fallback model's tokenizer without mutating the original.
4. **Max tokens per model** (`token_budget.py:get_max_tokens`): resolution order — (1) exact hit in the static `MAX_TOKENS` registry (`pr_agent/algo/__init__.py`, ~400 entries incl. provider-prefixed aliases); (2) `config.custom_model_max_tokens` if > 0; (3) alias normalization (`_thinking` suffixes, `openai/`/`azure/` prefixes) then registry; (4) `litellm.get_model_info(...).max_input_tokens` over provider-qualified/bare candidates; (5) raise. The result is then clamped to `min(value, config.max_model_tokens)` — **default `max_model_tokens = 32000`**, deliberately below model limits "to improve algorithmic quality" — unless the call site passes `ignore_max_model_tokens=True`.
5. **Buffers / thresholds** (`pr_processing.py` constants): `OUTPUT_BUFFER_TOKENS_SOFT_THRESHOLD = 1500` and `OUTPUT_BUFFER_TOKENS_HARD_THRESHOLD = 1000`. Soft (window − 1500 − prompt) governs the full-diff fast path, per-file admission, and chunk size; hard (window − 1000 − prompt) is the stop-adding ceiling and the limit for the trailing file-name metadata sections. So 1000–1500 tokens are always reserved for model output, scaled up when the AI handler asks for more (below).
6. **AttemptTokenBudget** (frozen dataclass, one per model attempt in the fallback chain): binds `model`, a model-bound `TokenHandler`, `context_window = get_max_tokens(model)`, and an optional `output_token_reserve: Callable(model, default) -> int` supplied by the AI handler (`get_output_token_reserve`) so reasoning-style models can reserve more output room. `available_tokens(default_output, preserve_minimum, clamp)` = `context_window − resolved_output_reserve − prompt_tokens`; `preserve_minimum=True` means the handler's reserve can only raise, never lower, the default; `clamp=False` lets packers see negative capacity (their strict hard stops rely on it). `require_input_capacity` raises `FallbackEligibleError` (≤ 0 capacity) so `retry_with_fallback_models` moves to the next configured model instead of failing the request.
7. **Exact request accounting** (`count_request_tokens` / `prepare_request`): the final (normalized) system+user messages are counted with the attempt tokenizer plus `MESSAGE_FRAMING_TOKEN_ALLOWANCE = 16` per message and `REPLY_FRAMING_TOKEN_ALLOWANCE = 16` once, plus `config.image_input_token_allowance` (default **4096**) per image block. This is the verified number compared against `input_token_limit`.
8. **Optional-text fitting** (`fit_optional_text` / `fit_prompt_variable`): for non-diff variable content (conversation history, related tickets) — render with the full candidate; if over the input limit, verify the empty-variable prompt fits (else `FallbackEligibleError`), then keep a prefix or suffix of the BPE token slice (`decode(..., errors="ignore")` to avoid split multi-byte chars), re-rendering and exact-counting each candidate, shrinking by a cost-scaled step (no monotonicity assumption), with marker `"\n...(truncated)\n"`.
9. **Heuristic clipping** (`clip_tokens`): character-ratio based — `chars_to_keep = 0.9 × (len/text_tokens) × max_tokens` (10% safety factor), optional `delete_last_line` for clean line boundaries, appends `"\n...(truncated)"`. Returns text unchanged on errors/non-numeric input (fail-open), `""` for `max_tokens ≤ 0`. Used for large-patch clipping and metadata sections; callers re-count exactly afterwards.
10. **What `token_budget.py` adds vs `token_handler.py`**: `token_handler.py` = tokenizer ownership and counting (estimate vs provider-accurate) plus scaffolding cost; `token_budget.py` = everything above counting — model window resolution (`get_max_tokens`), per-attempt immutable budget arithmetic with output reserves, exact request-shape accounting (framing/images), optional-text fitting, heuristic clipping, and the `FallbackEligibleError` contract that drives model fallback.

**Key code**

- `pr_agent/algo/token_handler.py:TokenEncoder.get_token_encoder`, `TokenEncoder._create_encoder`, `TokenHandler.__init__`, `TokenHandler._get_system_user_tokens`, `TokenHandler.count_tokens`, `TokenHandler._get_token_count_by_model_type`, `TokenHandler._apply_estimation_factor`, `TokenHandler.for_model`, `ModelTypeValidator.is_openai_model`, `CLAUDE_MAX_CONTENT_SIZE` @ 8e5a929
- `pr_agent/algo/token_budget.py:get_max_tokens`, `clip_tokens`, `AttemptTokenBudget` (`for_attempt`, `for_prompt_attempt`, `available_tokens`, `input_token_limit`, `require_input_capacity`, `resolve_output_reserve`, `count_request_tokens`, `prepare_request`, `fit_optional_text`, `fit_prompt_variable`, `matches`), `FittedPrompt`, `FallbackEligibleError`, constants `MESSAGE_FRAMING_TOKEN_ALLOWANCE=16`, `REPLY_FRAMING_TOKEN_ALLOWANCE=16`, `DEFAULT_TRUNCATION_MARKER` @ 8e5a929
- `pr_agent/algo/__init__.py:MAX_TOKENS` (static registry, line ~235) @ 8e5a929
- `pr_agent/algo/pr_processing.py:OUTPUT_BUFFER_TOKENS_SOFT_THRESHOLD`, `OUTPUT_BUFFER_TOKENS_HARD_THRESHOLD`, `retry_with_fallback_models`, `_get_all_models` @ 8e5a929
- `pr_agent/settings/configuration.toml` `[config]`: `model`, `fallback_models`, `max_model_tokens=32000`, `custom_model_max_tokens=-1`, `model_token_count_estimate_factor=0.3`, `image_input_token_allowance=4096`, `ai_timeout=120` @ 8e5a929

**Data flow**

- Inputs: model name, prompt templates + vars, settings, diff text.
- Intermediates: tiktoken encoder (per model) → `prompt_tokens` (rendered scaffolding) → `context_window` (registry/config/litellm, clamped) → soft/hard diff budgets → per-file/per-chunk token counts → exact request token count (messages + framing + images).
- Outputs: integer budgets consumed by section A's packers; `FittedPrompt` (normalized system/user + exact `input_tokens`); `FallbackEligibleError` signaling "try next model".

**Porting notes (Go)**

- Tokenizer: use a Go tiktoken port (e.g. `pkoukk/tiktoken-go`) with **o200k_base** as the single default — review-mcp targets arbitrary OpenAI-compatible endpoints, so the "gpt → encoding_for_model" branch collapses; one encoding + a configurable safety factor is the honest posture. Treat all counts as estimates.
- Everything LiteLLM-specific does **not** carry over: provider-native accurate counting (`litellm.acount_tokens`), `litellm.get_model_info` window lookup, Azure deployment routing, the `_thinking`/`gpt-6-astra` alias normalizations, and the per-provider api_key/api_base plumbing. Their *roles* map to: (a) estimate factor padding (keep), (b) explicit config for the window (keep, see DESIGN-QUESTION), (c) nothing.
- The static `MAX_TOKENS` registry is a maintenance treadmill and collides with review-mcp's "no embedded defaults" stance; prefer a required/validated config value. The ~250k deployment cap maps directly to upstream's `max_model_tokens` clamp semantics (min of model window and configured cap).
- `AttemptTokenBudget` ports cleanly to a small immutable struct (`Model`, `ContextWindow`, `PromptTokens`, `OutputReserve`); `FallbackEligibleError` becomes a sentinel error (`errors.Is`) consumed by the fallback-model loop. If v1 has no fallback chain, keep the sentinel anyway — it is the clean "diff cannot fit" signal for an MCP error message.
- Jinja2 template rendering for scaffolding counting becomes `text/template` (or plain string assembly); the key invariant to keep: **count the scaffolding with the real variables rendered, not the raw template**, and recount per model attempt.
- Keep the discipline the code shows everywhere: heuristic shrink (ratio clip / token-slice) → **exact recount verification** before accepting; never assume BPE counts are additive or monotone.
- The framing allowances (16/msg + 16) and image allowance are cheap insurance; port the constants. Image input is likely out of scope for v1 — drop the image path, keep the message framing.

**Config knobs**

| upstream key | default | keep/drop/rename for Go |
|---|---|---|
| `config.model` / `config.fallback_models` | `gpt-5.6` / `[gpt-5.6-terra]` | keep shape (`llm.model`, `llm.fallback_models`), **no default value** per review-mcp rules |
| `config.max_model_tokens` | `32000` | keep as `llm.max_context_tokens` (deployment cap, e.g. ~250k); this is the primary budget knob |
| `config.custom_model_max_tokens` | `-1` | merge into one required `llm.max_context_tokens` — no registry means no second knob needed |
| `MAX_TOKENS` registry (code) | ~400 static entries | drop (see DESIGN-QUESTION) |
| `config.model_token_count_estimate_factor` | `0.3` | keep (`llm.token_estimate_factor`); with a universal o200k estimate this is the main correctness margin |
| `config.image_input_token_allowance` | `4096` | drop for v1 (no image input) |
| `config.ai_timeout` | `120` | keep (`llm.timeout_seconds`) |
| `OUTPUT_BUFFER_TOKENS_SOFT/HARD` (code constants) | `1500` / `1000` | keep as constants initially; consider one `llm.output_reserve_tokens` that scales both |
| `get_output_token_reserve` handler hook | per-handler | simplify to optional `llm.output_reserve_tokens` config; per-model hooks are a LiteLLM-era artifact |
| `clip_tokens` safety factor | `0.9` (code) | keep as constant |
| framing allowances | `16`/`16` (code) | keep as constants |

**Risks**

- One global tokenizer against arbitrary OpenAI-compatible backends means systematic undercounting for some models; without the estimate factor (or with it set to 0) requests can 400 on context overflow. Keep the factor default conservative and surface overflow errors as "reduce max_context_tokens" guidance.
- Upstream's silent 32000 default clamp is a classic surprise ("why is my 1M-context model truncating at 32k?"); whatever default review-mcp picks for the cap must be loudly documented — or required, with no default.
- Fail-open behaviors (`clip_tokens` returning unclipped text on bad input, count errors → 0 prompt tokens) trade crashes for possible context overflows; decide per-site and keep the choice deliberate, not accidental.
- `prompt_tokens` counts rendered templates — if the Go port counts raw templates (with `{{placeholders}}`) the scaffolding cost will be wrong in both directions; canary-test with a template whose rendered form is much larger than its source.
- Exact-recount loops call the tokenizer O(log n) times over large strings; fine in Go, but keep the verified-prefix search rather than "subtract estimated counts", which is the bug class the upstream code explicitly engineered away.

DESIGN-QUESTION: Model context-window source
Upstream resolves the window from a static registry, then custom config, then LiteLLM metadata. review-mcp has no LiteLLM and a no-embedded-defaults rule. Options: (a) require `llm.max_context_tokens` in config (fail fast at startup if absent); (b) optional with a hardcoded conservative default (e.g. 128000); (c) ship a small model→window table plus config override. Recommendation: (a) — a required, validated integer matches the per-deployment ~250k cap reality, keeps the server generic, and avoids shipping a model registry that goes stale.

DESIGN-QUESTION: Output reserve shape
Upstream layers constant soft/hard buffers (1500/1000) with a per-handler `get_output_token_reserve` override. Options: (a) port both constants and a config override `llm.output_reserve_tokens` (reserve = max(config, 1000), soft = reserve + 500); (b) constants only; (c) fully configurable soft and hard values. Recommendation: (a) — review outputs on large PRs can exceed 1500 tokens on verbose models, so one user-visible knob is worth having, while the soft/hard split stays an internal invariant.

DESIGN-QUESTION: Accurate counting endpoint
Some OpenAI-compatible gateways expose no token-count API, and the OpenAI API itself has none. Options: (a) estimates only (tiktoken o200k + factor), no network counting; (b) optional provider count endpoint configured by the user. Recommendation: (a) for v1 — deterministic, offline, testable; the estimate factor absorbs tokenizer mismatch, and nothing in the clipping pipeline needs network-exact counts.

## C. Prompt assembly & output schemas (/review, /ask, /describe, /improve)

### /review

**Mechanism**

Prompt templates are Jinja2 strings (rendered with `StrictUndefined` in a sandboxed environment) stored in TOML (`[pr_review_prompt] system / user` in `pr_reviewer_prompts.toml`), loaded into settings by `config_loader.py`. `PRReviewer.__init__` builds a `self.vars` dict; `TokenHandler` renders system+user with those vars for token counting, and the final render happens per model attempt with the diff fitted into the budget.

*System message* (prose summary of template sections, in order):
1. Role statement: "You are PR-Reviewer…", review only new code (`+` lines), only issues introduced by this PR.
2. Diff-format explainer: injected as `{{ diff_hunk_format }}`, itself a rendered sub-template from `prompt_fragments.toml` (`__new hunk__` / `__old hunk__` sections, line numbers before the change marker, optional AI-metadata note). For /review it is rendered with `include_line_numbers=True`.
3. "Determining what to flag" rules (thorough on bugs/security, certain on low severity, discrete and actionable, no speculation, uncertain-but-high-impact allowed with an explicit uncertainty note).
4. "Constructing comments" style rules (direct, accurate severity, concise, no praise/filler).
5. Optional blocks, each guarded by a Jinja conditional: `skills_context` (organizational standards), `extra_instructions` (user), `repo_context` (contents of AGENTS.md-like files).
6. The output schema, written **as Pydantic class definitions in prose** (`class Review(BaseModel): …`), with each optional field wrapped in a Jinja conditional on its `require_*` var — the schema the model sees contains only the enabled fields.
7. A fenced `Example output:` YAML skeleton, with the same conditionals.
8. Closing instruction: answer must be valid YAML, each value after a newline with a block scalar indicator `|`.

*User message* sections: related tickets block (URL/title/labels/body/requirements per ticket) + an omission notice if tickets were trimmed for budget; `--PR Info--` with today's date, title, branch, PR description (trimmed, fenced with `======`); optional question/answer block (answer mode, `/review -answer`); `The PR code diff:` with `{{ diff }}`; optionally a duplicate of the example output (`duplicate_prompt_examples`); and the final line `Response (should be a valid YAML, and nothing else):` followed by an **open** ` ```yaml ` fence — the model is expected to continue inside the fence (this matters for parsing: replies usually carry only a closing fence).

*How settings alter the prompt:* every `require_*` toggle both adds the field to the rendered schema and is re-checked at validation time (`_validate_review_schema` requires the field to be present when its toggle is on). `num_max_findings` is interpolated into the `key_issues_to_review` field description ("0-{{num_max_findings}} issues"). `extra_instructions` is injected verbatim into the system prompt. **Answer language** is not a prompt variable: `PRAgent.handle_request` (`pr_agent/agent/pr_agent.py:354`) checks `config.response_language`; when it is not `en-us` it appends a fixed English sentence to every tool section's `extra_instructions` ("Your response MUST be written in the language corresponding to locale code: '<code>'. … Keep schema control values (such as 'No', 'Yes', 'None', 'false') in their original English form"). That last clause pairs with the schema: `security_concerns` explicitly instructs the literal English `No` even under a translation instruction, and `is_value_no()` checks rely on it.

*Structured output schema* (`review:` root mapping; optional fields appear only when their toggle/condition is on):

| field | type | meaning / gate |
|---|---|---|
| `ticket_compliance_check` | `List[TicketCompliance]` | per-ticket requirements vs PR; only when `related_tickets` non-empty |
| `estimated_effort_to_review_[1-5]` | int 1–5 (note: the bracket is part of the key name) | review effort; `require_estimate_effort_to_review` |
| `risk_level` | literal `low\|medium\|high` | `require_risk_assessment` |
| `merge_recommendation` | literal `safe_to_merge\|merge_with_caution\|changes_required` | `require_merge_recommendation` |
| `review_priority_files` | `List[str]` | `require_priority_files` |
| `contribution_time_cost_estimate` | `{best_case, average_case, worst_case}` strings like "45m", "5h" | `require_estimate_contribution_time_cost` |
| `score` | int 0–100 | `require_score` |
| `relevant_tests` | literal `Yes\|No` | `require_tests` |
| `insights_from_user_answers` | str | answer mode only (`question_str` set) |
| `key_issues_to_review` | `List[KeyIssuesComponentLink]` | **always present**; the core finding list |
| `security_concerns` | str (`No` or headed explanation) | `require_security_review` |
| `todo_sections` | `List[TodoSection]` or the string `No` | `require_todo_scan` |
| `can_be_split` | `List[SubPR]` max 3 | `require_can_be_split_review` |

`KeyIssuesComponentLink` = `{relevant_file: str, issue_header: str (1-2 words, e.g. "Possible Bug"), issue_content: str (no line numbers in text), start_line: int, end_line: int}`. `TicketCompliance` = `{ticket_url, ticket_requirements, fully_compliant_requirements, not_compliant_requirements, requires_further_human_verification}` (all str). `TodoSection` = `{relevant_file, line_number, content}`. `SubPR` = `{relevant_files: List[str], title}`.

There is a parallel strict Pydantic model set in `output_models.py` (`PRReview`/`Review` etc., `extra="forbid"`, `StrictInt` with ranges, literal values stripped before validation). Validation failure only logs a warning — the review is still published (the schema check is telemetry/quality signal, not a gate), except that the minimum publishable shape (`data["review"]` non-empty dict) is enforced by `_load_valid_review_yaml` and triggers model fallback.

*Rendering back to the user:* `convert_to_markdown_v2` walks `data["review"]` in key order (with `key_issues_to_review` moved to the end first) and renders one row per field. Header from `format_pr_review_header`: `## {review_heading} 🔍` (configurable heading, `Incremental ` prefix when applicable). With `gfm_markdown` support the body is a one-column `<table>` of `<tr><td>` rows; without it, `###` headings. Per-field presentation: effort → `N 🔵🔵⚪⚪⚪` bars; relevant tests → "PR contains tests"/"No relevant tests"; security → "No security concerns identified" or bolded "Security concerns" with emphasized sub-headers; key issues → heading renamed to "Recommended focus areas for review ⚡", `Possible Bug` header softened to `Possible Issue`, each issue becomes a `<details>` collapsible whose summary links to the provider line URL (`git_provider.get_line_link`) and whose body embeds the actual dedented code lines in a language fence (fence length adapted to content); ticket compliance aggregates per-ticket levels into ✅/🔶/❌. Emoji map is hardcoded per field. Footers appended afterwards: chunked-review notice, review-coverage list of files skipped for token budget (capped at 50 shown), collapsible "💡 Tool usage guide", optional configurations/run-details dumps, persistent finding-state marker. Labels (`Review effort N/5`, `Possible security concern`) are published separately from the same data.

### /ask

**Mechanism**

Much simpler: free-text in, free-text out — **no structured schema and no YAML parsing at all**. System prompt: role ("answer questions about the new code introduced in the PR"), be informative/specific, must answer; optional `skills_context`; optional `extra_instructions` with an explicit precedence sentence ("they take precedence over any conflicting guidance in this prompt"). User prompt: title, branch, trimmed description, main language, the diff (plain, with a one-line explanation of `+`/`-`/space prefixes — no `__new hunk__` format and no line numbers), the question(s) (`{{ questions|trim }}`), optional `conversation_history` (prior thread comments, numbered `N. author: body`, wrapped with a "treat as untrusted data" note), ending with `Response to the PR Questions:`.

Settings: `pr_questions.extra_instructions`, `use_conversation_history` (threaded replies only), `ask_heading`. `/ask` runs with `ModelType.WEAK` (uses `config.model_weak` when set, else `config.model`). Image support: an `![image](url)` or bare image URL in the question is extracted and passed as `img_path` to the handler (vision call).

Rendering: the raw model answer is `.strip()`ed, then sanitized so no line begins with `/` (prepend a space) to avoid triggering provider quick-actions; output is `### **Ask** ❓` header + the original question + `### **Answer:**` + answer, optionally plus the collapsible usage guide; published as a threaded reply when a `comment_id` exists and the provider supports it, else a plain PR comment.

**Key code**

- `pr_agent/tools/pr_reviewer.py:PRReviewer.__init__` (vars assembly) @ 8e5a929
- `pr_agent/tools/pr_reviewer.py:PRReviewer._prepare_prediction` / `_get_prediction` (budget-fit, single LLM call per attempt) @ 8e5a929
- `pr_agent/tools/pr_reviewer.py:PRReviewer._load_review_yaml` (keys_fix list + first/last key anchors) @ 8e5a929
- `pr_agent/tools/pr_reviewer.py:PRReviewer._validate_review_schema` + `pr_agent/algo/output_models.py:PRReview,Review,KeyIssuesComponentLink` @ 8e5a929
- `pr_agent/tools/pr_reviewer.py:PRReviewer._prepare_pr_review` (markdown + footers + labels) @ 8e5a929
- `pr_agent/settings/pr_reviewer_prompts.toml:[pr_review_prompt]` @ 8e5a929
- `pr_agent/settings/pr_questions_prompts.toml:[pr_questions_prompt]` @ 8e5a929
- `pr_agent/algo/prompt_fragments.py:render_diff_hunk_format` + `pr_agent/settings/prompt_fragments.toml:diff_hunk_format` @ 8e5a929
- `pr_agent/algo/utils.py:convert_to_markdown_v2` (+ `extract_relevant_lines_str`, `ticket_markdown_logic`) @ 8e5a929
- `pr_agent/algo/comment_identity.py:format_pr_review_header,format_pr_questions_header` @ 8e5a929
- `pr_agent/agent/pr_agent.py:handle_request` (response_language → extra_instructions injection) @ 8e5a929
- `pr_agent/tools/pr_questions.py:PRQuestions._prepare_prediction,_prepare_pr_answer` @ 8e5a929

**Data flow**

/review: provider → `vars` (title/branch/description/commits/tickets/toggles) → `TokenHandler(system_tmpl, user_tmpl, vars)` → per model attempt: fit tickets + diff to token budget → Jinja render → `ai_handler.chat_completion(model, temperature=config.temperature, system, user)` → raw text → `load_yaml` (area D) → dict → optional schema validation (warn-only) → optional inline-comment publication of key issues → `convert_to_markdown_v2` → footers → persistent comment publish + labels.

/ask: provider + question (+ thread history, + optional image) → vars → budget-fit history then diff → render → `chat_completion` (WEAK model, same temperature) → raw markdown answer → sanitize leading `/` → header+question+answer → threaded or plain comment.

**Porting notes (Go)**

- Keep the TOML-stored, template-driven prompt pair (system/user) with a Go template engine (`text/template` suffices; the templates use only conditionals, loops over tickets, and `trim`). Port the conditional-schema idea: render the schema section from the same toggles that later gate validation, so prompt and validator can never drift — in Go, generate both from one slice of field descriptors (name, YAML type text, description, toggle).
- The "open ```yaml fence at the end of the user prompt" trick is load-bearing for parse behavior; keep it and mirror the fence-stripping logic (area D) exactly.
- `estimated_effort_to_review_[1-5]` is a hostile key name (brackets) — as an MCP server we control both sides; renaming to `estimated_effort_to_review` with the 1–5 range in the description is safe since we don't need PR-Agent comment compatibility. Same for dropping fields we don't port.
- v1 scope suggestion: always-on core = `key_issues_to_review` + `security_concerns` + `relevant_tests` + `estimated_effort_to_review`; keep `require_*` toggles for these four; defer tickets/todo/can_be_split/time-cost/priority-files/score (tickets need a tracker integration we don't have in v1).
- MCP output is text returned to the client, not a PR comment: keep `convert_to_markdown_v2`'s structure (header, per-field sections, collapsible issues) but make GFM-vs-plain a renderer option; line links require the provider's line-URL builder (Gitea/Bitbucket Server formats differ — implement `GetLineLink` per provider). Embedding the actual code lines under each finding (`extract_relevant_lines_str`) is high-value; it needs head-file content fetch per flagged file.
- Output language: adopt PR-Agent's approach verbatim — one English instruction appended to extra_instructions naming the locale code, plus the "keep schema control values in English" clause; config key `output_language`, default `en-US`. Do not translate schema keys or literals.
- /ask: port the `/`-prefix sanitation (Gitea/GitLab-style quick actions) and the untrusted-data framing around conversation history.
- Validation: implement the strict struct (extra fields rejected, int ranges) but keep it warn-only as upstream does, except for the "non-empty `review` mapping" gate which must trigger fallback.

**Config knobs**

| upstream key | default | keep/drop/rename for Go config |
|---|---|---|
| `config.temperature` | 0.2 | keep (`llm.temperature`) |
| `config.seed` | -1 | keep optional (`llm.seed`; enforce temperature==0 when set, as upstream) |
| `config.response_language` | "en-US" | **rename** `output_language` (per-tool override allowed), default en-US, no embedded endpoint defaults affected |
| `config.max_output_tokens` | 0 (unset) | keep |
| `config.model` / `config.fallback_models` | gpt-5.6 / [gpt-5.6-terra] | keep keys, **no model defaults** (user must configure; aligns with no-embedded-defaults rule) |
| `config.model_weak` | unset | keep optional (`/ask` uses it); fall back to `model` |
| `config.duplicate_prompt_examples` | false | drop (marginal; revisit if weak models misformat) |
| `pr_reviewer.num_max_findings` | 3 | keep |
| `pr_reviewer.extra_instructions` | "" | keep (per-tool) |
| `pr_reviewer.require_tests_review` | true | keep |
| `pr_reviewer.require_security_review` | true | keep |
| `pr_reviewer.require_estimate_effort_to_review` | true | keep |
| `pr_reviewer.require_score_review` | false | drop v1 |
| `pr_reviewer.require_risk_assessment` / `require_merge_recommendation` / `require_priority_files` | false | defer (nice v1.1 candidates; cheap to add with descriptor table) |
| `pr_reviewer.require_can_be_split_review`, `require_todo_scan`, `require_estimate_contribution_time_cost`, `require_ticket_analysis_review` | false/false/false/true | drop v1 (tickets need tracker integration) |
| `pr_reviewer.review_heading` | "PR Reviewer Guide" | keep |
| `pr_reviewer.enable_intro_text`, `enable_help_text` | true/false | drop (MCP client renders; no usage-guide footer) |
| `pr_reviewer.enable_review_labels_effort/security`, `persistent_comment`, `persistent_finding_state`, `inline_key_issues`, incremental-review keys | various | drop v1 (comment/label publication is out of scope for an MCP tool that returns text) |
| `pr_reviewer.enable_large_pr_chunking` / `max_number_of_calls` | false / 3 | defer (see D risks) |
| `pr_questions.extra_instructions` | "" | keep |
| `pr_questions.use_conversation_history` | true | keep if pr_ask accepts a thread/comment anchor; else drop |
| `pr_questions.ask_heading` | "Ask" | keep |
| `pr_questions.enable_help_text` | false | drop |

**Risks**

- Jinja `StrictUndefined`: every referenced var must exist. In Go, `text/template` with `missingkey=error` replicates this; without it, silent empty sections will pass tests and degrade prompts.
- The schema is prompt-prose, not provider-enforced JSON mode. Weak/local OpenAI-compatible models (our target) misformat YAML far more than GPT-class models — the repair chain in area D is not optional polish, it is the main correctness lever.
- `security_concerns`'s "answer the exact English literal 'No'" contract is what makes `is_value_no` and the security label work under translated output; losing that sentence breaks the No-detection in non-English configs.
- `convert_to_markdown_v2` silently drops empty fields except three listed keys; porting by field-loop requires reproducing that exemption list or "no issues" reviews render empty.
- Upstream renders `<table>`/`<details>` HTML that Gitea and Bitbucket Server render differently (Bitbucket Server has limited HTML support; upstream gates on `is_supported("gfm_markdown")`). Our renderer must take the capability flag from the provider layer.

DESIGN-QUESTION: Output format of pr_review MCP tool
- Question: should pr_review return (a) rendered markdown only, (b) the parsed structured object only, or (c) both (markdown + structured JSON in a separate content item)?
- Options: (a) simplest, matches chat clients; (b) lets the MCP client render; (c) dual cost ~0, clients choose.
- Recommendation: (c) — return markdown as primary text content plus the validated struct as a JSON resource/metadata field; upstream's `publish_structured_review` hook shows the data is already shaped for this.

DESIGN-QUESTION: YAML vs JSON as the model output format
- Question: upstream asks for YAML with block scalars because multi-line code in JSON strings breaks escaping; do we keep YAML?
- Options: (1) YAML + full repair chain (upstream-proven across many models); (2) JSON + provider `response_format=json_object` where supported (cleaner parse, but optional on OpenAI-compatible endpoints and absent on many local servers); (3) JSON with YAML fallback.
- Recommendation: (1) keep YAML and port the repair chain; it is the battle-tested path for arbitrary OpenAI-compatible endpoints and the schema's block-scalar style is designed for it. Record attribution to PR-Agent (MIT) for schema + prompts.

### /describe

Added for v2 (WP-2c, X-26, design note Y-4 to Y-7). Same pinned revision
(`8e5a929`); the study covered `pr_agent/settings/pr_description_prompts.toml`
and `pr_agent/tools/pr_description.py` only.

**Mechanism**

`PRDescription.__init__` builds `self.vars`: the PR title, the source branch
(`get_pr_branch`), the description (`get_pr_description(full=False)`), the
commit messages (`get_commit_messages`), the main language, `extra_instructions`,
`skills_context`, `repo_context`, related tickets, and the toggles
`enable_custom_labels`, `enable_semantic_files_types` (forced off when the
provider lacks `gfm_markdown`), `include_file_summary_changes` (true only for
at most `collapsible_file_list_threshold` = 8 files), `enable_pr_diagram`,
`enable_pr_description`, `duplicate_prompt_examples`. `keys_fix` for the YAML
repair is `filename:`, `language:`, `changes_summary:`, `changes_title:`,
`description:`, `title:`. The run uses `ModelType.WEAK`.

*Prompt* (`[pr_description_prompt]`): system = role ("You are PR-Reviewer…"),
the task sentence (type, description, title, files walkthrough), instruction
lines (focus on `+` lines; previous title/description/commits are only a
reference; prioritise significant changes; block scalars; backticks; `- `
bullets), the optional skills / extra-instructions / repo-context blocks, the
schema as Pydantic prose (`PRType` enum `Bug fix|Tests|Enhancement|Documentation|Other`,
optional custom-labels class, `FileDescription{filename, changes_summary?,
changes_title, label}`, `PRDescription{type, description?, title,
changes_diagram?, pr_files?}` with `pr_files` `max_items=20`), a YAML
example and the closing YAML instruction. User = related tickets, `PR Info:`
with `Previous title`, `Previous description` (fenced `=====`), `Branch`,
`Commit messages` (fenced), the plain diff with its one-line prefix
explanation, the optional duplicated example, and the final line followed by
an open ` ```yaml ` fence.

*Large PRs* (`enable_large_pr_handling`, default true): when
`get_pr_diff(..., large_pr_handling=True)` returns no single diff, the diff is
split with `get_pr_diff_multiple_patchs`; each patch is sent with the prompt
set `pr_description_only_files_prompts` (files walkthrough only), the
answers' `pr_files` YAML texts are concatenated, a list of unprocessed and
deleted files is appended, and one more call with
`pr_description_only_description_prompts` produces the headers (type, title,
description) from that walkthrough text. Both prompt sets live in other TOML
files that were **not** part of this study. A failed chunk is skipped (its
files listed in a coverage footer); all chunks failing raises.

*Coverage padding:* `extend_uncovered_files` appends every changed file the
model did not return to `pr_files` with `changes_title: ...` and the label
`additional files` (capped at 100, then one "Additional files not shown"
entry), so the published walkthrough lists files the model never described.

*Validation:* `_validate_description_schema` checks `PRDescriptionAssembled`
and only logs a warning; labels come from `type` (comma-split when a string)
and are published separately.

**Key code**

- `pr_agent/settings/pr_description_prompts.toml:[pr_description_prompt]` @ 8e5a929
- `pr_agent/tools/pr_description.py:PRDescription.__init__` (vars, `keys_fix`, `COLLAPSIBLE_FILE_LIST_THRESHOLD`) @ 8e5a929
- `pr_agent/tools/pr_description.py:PRDescription.run` (`ModelType.WEAK`, publish paths, labels) @ 8e5a929
- `pr_agent/tools/pr_description.py:PRDescription._prepare_prediction` (one call, large-PR chunks, headers call) @ 8e5a929
- `pr_agent/tools/pr_description.py:PRDescription._get_prediction` (budget fit per prompt set) @ 8e5a929
- `pr_agent/tools/pr_description.py:PRDescription.extend_uncovered_files` @ 8e5a929
- `pr_agent/tools/pr_description.py:PRDescription._get_description_coverage_footer` @ 8e5a929
- `pr_agent/tools/pr_description.py:PRDescription._prepare_data,_validate_description_schema` @ 8e5a929
- `pr_agent/tools/pr_description.py:PRDescription._prepare_labels,_prepare_file_labels` @ 8e5a929
- `pr_agent/tools/pr_description.py:PRDescription._prepare_pr_answer,_prepare_pr_answer_with_markers,process_pr_files_prediction` (rendering; as implemented in WP-2d, see the porting notes) @ 8e5a929
- `pr_agent/tools/pr_description.py:sanitize_diagram,apply_diagram_direction` (mermaid diagram; not ported) @ 8e5a929

**Data flow**

provider → vars (title, branch, description, commits) → plain diff fitted to
the budget → one call (`pr_description_prompt`) → `load_yaml` with `keys_fix`
→ `extend_uncovered_files` → markdown (title, type, description, walkthrough
table) → publish (description edit, or comment) + labels. Large PR: chunks →
files-only calls (concurrent) → concatenated walkthrough → headers call →
merged YAML.

**Porting notes (Go)** — as implemented in `internal/describe` (v2 spec §3)

- Templates `internal/describe/prompts/system.tmpl` and `user.tmpl` adapt
  `[pr_description_prompt]` with the upstream toggles fixed (semantic file
  types on, file summaries always on, description on, diagram and custom
  labels off); their header comments list every deviation (no diagram,
  ticket, skills, repo-context blocks; the extra-instructions block only
  carries the output-language instruction; no `max_items=20`; an honesty line
  against describing files listed by name only; a `Target branch` line; the
  part-mode variant and its part line).
- Large PRs reuse pr_review's X-19 packing (`diffpipe.PrepareChunks` when
  the diff leaves files out and `review.max_chunks > 1`). Parts ask for
  `pr_files` only (the part-mode variant of the same templates, not
  upstream's `pr_description_only_files_prompts`); the reduce call
  (`reduce_system.tmpl`, `reduce_user.tmpl`, written for review-mcp, not
  adapted from `pr_description_only_description_prompts`) receives the
  merged walkthrough (path, title, summary) and no diff. Parts run
  sequentially, as pr_review's do.
- `extend_uncovered_files` is deliberately **not** ported (Y-7): a file the
  model did not return is reported as not described (coverage reason
  `not_returned`), never padded into the walkthrough.
- Validation is enforced, not warn-only: types outside the enum are dropped
  with a note, walkthrough entries for a path the call was not shown are
  dropped (per part: that part's own files), the first entry of a path wins,
  labels are one line of at most 40 characters.
- `keys_fix` is kept without `language:` (not in the schema); no first/last
  key, as upstream's describe path passes none.
- One model: review-mcp has no weak-model setting; describe uses `llm.model`.
- Publishing (WP-2d, X-26): upstream's description markers
  (`use_description_markers`) and its overwrite of the description are not
  ported; review-mcp owns only a region between `[//]: # (review-mcp:describe:start)`
  and `[//]: # (review-mcp:describe:end)`, appended after the author's text and
  replaced on later runs, with the text outside it untouched
  (`publish_mode=description`). The comment path
  (`publish_description_as_comment`) is `publish_mode=comment`, one comment
  edited in place by a marker (`[//]: # (review-mcp:describe:v1)`) and the
  author check of X-12. `generate_ai_title` is the `update_title` argument.
  Labels are not published (Y-4). The published text is escaped as
  `pr_review` escapes its findings, and the Bitbucket Server update sends the
  reviewer list back, as this study's note on upstream's PR update requires.
  Guide: `docs/describe.md`.
- Commit messages: numbered `N. message` (upstream's GitHub provider format),
  clipped to `diff.max_commits_tokens` (default 500).

**Config knobs**

Defaults are given only where `pr_description.py` states them (a `.get`
fallback); the others live in `configuration.toml`, which this study did not
cover ("n/s").

| upstream key | default | keep/drop/rename for Go config |
|---|---|---|
| `pr_description.enable_large_pr_handling` | true | replaced by `review.max_chunks` (> 1 enables parts, X-19) |
| `pr_description.enable_semantic_files_types` | n/s | always on (no key) |
| `pr_description.collapsible_file_list_threshold` | 8 | drop (summaries always asked for) |
| `pr_description.enable_pr_diagram` | false | drop |
| `pr_description.enable_pr_description` | true | always on (no key) |
| `pr_description.publish_labels` / `config.enable_custom_labels` | n/s | drop (labels are not set in v2, Y-4) |
| `pr_description.extra_instructions` | n/s | drop (no argument or key; spec §3.7) |
| `pr_description.publish_description_as_comment` (+`_persistent`) | n/s | `publish_mode=comment` (WP-2d; always edited in place) |
| `pr_description.generate_ai_title` | n/s | `update_title` argument (WP-2d) |
| `pr_description.use_description_markers` | n/s | replaced by the managed region markers (WP-2d) |
| `pr_description.add_original_user_description` | n/s | n/a: the author's text is never replaced (Y-5) |
| `config.max_commits_tokens` | unset | `diff.max_commits_tokens` (500) |

**Risks**

- Upstream's `pr_files` description says "all the files", while upstream
  caps the list at 20 and pads the rest; models trained on the upstream
  prompt may still stop early. The `not_returned` coverage reason makes that
  visible instead of hiding it.
- The reduce prompt is ours; its quality on weak models is unmeasured until
  the in-use acceptance (N1).

### /improve

Added for v2 (WP-2f, X-27, design note Y-8 and Y-9). Same pinned revision
(`8e5a929`); the study covered
`pr_agent/settings/code_suggestions/pr_code_suggestions_prompts.toml`,
`pr_code_suggestions_reflect_prompts.toml` and
`pr_agent/tools/pr_code_suggestions.py`. The anchoring and publishing side of
the tool is section E; this part covers the two prompts, the parsing and the
merge.

**Mechanism**

`PRCodeSuggestions.__init__` builds `self.vars`: the title, the branch, the
description, the main language, empty `diff` and `diff_no_line_numbers`,
`num_code_suggestions` (`num_code_suggestions_per_chunk`, 3 when it is not a
number), `extra_instructions`, `skills_context`, `repo_context`,
`suggestion_discussion_context` (prior code-suggestion threads as JSON),
`commit_messages_str`, `diff_hunk_format` (the fragment rendered without line
numbers), `focus_only_on_problems` (code default false; the shipped
configuration sets true) and today's `date`. `decouple_hunks` (code default
true; the shipped configuration sets false) picks the prompt set:
`[pr_code_suggestions_prompt]` (decoupled view) or
`[pr_code_suggestions_prompt_not_decoupled]` (plain diff; not studied).

*Suggestion prompt* (`[pr_code_suggestions_prompt]`): system = role ("You
are PR-Reviewer, an AI specializing in ... code analysis and suggestions"),
the task sentence (two variants by `focus_only_on_problems`), the
diff-format fragment, "Specific guidelines" (up to `num_code_suggestions`
suggestions, only on `+` lines of `__new hunk__`, the do-not list, imports,
partial-code caution, backticks), the optional skills / extra-instructions /
repo-context blocks, the schema as Pydantic prose (`CodeSuggestion{relevant_file,
language, existing_code, suggestion_content, improved_code,
one_sentence_summary, label}` with two `label` variants, and
`PRCodeSuggestions{code_suggestions: List[CodeSuggestion]}`), a YAML example
and the closing YAML instruction. User = `--PR Info--` with the title and the
date, `The PR Diff:` fenced with `======` (`diff_no_line_numbers`), the
optional discussion block (with its instruction paragraph after the data),
the optional duplicated example, and the final line followed by an open
` ```yaml ` fence. No line numbers are asked for.

*Diff*: `prepare_prediction_main` gets the numbered decoupled view in up to
`max_number_of_calls` chunks (`get_pr_multi_diffs(add_line_numbers=True)`)
and strips the numbers for the suggestion call (`remove_line_numbers`); with
`decouple_hunks` false it gets the plain chunks and builds the numbered twin
for the self-review (`convert_to_decoupled_with_line_numbers`). A chunk must
fit whole (`FallbackEligibleError` otherwise).

*Parsing* (`_prepare_pr_code_suggestions`): `load_yaml` with `keys_fix_yaml`
`relevant_file`, `suggestion_content`, `existing_code`, `improved_code`,
`first_key="code_suggestions"`, `last_key="label"`; a list answer is wrapped;
an answer without a `code_suggestions` list counts as a parse failure of the
chunk. An entry without `one_sentence_summary`, `label` or `relevant_file`,
or without `existing_code` and `improved_code`, is skipped; so is a
duplicate `one_sentence_summary` within the chunk and the "const instead of
let" boilerplate. With `focus_only_on_problems`, a label containing
"critical" becomes "possible issue".

*Self-review* (`self_reflect_on_suggestions`, mandatory): one call per chunk
with `[pr_code_suggestions_reflect_prompt]`: the numbered diff and the
chunk's suggestions as `suggestion N: <Python dict repr>` paragraphs; the
answer (`code_suggestions` list of `suggestion_summary`, `relevant_file`,
`relevant_lines_start`, `relevant_lines_end`, `suggestion_score` 0-10,
`why`) is matched to the suggestions by position, only when the counts are
equal (`analyze_self_reflection_response`); an empty or failed self-review
gives every suggestion score 7 and no line range. A self-review request whose
diff does not fit whole is not sent.

*Merge*: chunks in order; a suggestion below `suggestions_score_threshold`
is dropped (logged only); `_limit_suggestions_per_file` caps per file. There
is no total cap and no cross-chunk deduplication.

**Key code**

- `pr_agent/settings/code_suggestions/pr_code_suggestions_prompts.toml:[pr_code_suggestions_prompt]` @ 8e5a929
- `pr_agent/settings/code_suggestions/pr_code_suggestions_reflect_prompts.toml:[pr_code_suggestions_reflect_prompt]` @ 8e5a929
- `pr_agent/tools/pr_code_suggestions.py:PRCodeSuggestions.__init__` (vars) @ 8e5a929
- `pr_agent/tools/pr_code_suggestions.py:PRCodeSuggestions.prepare_prediction_main,_get_prediction` (chunks, generation, mandatory self-review) @ 8e5a929
- `pr_agent/tools/pr_code_suggestions.py:PRCodeSuggestions._prepare_pr_code_suggestions` (parsing and filters) @ 8e5a929
- `pr_agent/tools/pr_code_suggestions.py:PRCodeSuggestions.self_reflect_on_suggestions,_self_reflect_with_fallback,analyze_self_reflection_response` @ 8e5a929

**Data flow**

provider → vars → numbered chunks (≤ `max_number_of_calls`) → per chunk:
suggestion call on the unnumbered twin → YAML → filters → self-review call
on the numbered chunk with the suggestions → scores and line ranges by
position → threshold → per-file cap → publish (section E).

**Porting notes (Go)** — as implemented in `internal/improve` (v2 spec §1)

- Templates `internal/improve/prompts/system.tmpl`, `user.tmpl`,
  `reflect_system.tmpl` and `reflect_user.tmpl` adapt the two prompt
  sections, with `focus_only_on_problems` fixed to true (the shipped
  configuration) and `num_code_suggestions` = `improve.max_suggestions_per_part`;
  their header comments list every deviation. In short: the skills and
  repo-context blocks are removed, the extra-instructions block carries only
  the output-language instruction (also in the self-review prompt, which
  upstream does not have, so the `why` texts follow the output language),
  the "Use ellipsis (...) for brevity" sentence of `existing_code` is
  removed (WP-2g compares the quoted code with the head file), an honesty
  line keeps suggestions off files listed by name only, the user prompt
  adds the branches and the description, pr_review's discussion block
  (X-13) replaces `suggestion_discussion_context`, the X-22 repository
  context and the X-19 part line are added as in pr_review, and the
  self-review schema gains `suggestion_number`; the suggestions are listed
  as `suggestion N: <JSON object>` (one line each) instead of a Python repr.
- **Diff format.** `diffpipe.ModeNumbered` is upstream's decoupled numbered
  view byte for byte (its goldens are upstream outputs: `## File: '<path>'`,
  `__new hunk__` with new-file numbers and the change marker after the
  number, `__old hunk__` unnumbered and omitted without removed lines, the
  deleted-file line). review-mcp sends that same numbered text to both calls
  of a part, where upstream strips the numbers for the suggestion call: one
  preparation, no `remove_line_numbers` heuristic, no pair to drift (this
  map's DESIGN-QUESTION "Diff representation for the Go port" recommended
  one view for both calls). The suggestion prompt therefore describes the
  numbered format (the fragment rendered with `include_line_numbers=True`, as
  in pr_review), and the schema still asks for no line numbers.
- Parts reuse pr_review's X-19 packing (`diffpipe.PrepareChunks` when the
  diff leaves files out and `review.max_chunks > 1`); the parts run
  sequentially, each followed by its own self-review on the part's diff and
  suggestions. The diff budget reserves the larger of the suggestion
  scaffolding and the self-review scaffolding plus the hard output reserve,
  so a packed part still leaves room for its self-review; a self-review
  request that does not fit all the same is not sent and counts as failed.
- Parsing keeps upstream's repair keys and the "critical" relabel. An answer
  with `code_suggestions: null` is an answer without suggestions; without
  the key it gets the one re-ask (as every review-mcp call). Validation is
  enforced and counted in notes: an entry without a file, a summary or the
  code is dropped, a suggestion for a file its call was not shown is
  dropped, existing code equal to improved code after whitespace folding is
  dropped ("no change"), labels are one line of at most 40 characters. The
  "const instead of let" filter and the within-chunk summary dedup are not
  ported; the fingerprint dedup below replaces the latter.
- The self-review is matched by `suggestion_number`, checked against the
  file and the summary; conflicting or out-of-range numbers match nothing;
  positions are a fallback only when the counts are equal (upstream's rule).
  A failed self-review keeps the suggestions **unscored** (score null) with
  a fixed note, never upstream's default 7; a suggestion the answer does not
  score is unscored too. Scores below `improve.min_score` are dropped and
  counted.
- Merge: part order, then score descending (unscored last in their part);
  X-13 fingerprint dedup over file, summary and existing code
  (`llmrun.Fingerprint`); a total cap `improve.max_suggestions` with a note.
  The per-file cap is not ported.
- Line ranges come from the self-review only; `verified` and `anchor` are
  WP-2g and WP-2h (section E).

**Config knobs**

| upstream key | default | keep/drop/rename for Go config |
|---|---|---|
| `pr_code_suggestions.num_code_suggestions_per_chunk` | n/s (code fallback 3) | `improve.max_suggestions_per_part` (4, 1 to 10) |
| `pr_code_suggestions.suggestions_score_threshold` | n/s (code fallback 1, at least 1) | `improve.min_score` (7, 0 to 10; 0 keeps every scored suggestion) |
| `pr_code_suggestions.max_number_of_calls` | n/s | replaced by `review.max_chunks` (X-19) |
| — | — | `improve.max_suggestions` (8, 1 to 30): total cap after the merge, ours |
| `pr_code_suggestions.focus_only_on_problems` | n/s (code fallback false) | fixed true (no key) |
| `pr_code_suggestions.decouple_hunks` | n/s (code fallback true) | drop: one numbered view for both calls |
| `pr_code_suggestions.extra_instructions` | n/s | drop (no argument or key) |
| `pr_code_suggestions.max_suggestions_per_file` | n/s | drop |
| `pr_code_suggestions.parallel_calls` | n/s | drop (parts run sequentially, as pr_review's) |
| `config.duplicate_prompt_examples` | n/s | drop |

**Risks**

- The suggestion call sees line numbers that upstream hides from it; a
  model may copy a number into `existing_code`. WP-2g's comparison with the
  head file would then fail and mark the suggestion unverified. Unmeasured
  until the in-use acceptance (P1).
- The self-review prompt is upstream's apart from `suggestion_number`;
  weak models may ignore the number, and then the positional fallback or
  the unscored path applies.

## D. Output parsing & repair path

**Mechanism**

*Parsing pipeline* (`load_yaml`, /review only — /ask publishes raw text):
1. Keep a copy of the original text. Strip surrounding newlines.
2. **Fence stripping:** remove a leading ` ``` ` optionally labeled `yaml`/`yml` (case-insensitive) only when the label is a complete info string (so a key like `yml_config` is not mangled); if no fence matched, strip a bare leading `yaml` prefix.
3. **Sign-off dropping** (`drop_sign_off_after_wrapper_fence`): if a closing ` ``` ` is followed by trailing text, drop fence+tail only when the tail does not look like more of the answer (first line not `key:`-shaped and tail not YAML-parseable as dict/list) and the remaining candidate parses to a dict. This exists because the user prompt ends with an open fence, so the reply typically carries only a closing fence plus an occasional model sign-off.
4. Strip a trailing ` ``` ` line.
5. **Control-char sanitation** (`sanitize_yaml_control_chars`): delete C0 controls except TAB/LF/CR, plus DEL (regex `[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]`); deliberately preserve C1 `\x80-\x9f` so the mojibake repair (tactic 10) can still work.
6. `yaml.safe_load`. A text that became empty purely through preprocessing is routed into the failure path instead of silently returning None.
7. On any failure → `try_fix_yaml` with `keys_fix_yaml` (for /review: `ticket_compliance_check`, `estimated_effort_to_review_[1-5]:`, `risk_level:`, `merge_recommendation:`, `security_concerns:`, `key_issues_to_review:`, `relevant_file:`, `relevant_line:`, `suggestion:`), `first_key='review'`, `last_key='security_concerns'`.
8. `load_yaml` returns `{}` when everything fails; the caller (`_load_valid_review_yaml`) then raises `FallbackEligibleError` ("did not contain a non-empty review mapping"), which the fallback-model loop treats as retryable.

*`try_fix_yaml` repair tactics, in exact order* (each: transform a fresh copy → `yaml.safe_load` → return on non-None success):
1. **Add block scalars to known keys:** for every line containing a known key and no `|`, rewrite `key:` → `key: |\n        ` (forces the value into a block scalar; fixes unquoted strings with colons/quotes).
2. **(1.5) `|` → `|2`:** replace every `|\n` with `|2\n` (explicit indentation indicator 2; fixes block scalars whose content dedents below the parent).
3. **(continuation of 1.5) brace re-indent:** on the `|2` text, for lines with exactly 2 leading spaces containing `}`: a standalone `}` is indented into the preceding block scalar only when that scalar contains an opening brace (else the line is deleted); other `}` lines get 4 extra spaces. Then parse.
4. **Extract fenced snippet:** regex-extract the body between the first ` ```yaml` (or bare ` ``` `) and the last ` ``` ` — tried on the transformed text, then on the original (pre-stripping) text.
5. **Strip curly braces:** remove one leading `{` and trailing `}` (plus trailing `:`/newlines) — fixes a model answering in JSON-ish wrapper.
6. **first_key/last_key window:** cut from the first occurrence of `\nreview:` (or `review:`) to the first blank line after the *last* occurrence of `security_concerns:` (falling back to end of text); strip any trailing fence and backticks; parse the window. This rescues YAML embedded in surrounding prose.
7. **Strip leading `+`:** replace a line-leading `+` with a space (models echo diff markers into code blocks).
8. **(5.5) Normalize diff markers:** same `+` fix, plus heuristic cleanup of leading `-` lines — a `- ` line whose second payload char is not space/tab/`+`/`-` is kept as a real YAML list item; otherwise leading `+`/`-` runs are stripped and a space is substituted, so diff "removed" lines inside block scalars stop terminating the scalar.
9. **Tabs → 4 spaces** (only when a tab exists).
10. **Code-section re-indent:** after lines containing section keys from /improve//describe (`existing_code:`, `improved_code:`, `response:`, `why:`, `description:`, `title:`, `changes_diagram:`, `pr_files:`, `pr_ticket:`), indent following lines by 4 until the next known key or block-scalar header; then also ` |`→` |2`. (Mostly relevant for other tools; harmless for /review.)
11. **Root pipe strip:** `lstrip('|\n')` — removes a stray block-scalar pipe before the root mapping.
12. **Encoding round-trips:** re-encode the text as `latin-1` then `utf-16` and decode as UTF-8 (repairs mojibake some models emit).
(A dead "remove last lines one at a time" tactic exists only as commented-out code.)

*Retries and fallbacks* — three nested layers:
- **Layer 1, same model** (`LiteLLMAIHandler._chat_completion_with_retry`): tenacity `stop_after_attempt(MODEL_RETRIES=2)`, retrying only transport-level `openai.APIError`s — never `RateLimitError`, `BadRequestError`, `UnprocessableEntityError`; `APITimeoutError` only when `config.retry_same_model_on_timeout` (default true). Parse failures never reach this layer.
- **Layer 2, model chain** (`retry_with_fallback_models`): builds `[primary] + config.fallback_models` (primary chosen by `ModelType`: /review REGULAR → `config.model`, /ask WEAK → `config.model_weak` else `config.model`; optional small-PR routing can swap the primary), zipped with Azure deployment IDs. For each model it re-runs the tool's whole `_prepare_prediction` (so the diff is re-fitted to the new model's context). It advances to the next model only on `openai.APIError`, `asyncio.TimeoutError`, or `FallbackEligibleError` (raised for: empty diff after budget fit, diff that cannot fit the token limit, and **unparseable/empty review YAML**); any other exception propagates immediately. After the last model it raises an aggregate "Failed to generate prediction with any model of [...]" exception listing per-model errors.
- **Layer 3, chunked review** (opt-in `enable_large_pr_chunking`): per-chunk predictions are gathered concurrently; a chunk whose YAML fails to parse is retried on the next fallback model while successful chunks are cached (`_chunked_results`); if fallbacks run out but some chunks succeeded, the partial merged review is published with a "N chunk(s) failed" notice.
- **Temperature never changes on retry.** Every attempt on every model uses `config.temperature` (default 0.2) verbatim; there is no temperature escalation/decay anywhere in the retry paths (temperature is only *dropped* for models that reject the parameter, or forced to 1 for Claude thinking mode).
- Streaming is not used for parsing purposes: non-streaming by default; streaming is turned on only for models that require it or endpoints forced via `litellm.force_streaming_*` config, and the stream is accumulated back into one string.

*Final failure* (/review): the aggregate exception bubbles into `PRReviewer.run`'s handler → logged, run recorded as failed, and a comment "Failed to review PR" is published; with `pr_reviewer.publish_error_details=true` an allowlist classifier (`_REVIEW_FAILURE_REASONS`) matches the exception-chain text against known patterns (insufficient credits, auth error, rate limit, timeout, context length, connection error, all-models-failed) and appends one fixed, sanitized English sentence — never raw provider text, so no token/URL leakage. Unknown errors get a generic internal-error sentence. `config.propagate_tool_errors` re-raises for programmatic callers. /ask has no parse step; LLM-layer failure propagates out of `run()` (temporary "Preparing answer..." comment is removed in a `finally`).

**Key code**

- `pr_agent/algo/utils.py:load_yaml` @ 8e5a929
- `pr_agent/algo/utils.py:try_fix_yaml` @ 8e5a929
- `pr_agent/algo/utils.py:sanitize_yaml_control_chars` / `drop_sign_off_after_wrapper_fence` / `_looks_like_more_answer` @ 8e5a929
- `pr_agent/tools/pr_reviewer.py:PRReviewer._load_review_yaml,_load_valid_review_yaml` @ 8e5a929
- `pr_agent/algo/pr_processing.py:retry_with_fallback_models,_get_all_models,_get_all_deployments` @ 8e5a929
- `pr_agent/algo/token_budget.py:FallbackEligibleError` @ 8e5a929
- `pr_agent/algo/utils.py:get_model,ModelType` @ 8e5a929
- `pr_agent/algo/ai_handlers/litellm_ai_handler.py:_should_retry_same_model,_chat_completion_with_retry (MODEL_RETRIES=2),_get_completion` @ 8e5a929
- `pr_agent/tools/pr_reviewer.py:_review_failure_comment,_REVIEW_FAILURE_REASONS` @ 8e5a929
- `pr_agent/algo/review_merge.py:merge_review_chunks` @ 8e5a929 (chunk-result reduction; field-specific merge rules with first-non-empty fallback)

**Data flow**

raw LLM string → strip/fence/sign-off/control-char preprocessing → `yaml.safe_load` → (on failure) 12-tactic repair chain, first success wins → dict (or `{}`) → non-empty `review` mapping gate → `FallbackEligibleError` → next (model, deployment) pair re-runs diff-prep + call + parse → success records model used (primary vs fallback) → parsed dict flows to area C rendering. Final failure → classified, sanitized failure comment.

**Porting notes (Go)**

- Use `gopkg.in/yaml.v3` for lenient-ish parsing; note PyYAML and yaml.v3 differ at edges (yaml.v3 accepts tabs in some positions, duplicate-key behavior differs, PyYAML 1.1 bools like `yes/no` — upstream relies on `safe_load` resolving bare `No` to Python `False` in some fields, which `is_value_no` handles by checking bool and string; the Go port must likewise accept `false`, `"no"`, `"none"` in the No-detector).
- Port tactics as an ordered slice of `func(string) (string, bool)` transforms with a shared "transform → unmarshal → return on success" driver, logging which tactic won (upstream logs this; invaluable telemetry against local models). Keep the exact order — later tactics assume earlier ones failed.
- Tactics worth keeping all of: 1, 2 (|→|2), 4 (fenced snippet, incl. retry on original text), 5, 6 (first/last key window), 7/8 (diff-marker cleanup — review output embeds code), 9 (tabs), 11 (root pipe), 12 (mojibake — cheap). Tactic 3 (brace re-indent) and 10 (improve/describe section re-indent) are tuned to other tools' schemas; port behind the same chain but they can be simplified or dropped if canary tests show no coverage for review-shaped output.
- `keys_fix_yaml` for our schema: regenerate from our field descriptor table (every leaf key that takes free text) rather than hardcoding upstream's list, which includes /improve keys.
- Error taxonomy: define a `FallbackEligible` error (wraps parse-empty, diff-does-not-fit, empty-diff) vs terminal errors; map OpenAI-compatible HTTP statuses: 429/5xx/timeout/connection → same-model retry (bounded, 2 attempts) then next model; 400/401/403/422 → straight to next model or terminal. Re-run the full prompt preparation per model (context sizes differ).
- Keep "temperature constant across retries" — do not invent escalation; upstream's evidence is that repair + model fallback suffices.
- Final-failure message: port the allowlist classifier idea 1:1 (patterns → fixed sentences, never raw provider error text) — it is also our token-leak firewall; as an MCP server, return it as a tool error message instead of publishing a comment.
- Chunked review (layer 3) is substantial machinery (chunk planning, resize on fallback, caching, merge rules); defer to post-v1 and instead report "files omitted for token budget" (upstream's coverage footer), which we get almost free from the diff-fit step.

**Config knobs**

| upstream key | default | keep/drop/rename for Go config |
|---|---|---|
| `config.fallback_models` | ["gpt-5.6-terra"] | keep key, **empty default** (no embedded model names) |
| `config.model_weak` | unset | keep optional |
| `config.model_reasoning` | unset | drop v1 (no self-reflection flow ported) |
| `config.ai_timeout` | 120 | keep (`llm.timeout_seconds`) |
| `config.retry_same_model_on_timeout` | true | keep |
| `config.num_retries` (client-internal) | unset | drop (one retry mechanism is enough; our MODEL_RETRIES equivalent, default 2, suffices) |
| `MODEL_RETRIES` (code constant) | 2 | promote to config `llm.same_model_retries`, default 2 |
| `config.temperature` | 0.2 | keep (shared with C) |
| `config.seed` | -1 | keep optional |
| `openai.deployment_id` / `fallback_deployments` | unset | drop (Azure-specific; out of v1 scope — generic base_url+key covers "any OpenAI-compatible endpoint") |
| `pr_reviewer.publish_error_details` | false | **rename** `review.detailed_errors`; as MCP we return errors to the caller, so default true is defensible — keep the sanitization allowlist either way |
| `config.propagate_tool_errors` | false | drop (MCP tools always surface errors) |
| `pr_reviewer.enable_large_pr_chunking` / `max_number_of_calls` | false / 3 | defer post-v1 |
| `pr_reviewer.enable_review_coverage_footer` | true | keep (cheap honesty about skipped files) |
| `litellm.force_streaming_*` / `STREAMING_REQUIRED_MODELS` | unset | drop v1; add a plain `llm.stream` bool only if a target endpoint demands it |

**Risks**

- The repair chain is heuristic text surgery; a tactic can "succeed" by producing *valid but wrong* YAML (e.g. tactic 1 swallowing following lines into a block scalar). Upstream accepts this risk; mitigate in Go by running the strict schema validation after every repaired parse and preferring a later tactic if validation fails outright (deviation from upstream — keep behind a flag, canary-tested).
- PyYAML↔yaml.v3 semantic drift is the top silent-port hazard: `safe_load` of `No` → `False`, `~` → nil, octal-ish strings, and duplicate keys all behave differently. Canary tests must include upstream's own unittest fixtures for `try_fix_yaml` (see `tests/unittest/` yaml/escape tests) replayed against the Go parser.
- Fallback eligibility hinges on precise error classification of arbitrary OpenAI-compatible servers, which often return nonstandard bodies/statuses; misclassifying a 400-with-retryable-meaning (some local servers use 400 for context overflow) stops the chain early. Upstream handles context overflow mostly *before* the call via token budgeting — port that ordering (budget first, classify second).
- `drop_sign_off_after_wrapper_fence` depends on the prompt ending with an open fence; if we change the prompt tail, this tactic's assumptions invert and it can delete real content.
- Concurrency: upstream reuses one handler per tool run and mutates settings (`openai.deployment_id`) around attempts via context vars; an MCP server handling parallel tool calls must keep all attempt state request-scoped (no globals) — per-user tokens make this mandatory anyway.

DESIGN-QUESTION: Repair-chain scope for v1
- Question: port all 12 try_fix_yaml tactics or a reduced set tuned to the /review schema?
- Options: (1) all 12 verbatim (max compatibility, some dead code for us); (2) reduced set (1,2,4,5,6,7/8,9,11,12) + driver designed for easy additions; (3) JSON mode first, YAML repair as fallback.
- Recommendation: (2) — the skipped tactics (3, 10) target /improve//describe output shapes we do not emit; keep the driver table-based so any tactic is a 10-line addition, and prove each kept tactic with a canary fixture (a real malformed sample it alone repairs).

DESIGN-QUESTION: Parse-failure retry semantics
- Question: on YAML parse failure after repairs, upstream burns a whole fallback *model*; with a single configured model (common for our users: one endpoint, one model) that means immediate hard failure. Add a same-model re-ask?
- Options: (1) upstream behavior (fail if no fallback configured); (2) one same-model re-ask on parse failure (optionally appending a terse "previous output was invalid YAML" note), then model fallback; (3) configurable `parse_retries` per model.
- Recommendation: (2) with one re-ask, temperature unchanged, then fallback chain — it directly addresses the single-model deployment our config model makes likely, at the cost of one extra call in the rare failure case.

## E. Line anchoring for /improve (v2 — document only)

Scope: how PR-Agent v0.47.0 (commit 8e5a929) turns an LLM "code suggestion" into a provider inline comment on an exact file+line, and everything that can go wrong on the way. `/improve` itself is deferred to review-mcp v2; this section is the porting map for that pipeline.

**Mechanism**

1. Numbered diff view ("decoupled hunks"). Each file patch is rendered into per-hunk sections with exact markers `__new hunk__` and `__old hunk__` (literal strings, on their own line, no backticks). The `__new hunk__` section lists the post-change content — context lines and `+` lines, `-` lines excluded — and every line is prefixed with its absolute head-file line number computed as `start2 + i` from the (possibly extended) `@@ -start1,size1 +start2,size2 @@` header. The diff marker (`+` or space) is kept after the number: `887 +      line4`. The `__old hunk__` section lists context plus `-` lines, unnumbered. The `__old hunk__` section is omitted when the hunk has no removed lines, and `__new hunk__` is always emitted. Each file is headed `## File: '<path>'` (deleted files become `## File '<path>' was deleted` with no hunks). The format is described to the model by a shared prompt fragment (`prompt_fragments.diff_hunk_format`), rendered with or without line numbers per call.

2. Context extension. Before numbering, each hunk may be extended with `config.patch_extra_lines_before/after` extra context lines (default 5/1, hard cap `MAX_EXTRA_LINES`), with `allow_dynamic_context` growing the "before" side up to `max_extra_lines_before_dynamic_context` until an enclosing function/class header is found. Extension rewrites the hunk headers, so numbering stays consistent with the real head file. Extension only happens on the full-diff fast path (`pr_generate_extended_diff`); when the PR is too large and must be split into chunks (`get_pr_multi_diffs` packed path), raw un-extended patches are used.

3. Two prompt modes, one invariant. `pr_code_suggestions.decouple_hunks` (default **false** in configuration.toml, though the code default when unset is true) picks the generation prompt:
   - decoupled: generation sees the decoupled `__new hunk__`/`__old hunk__` view with the numbers stripped (`remove_line_numbers`, which deletes the leading digit run of each line);
   - not-decoupled (the shipped default): generation sees the plain unified diff per file (no `__new/__old hunk__`, no numbers), and a numbered decoupled twin is built separately (`convert_to_decoupled_with_line_numbers`) for the reflection pass; if that conversion fails or exceeds budget, the tool falls back to fetching decoupled numbered chunks directly.
   The invariant either way: **the generation model never sees line numbers; the reflection model always does.** Chunks are kept as strict pairs (numbered, unnumbered) via `zip(..., strict=True)`.

4. What the generation model must return. YAML (`code_suggestions: [...]`), each entry with: `relevant_file`, `language`, `existing_code` (verbatim snippet from a `__new hunk__` / final-state diff, no `+/-` prefixes in not-decoupled mode), `suggestion_content`, `improved_code` (complete replacement, no ellipsis), `one_sentence_summary`, `label`. **No line numbers are requested at generation time.** Parsing (`_prepare_pr_code_suggestions`) drops entries missing required keys, duplicates by `one_sentence_summary` (within one chunk only), "const instead of let" boilerplate, and entries lacking both `existing_code` and `improved_code`; `improved_code` may be truncated to `max_code_suggestion_length`.

5. Self-reflection pass = where line numbers are born. A second, mandatory LLM call (`self_reflect_on_suggestions`, run over the reasoning-model fallback chain) receives the **numbered** diff plus the suggestion list and returns, per suggestion and in the same order: `suggestion_summary`, `relevant_file`, `relevant_lines_start`, `relevant_lines_end` (both from `__new hunk__` numbers, inclusive, matching the `existing_code` span), `suggestion_score` 0–10, `why`. `analyze_self_reflection_response` copies score and line anchors onto each suggestion. If the reflection output count mismatches the suggestion count, anchors stay unresolved. If reflection fails entirely, every suggestion gets default score 7 and **no anchors**. Score 0 (or negative anchors) marks a suggestion for removal; merge-time filter drops anything below `suggestions_score_threshold` (effectively ≥1, so score-0 always dies). `validate_one_liner_suggestion_not_repeating_code` zeroes a suggestion whose `existing_code` exists only in the base file while `improved_code` is already in the head file (the model "suggesting" a change the PR already made).

6. Validation and snapping before publish:
   - Range sanity (`_is_suggestion_line_range_valid`): start/end must parse as finite ints, start ≥ 1, end ≥ start.
   - Range-in-file (`_is_suggestion_line_range_in_diff`): the file must be in the PR diff (matched by exact `filename` string); with a complete `head_file`, end must be ≤ head-file line count; otherwise `_get_patch_range_lines` re-walks the patch hunk headers and must find *every* line in [start, end] among context/`+` lines, else the suggestion is dropped.
   - Anchor-content check (`_validate_suggestion`): the anchored head-file lines (or patch-extracted lines) are dedented and rstripped and compared to the dedented `existing_code`. Mismatch ⇒ not committable (`is_applicable=False`) but anchor still "valid" — published as a plain comment with the proposed code in a normal fence plus the reason. Out-of-range ⇒ no valid anchor at all ⇒ relegated to a text comment with `Location: file:start-end`.
   - Re-indentation (`dedent_code` + `_shift_code_indentation` / `_align_code_with_tabs`): the improved snippet's indentation is shifted so its first line matches the real indentation of the head-file line at `relevant_lines_start` (space delta shift, or a tab-alignment algorithm that infers the space indent unit and preserves continuation-line alignment).
   - Python-only committable guard (`_validate_python_replacement_syntax`): splice `improved_code` over the range in a compilable head file and recompile; a new SyntaxError demotes the suggestion to a plain comment.

7. Rendering & publication. Two output shapes, chosen by `_uses_summarized_output()`:
   - Summary table (default when the provider supports `gfm_markdown` and `committable_code_suggestions` is false): one persistent PR comment (HTML `<table>`, grouped by label, sorted by score, per-suggestion `<details>` with a `diff`-fenced existing→improved rendering and a `get_line_link` deep link). Unanchorable suggestions are silently skipped from the table. History of previous runs is folded into "Previous suggestions" (`max_history_len`). Optional self-review checkbox.
   - Inline committable (when `committable_code_suggestions=true`, or the provider lacks gfm_markdown — Bitbucket Server lands here by default): `push_inline_code_suggestions` renders body = `**Suggestion:** <content> [label, importance: N]` + ```` ```suggestion ```` fence with the re-indented improved code, and hands `{body, relevant_file, relevant_lines_start, relevant_lines_end, original_suggestion}` to `git_provider.publish_code_suggestions`. Failed batch ⇒ retry one-by-one ⇒ fall back to the summarized comment. `dual_publishing_score_threshold` > 0 additionally pushes high-scoring suggestions inline on top of the table.

8. Per-provider anchoring (base + the two v1 targets):
   - Base template (`git_provider.publish_code_suggestions`): `_prepare_code_suggestions` → per-suggestion `_prepare_code_suggestion` → `_is_valid_code_suggestion` (start truthy and not −1; end ≥ start) → `_build_code_suggestion_payload` → `publish_inline_comments(payloads)`.
   - **Gitea** overrides `publish_code_suggestions` wholesale: each suggestion becomes one review via `POST /repos/{owner}/{repo}/pulls/{index}/reviews` with `event: "COMMENT"` (an event-less review would stay a draft, invisible), `commit_id` = last head commit SHA, and `comments: [{body, path, old_position, new_position}]` where **both positions are set to `relevant_lines_start`** — `relevant_lines_end` is ignored, so every anchor is single-line. The ```` ```suggestion ```` fence stays in the body (Gitea renders it as an applicable suggestion). Free-floating inline comments (`publish_inline_comment`, used by /review not /improve) instead resolve the line by text via `find_line_number_of_relevant_line_in_file` and send `old_position=position-in-patch`, `new_position=absolute line` — note the two call sites fill `old_position` with different semantics. Partial batch failure reports success to avoid double-posting.
   - **Bitbucket Server** uses the base template. `_prepare_code_suggestion` rewrites multi-line bodies: ```` ```suggestion ```` → ```` ```diff ```` with a unified diff of existing→improved (BBS cannot do committable multi-line suggestions, BSERV-4553). `_build_code_suggestion_payload` emits `{body, path, line, start_line?, side/start_side: "RIGHT"}`; `publish_inline_comments` collapses that to a single `from_line` (start_line wins) and `publish_inline_comment` posts `{text, severity: NORMAL, anchor: {diffType: "EFFECTIVE", path, lineType: "ADDED", line, fileType: "TO"}}` to the PR comments endpoint. `lineType` is hardcoded `ADDED`: anchoring on an unchanged context line (possible, since numbered hunks include context) depends on the server tolerating the mismatch. Text-based calls resolve through `find_line_number_of_relevant_line_in_file` with optional `absolute_position`.
   - `find_line_number_of_relevant_line_in_file` (shared util) maps (file, relevant line text | absolute line) → (position-in-patch, absolute line): walks hunk headers, counts non-`-` lines; text mode tries exact match, then `difflib.get_close_matches` (cutoff 0.93, must be a unique `+` match), then substring, then a `+`-stripped retry; failure ⇒ (−1, −1) ⇒ comment demoted to file-level or dropped per provider.

9. Dedup. Two independent layers: (a) in-run — duplicate `one_sentence_summary` within a chunk is skipped at parse time; `_limit_suggestions_per_file` caps suggestions per file by score when `max_suggestions_per_file` > 0. (b) cross-run — `algo/inline_comment_dedup.py` fingerprints each inline comment with SHA-256 over (file, anchor, normalized body-prefix) OR (file, anchor, normalized ```` ```suggestion ````/```` ```diff ```` code), embeds 12-hex markers as HTML comments (or link-reference form for providers without HTML comments) in the posted body, and on later runs scans existing comment bodies to skip already-posted fingerprints (`InlineCommentStore`, lazy, failure degrades to within-run dedup). Opt-in via `config.persistent_inline_comments` (default false) and wired only into GitHub, GitLab and Azure DevOps — **not Gitea or Bitbucket Server** in upstream.

**Key code**

- `pr_agent/algo/git_patch_processing.py:decouple_and_convert_to_hunks_with_lines_numbers @ 8e5a929` — builds the `__new hunk__`/`__old hunk__` numbered view; numbering = `start2 + index-within-new-content`.
- `pr_agent/algo/git_patch_processing.py:extend_patch / process_patch_lines @ 8e5a929` — extra/dynamic context, rewrites hunk headers; `check_if_hunk_lines_matches_to_file` gates extension.
- `pr_agent/algo/git_patch_processing.py:extract_hunk_lines_from_patch @ 8e5a929` — side-aware (left/right) hunk slice for a line range (used by /ask_line style flows; same header math).
- `pr_agent/algo/pr_processing.py:get_pr_diff, get_pr_multi_diffs, pr_generate_extended_diff, pr_generate_compressed_diff @ 8e5a929` — token-budgeted single/multi chunk diff assembly; `add_line_numbers(_to_hunks)` toggles the numbered view; packed chunks skip context extension.
- `pr_agent/tools/pr_code_suggestions.py:prepare_prediction_main, _get_prediction, remove_line_numbers, convert_to_decoupled_with_line_numbers @ 8e5a929` — chunking, numbered/unnumbered pairing, per-chunk generation + mandatory reflection, chunk-level model fallback recovery.
- `pr_agent/tools/pr_code_suggestions.py:_prepare_pr_code_suggestions, _self_reflect_with_fallback, analyze_self_reflection_response @ 8e5a929` — YAML parse/filter; anchors and scores assigned from reflection output (order-based correlation).
- `pr_agent/tools/pr_code_suggestions.py:_is_suggestion_line_range_valid, _is_suggestion_line_range_in_diff, _get_patch_range_lines, _validate_suggestion, _validate_python_replacement_syntax, dedent_code, _align_code_with_tabs, validate_one_liner_suggestion_not_repeating_code @ 8e5a929` — the entire snap-back/verification layer.
- `pr_agent/tools/pr_code_suggestions.py:push_inline_code_suggestions, generate_summarized_suggestions, dual_publishing, _limit_suggestions_per_file @ 8e5a929` — rendering committable vs table vs fallback text comments.
- `pr_agent/settings/code_suggestions/pr_code_suggestions_prompts.toml`, `pr_code_suggestions_prompts_not_decoupled.toml`, `pr_code_suggestions_reflect_prompts.toml @ 8e5a929` — generation schema (no lines) and reflection schema (`relevant_lines_start/end`, `suggestion_score`, `why`).
- `pr_agent/settings/prompt_fragments.toml:diff_hunk_format @ 8e5a929` + `pr_agent/algo/prompt_fragments.py:render_diff_hunk_format` — single source of truth for the hunk format description, parameterized by `include_line_numbers`.
- `pr_agent/algo/utils.py:find_line_number_of_relevant_line_in_file @ 8e5a929` — text/absolute → (patch position, absolute line), with fuzzy matching.
- `pr_agent/algo/inline_comment_dedup.py:body_fingerprint, code_fingerprint, body_with_markers, InlineCommentStore @ 8e5a929` — cross-run dedup markers.
- `pr_agent/git_providers/git_provider.py:publish_code_suggestions (template), _is_valid_code_suggestion, _build_code_suggestion_payload, publish_inline_comment(s), create_inline_comment @ 8e5a929` — provider contract.
- `pr_agent/git_providers/gitea_provider.py:publish_code_suggestions, publish_inline_comment, publish_inline_comments, RepoApi.create_inline_comment @ 8e5a929` — review API, `old_position`/`new_position`, `event: COMMENT`, `commit_id`.
- `pr_agent/git_providers/bitbucket_server_provider.py:_prepare_code_suggestion, _build_code_suggestion_payload, create_inline_comment, publish_inline_comment, publish_inline_comments @ 8e5a929` — suggestion→diff rewrite, `anchor{diffType, lineType, fileType, line}`.
- `pr_agent/algo/types.py:FilePatchInfo @ 8e5a929` — `filename` (new path), `old_filename`, `head_file`, `head_file_is_complete`, `edit_type` drive every lookup above.

**Data flow**

```
provider diff files (FilePatchInfo: patch, base_file, head_file, filename)
  -> extend_patch (extra/dynamic context; full-diff path only)
  -> decouple_and_convert_to_hunks_with_lines_numbers     ("numbered view")
  -> remove_line_numbers / not-decoupled raw diff          ("unnumbered view")
  -> chunk pairs (numbered, unnumbered), token-budgeted, <= max_number_of_calls
  -> LLM #1 (generation, unnumbered): existing_code + improved_code, NO lines
  -> parse YAML, filter junk/duplicates
  -> LLM #2 (reflection, numbered, reasoning chain):
       per suggestion -> relevant_lines_start/end (from __new hunk__ numbers) + score
  -> score filter (threshold, score-0 kills) + per-file cap
  -> validate: range parses, range within head file / patch, existing_code == head lines
  -> dedent/re-indent improved_code to the real line's indentation
  -> render: ```suggestion fence (committable) | plain comment | summary table
  -> provider publish:
       Gitea: POST pulls/{n}/reviews {event:COMMENT, commit_id, comments:[{body,path,
              old_position=new_position=relevant_lines_start}]}   (single-line anchor)
       BBS:   POST pr comments {text, anchor:{diffType:EFFECTIVE, path,
              lineType:ADDED, fileType:TO, line=start_line|line}} (single-line anchor;
              multi-line suggestion downgraded to ```diff block)
  -> cross-run dedup markers in body (GitHub/GitLab/ADO only upstream)
```

The anchor coordinate system end-to-end is **absolute head-file (new side) line numbers**; nothing uses GitHub-style diff "position" except the text-resolution helper's first return value and Gitea's secondary inline-comment path.

**Porting notes (Go)**

- The numbered view is a pure function of a unified patch: port `decouple_and_convert_to_hunks_with_lines_numbers` first, with golden tests (ordinary hunk, hunk without deletions, multi-hunk file, deleted file, `\ No newline at end of file`, malformed `@@` header skip). It is the contract between diff building, both prompts, and anchor validation; the exact marker strings `__new hunk__` / `__old hunk__` and `## File: '<path>'` must match the prompt text verbatim.
- Keep the invariant "generation unnumbered, reflection numbered" as paired renderings of the *same* chunk, not two independent diff builds — upstream bails out (`return []`) rather than let the pair drift, and so should the port. In Go, render both from one parsed hunk model instead of string-stripping numbers back out (upstream's `remove_line_numbers` digit-stripping heuristic is a smell, not a design).
- Represent a suggestion as a struct with explicit lifecycle flags rather than upstream's dict mutation: `{File, Language, ExistingCode, ImprovedCode, Content, Summary, Label, Score, ScoreWhy, LinesStart, LinesEnd, Anchored bool, Applicable bool, FallbackReason string, Truncated bool}`.
- Order-based correlation of reflection output to suggestions (index i ↔ suggestion i) is fragile; at minimum enforce the same count-mismatch bail-out, and consider echoing a stable id per suggestion in the reflection prompt so a partial answer can still be matched (deviation from upstream — flag in DESIGN-QUESTION 2).
- The validation ladder is the most valuable part to port faithfully: parse-int guards (reject bool/NaN/fractional), start ≥ 1, end ≥ start, end ≤ head-file length (or every line of the range resolvable from the patch when the head file is unavailable/incomplete), dedented `existing_code` == dedented anchored lines, and the committable/plain-comment demotion rather than dropping. Go's `strings` + a small dedent helper cover it; the tab-alignment re-indenter (`_align_code_with_tabs`) is ~100 lines of niche logic — port the simple space-delta shift first and treat tab alignment as a follow-up.
- Skip the Python-compile check (`_validate_python_replacement_syntax`) or generalize it behind an optional per-language hook; it is not portable as-is.
- Provider interface for v2: `PublishCodeSuggestions([]Suggestion) error` built on the base-template shape (prepare → validate → payload → batch publish → per-item retry → summarized fallback). For Gitea use one review per batch (not per suggestion, which upstream does and which spams N review events) — but note upstream deliberately posts per-suggestion to attach `**Suggestion:** ...` as the review body; decide in DESIGN-QUESTION 4. Always send `commit_id` (head SHA the diff was computed from) to pin anchors against races with new pushes.
- Bitbucket Server: anchor only single lines; keep upstream's multi-line → ```diff downgrade. Consider computing `lineType` honestly (ADDED vs CONTEXT) from the patch instead of hardcoding ADDED — upstream's hardcoding is a latent 400-error source when the anchor lands on a context line.
- The cross-run dedup module is provider-agnostic and small (fingerprint + marker + scan); if review-mcp's /improve posts inline comments on repeated runs, port it and wire it into Gitea/BBS (upstream never did), or rely on review-mcp's own comment-identity machinery if one exists for /review.
- Token budgeting, chunk packing and multi-model recovery (`AttemptTokenBudget`, `_recover_failed_chunks`) are orthogonal to anchoring; reuse whatever review-mcp already has for /review and keep only the rule "a chunk must fit whole, never truncated" (truncated numbered diff = corrupted anchors).

**Config knobs**

| upstream key | default | keep/drop/rename |
|---|---|---|
| `pr_code_suggestions.decouple_hunks` | false | drop — pick one representation in Go (recommend: always decoupled generation view) and delete the mode switch; see DESIGN-QUESTION 1 |
| `pr_code_suggestions.committable_code_suggestions` (deprecated alias `commitable_...`) | false | keep (canonical spelling only; no alias) |
| `pr_code_suggestions.dual_publishing_score_threshold` | −1 (off) | drop for v2 initial; revisit if table output exists |
| `pr_code_suggestions.suggestions_score_threshold` | 0 (min 1 effective) | keep |
| `pr_code_suggestions.num_code_suggestions_per_chunk` | 3 | keep |
| `pr_code_suggestions.max_number_of_calls` | 3 | keep |
| `pr_code_suggestions.parallel_calls` | true | keep |
| `pr_code_suggestions.max_suggestions_per_file` | 0 (off) | keep |
| `pr_code_suggestions.max_code_suggestion_length` / `suggestion_truncation_message` | 0 / "" | drop (truncation corrupts committable suggestions; prefer hard skip) |
| `pr_code_suggestions.focus_only_on_problems` | true | keep |
| `pr_code_suggestions.extra_instructions` | "" | keep |
| `pr_code_suggestions.persistent_comment` / `max_history_len` | true / 4 | keep if table output is ported |
| `pr_code_suggestions.demand_code_suggestions_self_review` + approve/fold flags | false | drop (webhook-driven UX, not MCP-shaped) |
| `pr_code_suggestions.new_score_mechanism(_th_high/_th_medium)` | true / 9 / 7 | rename → `score_display` (high/medium/low thresholds) or drop |
| `pr_code_suggestions.suggestions_heading` | "PR Code Suggestions" | keep |
| `pr_code_suggestions.enable_suggestions_coverage_footer` | true | keep (honest-degradation reporting) |
| `config.patch_extra_lines_before` / `after` | 5 / 1 | keep |
| `config.allow_dynamic_context` / `max_extra_lines_before_dynamic_context` | true / 10 | keep (port later; start with static extra lines) |
| `config.persistent_inline_comments` | false | keep (and wire into Gitea/BBS, unlike upstream) |
| `pr_code_suggestions.max_discussion_context_chars` | 24000 | drop for v2 initial (needs provider thread-state support) |

**Risks**

- Off-by-N anchors: the reflection model misreads the `__new hunk__` numbers (classic: picks the first line of the hunk, or counts from the unextended hunk). Upstream's only true defense is the `existing_code == anchored head lines` equality check, which demotes to a plain comment rather than fixing the number. Any port that skips that check will post committable suggestions on the wrong lines.
- Wrong `existing_code` echo: the model paraphrases, drops a comment line, or normalizes whitespace; dedent+rstrip comparison then fails and the suggestion silently loses committability. Conversely `...` ellipses in `existing_code` (explicitly invited by the not-decoupled prompt for long spans) can never pass the equality check — upstream relies on it failing "safe".
- Clipped/compressed hunks: packed multi-chunk mode drops context extension and can drop whole files (`remaining_files_list`, coverage footer); `head_file_is_complete=false` forces the patch-walk range check, which rejects any range touching lines outside recorded hunks — suggestions near but not inside a hunk die quietly.
- Reflection fragility: empty reflection ⇒ default score 7 but **no anchors** ⇒ everything dropped at publish; count mismatch ⇒ same. A single malformed YAML item can therefore wipe a whole chunk's suggestions. This is the pipeline's biggest silent-loss point.
- Renamed files: matching is by exact new-path string (`FilePatchInfo.filename`); the model sometimes emits the old path (visible in diff metadata) ⇒ "file not part of the PR diff" ⇒ dropped. Deleted files are rendered as a marker only, but models still occasionally suggest on them.
- Gitea single-line anchoring: `relevant_lines_end` is discarded; a multi-line ```` ```suggestion ```` applied through Gitea's UI replaces only the anchored line — the applied result can be wrong even though everything validated. Also `old_position` carries different meanings on the two Gitea call paths (patch position vs start line); treat it as unreliable and re-derive in the port.
- Bitbucket Server `lineType: "ADDED"` hardcoded: anchoring a context line (legal in the numbered view) may be rejected by the server; multi-line suggestions become non-committable ```diff blocks by design.
- Anchor races: anchors are head-file lines of the diff snapshot; a push between diff fetch and publish shifts lines. Gitea pins `commit_id`; BBS `diffType: EFFECTIVE` floats with the PR. Cross-run dedup fingerprints deliberately exclude the anchor line on GitHub for this reason.
- Number-stripping heuristic (`remove_line_numbers`) operates on rendered text and turns a purely numeric content line into an empty line in the generation view; harmless upstream only because the view it feeds never needs round-tripping. Avoid reproducing the heuristic (render twice from the parsed model instead).
- Indentation repair: space-shift and tab-alignment fix the committable fence to match the file, but an over-aggressive dedent of `existing_code` on mixed tabs/spaces can make two different snippets compare equal (dedent + rstrip) and anchor the wrong one when a file contains repeated similar blocks. Range equality is content-based, not unique-match-based.

DESIGN-QUESTION: Diff representation for the Go port
- Question: keep upstream's dual-mode (`decouple_hunks` true/false) with a numbered twin built only for reflection, or standardize on one representation?
- Options: (a) port both modes; (b) always generate from the plain unified diff and build the numbered decoupled view only for reflection (upstream's shipped default, config `decouple_hunks=false`); (c) always use the decoupled view for both calls (numbers stripped for generation).
- Recommendation: (c). One hunk model, two renderings, no string-stripping; eliminates the pair-drift failure class and the `convert_to_decoupled_with_line_numbers` bail-out path, at the cost of deviating from the upstream default prompt (prompts must be re-validated once with live models).

DESIGN-QUESTION: Anchor acquisition — second LLM pass vs direct
- Question: reproduce the two-pass design (generation without numbers, reflection returns `relevant_lines_start/end` + score) or ask for line numbers in the first pass?
- Options: (a) faithful two-pass, order-correlated; (b) two-pass with stable per-suggestion ids echoed back (tolerates partial/reordered reflection output); (c) single pass with numbered view and self-assigned lines plus a purely mechanical snap (search `existing_code` in the head file / numbered hunks, no second call).
- Recommendation: (b) for fidelity with a robustness fix; prototype (c) behind a flag — the mechanical `existing_code` search is deterministic and the equality check already exists, so (c) may remove one model call and the biggest silent-loss point (reflection failure), at the cost of losing the scoring pass (which would then need folding into generation output).

DESIGN-QUESTION: Verification source of truth
- Question: v2 validation can check anchors against the full head file or only against the patch. Upstream prefers `head_file` when complete and falls back to a patch walk. Requiring head-file fetch per suggested file adds API calls on Gitea/BBS.
- Options: (a) require head file (reject file suggestions when unavailable); (b) upstream's hybrid (head file if complete, else patch walk); (c) patch-only.
- Recommendation: (b). Patch-only forbids anchoring in extended-context areas the model legitimately saw; head-file-only turns provider fetch hiccups into lost suggestions.

DESIGN-QUESTION: Gitea publication granularity and range anchors
- Question: upstream posts one review per suggestion with `old_position=new_position=start` (single-line). Should v2 batch all suggestions into one review, and should it refuse committable fences whose range spans multiple lines on Gitea?
- Options: (a) upstream-faithful (per-suggestion reviews, single-line anchors, multi-line fences allowed); (b) one review per run, demote multi-line suggestions to plain comments on Gitea; (c) one review per run, keep multi-line fences but document the apply-behavior caveat.
- Recommendation: (b). One review event per run matches reviewer expectations and the "don't spam" goal; demoting multi-line fences avoids Gitea applying a replacement to the wrong span. Verify current Gitea/Forgejo multi-line comment API support at implementation time before finalizing.

DESIGN-QUESTION: Bitbucket Server lineType correctness
- Question: upstream hardcodes `lineType: "ADDED"`, `fileType: "TO"`, `diffType: "EFFECTIVE"`. Should v2 compute `lineType` (ADDED vs CONTEXT) from the patch, and should anchors pin a commit instead of the effective diff?
- Options: (a) keep hardcoded; (b) compute lineType from the hunk data already in hand; (c) also switch to COMMIT diffType pinned at the analyzed head SHA.
- Recommendation: (b). The hunk model already knows whether the anchored line is `+` or context, so the fix is nearly free and removes a server-side rejection class; (c) only if live testing shows EFFECTIVE anchors drifting on force-push.

DESIGN-QUESTION: Cross-run dedup for Gitea/BBS
- Question: upstream's fingerprint-marker dedup (`persistent_inline_comments`) is not wired into Gitea or Bitbucket Server. Does v2 /improve need it, and in which form?
- Options: (a) skip (rely on persistent summary comment, which replaces itself); (b) port the marker scheme (HTML comment markers; verify both servers preserve HTML comments in bodies, else use the link-reference marker form upstream already provides); (c) dedup via a local state file per PR instead of in-body markers.
- Recommendation: (b) if committable inline mode ships in v2; the module is self-contained and the link-reference fallback already solves the "no HTML comments" case. (a) is sufficient while only table output exists.

## F. Providers: Gitea & Bitbucket Server

PR-Agent isolates every host behind one abstract class, `GitProvider` (`pr_agent/git_providers/git_provider.py`), and selects the concrete implementation at runtime from `config.git_provider` via a lazy registry in `pr_agent/git_providers/__init__.py` (`_BUILTIN_GIT_PROVIDERS` maps `"gitea"` → `GiteaProvider`, `"bitbucket_server"` → `BitbucketServerProvider`; `get_git_provider_with_context(pr_url)` instantiates and caches per PR URL in request context). The provider is constructed **from the PR URL alone** — each provider parses owner/repo/PR-number out of the pasted link and combines it with a configured base URL + token. Tools never branch on provider type; they ask `provider.is_supported("capability")` and the ~30 `supports_*()` hooks.

Notably, `git_providers/diff_parsing.py` (unified-diff → `FilePatchInfo` parsing, reverse-apply reconstruction) is used only by the Bitbucket **Cloud** and plain-diff providers — neither Gitea nor Bitbucket Server imports it. Gitea parses the whole-PR `.diff` with a hand-rolled line scanner; Bitbucket Server never receives a server-side patch at all and regenerates patches locally with `difflib` (`load_large_diff`). `git_providers/utils.py::apply_repo_settings(pr_url)` is the shared config-merge step: it constructs the provider, calls `provider.get_repo_settings()` (global `pr-agent-settings` repo + repo-local `.pr_agent.toml`), writes the TOML to temp files and merges them into Dynaconf before any tool runs — for review-mcp this whole layer is replaced by per-user env/header config, but the *order* (provider first, repo settings second, diff filtering lazily at `get_diff_files()` time) matters and both providers depend on it.

### F.1 Provider abstraction (base class)

**Mechanism**

`GitProvider` (ABC) defines a small set of **required** abstract methods and a large set of **optional** capability-flagged ones with safe defaults.

Required (`@abstractmethod`): `is_supported(capability)`, `get_files()`, `get_diff_files() -> list[FilePatchInfo]`, `publish_description(title, body)`, `get_languages()`, `get_pr_branch()`, `get_user_id()`, `get_pr_description_full()`, `get_repo_settings()`, `publish_comment(text, is_temporary)`, `publish_inline_comment(...)`, `publish_inline_comments(list)`, `remove_initial_comment()`, `remove_comment(comment)`, `get_issue_comments()`, `publish_labels(labels)`, `get_pr_labels(update)`, `remove_reaction(...)`, `get_commit_messages()`.

Optional with defaults (the ones relevant to review/ask): `get_incremental_commits()` (no-op), `edit_comment()` (no-op → `supports_comment_editing()` detects the override via `type(self).edit_comment is not GitProvider.edit_comment`), `publish_persistent_comment[_full]()` (concrete template: find prior PR-Agent comment by identity marker, edit it or fall back to a new comment), `get_line_link()` (""), `get_comment_url()` (""), `get_latest_commit_url()` (""), `get_pr_head_sha()` (""), `add_reaction()` (None), `add_eyes_reaction()`/`react_to_outcome()` (built on `add_reaction`, driven by `config.reaction_on_start/success/failure`), `get_repo_file_content()` (""), `get_owning_namespace()` (None → disables global settings), `limit_output_characters()` (truncation helper), `clone()` (shallow `git clone --filter=blob:none --depth 1` with the token passed as `http.extraHeader` through `GIT_CONFIG_*` env, never argv).

Capability string checked through `is_supported()` by /review and /describe output paths: `'get_labels'`, `'gfm_markdown'` (others exist for other tools: `'get_issue_comments'`, `'create_inline_comment'`, `'publish_inline_comments'`, `'is_supported'`...). Boolean hooks: `supports_markdown_tables()` (False — consulted only when `gfm_markdown` is off, to degrade review tables), `supports_comment_publish_confirmation()` (True), `supports_checkbox_commands()` (False), `supports_review_comment_identity()` (False), `supports_thread_resolution()` (False), many more — defaults are all "not supported", so a minimal provider still works with degraded output.

**Key code**

- `pr_agent/git_providers/git_provider.py:GitProvider @ 8e5a929` — the ABC; abstract vs defaulted methods as listed above.
- `pr_agent/git_providers/git_provider.py:publish_persistent_comment_full @ 8e5a929` — find-latest-matching-comment → edit-in-place → "updated until commit X" header rewrite → fallback `publish_comment` on any error.
- `pr_agent/git_providers/git_provider.py:redact_credentials @ 8e5a929` — regex-strips URL userinfo and `Authorization: ...` values from anything logged. Port this exactly.
- `pr_agent/git_providers/__init__.py:get_git_provider_with_context @ 8e5a929` — registry lookup + per-PR-URL instance cache.
- `pr_agent/git_providers/git_provider.py:FileContentSnapshot / IncrementalPR @ 8e5a929` — ancillary types; `IncrementalPR` is only meaningfully driven by GitHub.

**Data flow**

Tool → `get_git_provider_with_context(pr_url)` → provider `__init__` parses URL + loads PR metadata → `apply_repo_settings` merges repo config → tool calls `get_diff_files()` (list of `FilePatchInfo{base_file, head_file, patch, filename, edit_type, old_filename, num_plus/minus_lines}`) → prompt building → `publish_comment` / `publish_persistent_comment` / `publish_inline_comments`.

**Porting notes (Go)**

- Model the interface as a small Go interface for v1 (`pr_review` + `pr_ask` only): `GetDiffFiles`, `GetFiles`, `GetPRMetadata` (title/body/branch/head SHA), `GetCommitMessages`, `GetFileContent(path, ref)`, `PublishComment`, `PublishInlineComments`, `GetIssueComments`, plus a `Capabilities()` bitset/struct instead of stringly-typed `is_supported("gfm_markdown")`. Drop the ~25 hooks that only serve /improve, /describe persistence, reactions, checkboxes, webhooks.
- Keep `FilePatchInfo` as the central DTO — everything in the compression/prompt layer consumes it.
- Keep the capability idea but as a typed struct (`SupportsLabels bool`, `SupportsGFM bool`, `SupportsMarkdownTables bool`, `SupportsInlineComments bool`); review output rendering needs exactly these.
- Persistent-comment update (edit prior review comment, identified by a hidden marker) is worth porting for `pr_review` re-runs; it needs only `ListComments` + `EditComment`.
- The clone machinery, global `pr-agent-settings` repo, per-directory settings, reaction lifecycle: drop for v1.

**Config knobs**

| upstream key | default | keep/drop/rename |
|---|---|---|
| `config.git_provider` | `"github"` | keep → `provider` per request/tool-arg (`gitea` \| `bitbucket_server`); no default host assumptions |
| `config.reaction_on_start/success/failure` | `"eyes"` / "" / "" | drop (reaction lifecycle not in v1) |
| `config.use_global_settings_file` | true | drop (no org settings repo in v1) |
| `config.max_description_tokens` (`CONFIG.MAX_DESCRIPTION_TOKENS`) | unset | keep as optional clip |
| `config.max_commits_tokens` (`CONFIG.MAX_COMMITS_TOKENS`) | unset | keep as optional clip |

**Risks**

- The base class embeds Python-isms (comment objects that are sometimes dicts, sometimes SDK objects, `_get_comment_body` normalizer). Go should normalize to one `Comment{ID, Body, URL, Version}` struct at the provider boundary.
- `publish_persistent_comment_full` swallows errors into a fallback new-comment; a Go port must keep that fallback or re-runs will fail hard when the prior comment was deleted.

DESIGN-QUESTION: Capability surface for v1
- Question: should review-mcp expose a capability struct at all for two providers, or hard-code per-provider rendering?
- Options: (a) typed `Capabilities` struct returned by each provider; (b) `switch provider` at render sites.
- Recommendation: (a). GitHub is planned next, and the Bitbucket Server markdown degradations (no GFM, no labels) already force two render paths; a struct keeps render code provider-agnostic from day one.

### F.2 Gitea

**Mechanism — construction & auth**

- Token: a Gitea **personal access token** from `GITEA.PERSONAL_ACCESS_TOKEN` (secrets template `[gitea].personal_access_token`). Sent as an API-key header: `Authorization: token <PAT>` (giteapy `configuration.api_key['Authorization']`, auth setting `AuthorizationHeaderToken`). Constructor raises if missing.
- Base URL: `GITEA.URL` (shipped default `https://gitea.com` — the code explicitly treats a value equal to the shipped default as "unset" when deriving user-facing links, a trap created by having a default at all). API host is `{base_url}/api/v1`. TLS knobs: `GITEA.SKIP_SSL_VERIFICATION` (bool), `GITEA.SSL_CA_CERT` (path).
- Separate **web URL** resolution (`base_url_html`, lazy property): `GITEA.WEB_URL` > explicitly configured `GITEA.URL` (≠ shipped default) > derived by stripping `/{owner}/{repo}/pulls/{n}` off `pr.html_url` (which Gitea builds from its own `ROOT_URL`) > `base_url`. Used only for links in published comments; API/clone always use `base_url`.
- URL parsing (`_parse_pr_url`): strips a leading `/api/v1/repos` if present, then expects path `/{owner}/{repo}/pulls/{number}`; same shape with `issues` for issue URLs. Anything else → `ValueError`.
- Client: generated `giteapy` SDK wrapped by a local `RepoApi` class that mixes `RepositoryApi`, `IssueApi` and raw `api_client.call_api(...)` calls for endpoints the SDK lacks.

**REST endpoints actually called** (all under `{base}/api/v1`)

| Purpose | Method + path |
|---|---|
| PR metadata | GET `/repos/{owner}/{repo}/pulls/{index}` (SDK `repo_get_pull_request`) |
| Changed files | GET `/repos/{owner}/{repo}/pulls/{pr}/files?page=N&limit=50` (raw, paginated) |
| Whole-PR diff | GET `/repos/{owner}/{repo}/pulls/{pr}.diff` (raw text) |
| PR commits | GET `/repos/{owner}/{repo}/pulls/{pr}/commits?page=N&limit=50` (raw, paginated) |
| File content at rev | GET `/repos/{owner}/{repo}/raw/{filepath}?ref={sha}` (raw bytes) |
| PR-level comment | POST `/repos/{owner}/{repo}/issues/{index}/comments` (SDK `issue_create_comment`) |
| Edit comment | PATCH `/repos/{owner}/{repo}/issues/comments/{id}` (SDK `issue_edit_comment`) |
| Delete comment | DELETE `/repos/{owner}/{repo}/issues/comments/{id}` |
| List comments | GET `/repos/{owner}/{repo}/issues/{index}/comments` (SDK; **not** paginated here — see Risks) |
| Inline comments | POST `/repos/{owner}/{repo}/pulls/{pr}/reviews` with body `{body, comments:[{path, old_position, new_position, body}], commit_id, event:"COMMENT"}` |
| Edit PR title/body | PATCH `/repos/{owner}/{repo}/pulls/{index}` (SDK `repo_edit_pull_request`) |
| Labels (read) | GET `/repos/{owner}/{repo}/issues/{index}/labels` |
| Labels (write) | POST `/repos/{owner}/{repo}/issues/{index}/labels` (body `{labels: [ids]}` — **IDs, not names**) |
| Languages | GET `/repos/{owner}/{repo}/languages` |
| Reactions | POST/DELETE `/repos/{owner}/{repo}/issues/comments/{id}/reactions` |
| Repo info (default branch) | GET `/repos/{owner}/{repo}` (`repo_get`) |

No commit-status API is used for /review on Gitea (labels serve as the "review effort" signal, via `publish_labels`).

**Pagination**: `RepoApi._list_all_pages(url, page_size=50)` loops `page=1,2,...` until an **empty page**. Deliberate: Gitea caps `limit` at the server's `MAX_RESPONSE_ITEMS`, so a short page is *not* proof of the last page. Errors propagate (no silently truncated lists). Only `/files` and `/commits` go through this; `issue_get_comments` does not.

**Diff format quirks**

- One whole-PR unified diff (`.diff` endpoint) is split per-file by a hand-rolled scanner (`__add_file_diff`): file starts at `diff --git`, filename taken as `line.split(' b/')[-1]`, patch content begins at the first `@@`. Per-file patches therefore contain **hunks only** — no `---`/`+++` headers, no `index` lines. Context lines: whatever the server emits (standard 3).
- Changed-file metadata (status/additions/deletions) comes from the `/files` endpoint; statuses mapped: `added`→ADDED, `removed|deleted`→DELETED, `renamed`→RENAMED, `modified|changed`→MODIFIED, else UNKNOWN (logged).
- **Rename handling is lossy**: the `b/`-side name is used; `old_filename` is never populated (`FilePatchInfo.old_filename` stays None). The naive `split(' b/')` also breaks on paths containing ` b/` or quoted/escaped paths (spaces, unicode) — Gitea quotes such paths in `diff --git` lines.
- Deleted files: the `b/`-side of `diff --git a/X b/X` still names X, so the scanner works, but `__add_file_content` of head will 404 (logged, content ""), which is fine.
- Full file contents: head content fetched at PR head SHA, base content at `pr.base.sha`, each via the raw endpoint; beyond `MAX_FILES_ALLOWED_FULL` (50) valid files, contents are skipped (`avoid_load`) and only patches are used. Byte decoding via `decode_if_bytes` fallback chain (utf-8 → iso-8859-1 → latin-1 → ascii → utf-16).
- `get_diff_files()` applies `filter_ignored(platform="gitea")` lazily (repo settings are merged after construction).

**Inline comments**: published as one **review** per batch (`event: "COMMENT"` is mandatory — an event-less review is a draft PENDING review, invisible to everyone but its author, and nothing would ever submit it). Line anchoring: `old_position`/`new_position` computed by `find_line_number_of_relevant_line_in_file` against the stored diff; when the line isn't found the comment degrades to an empty payload (effectively dropped from the review). `commit_id` = last commit SHA.

**Commits**: PR commits endpoint returns **newest-first**; provider reverses to oldest-first to match GitHub iteration order, and keeps a `last_commit` fallback that wraps the PR head SHA when the commits endpoint returns nothing (adapter `_GiteaCommitAdapter` mimics PyGithub `.sha`/`.html_url`).

**Incremental review**: none. `self.incremental = IncrementalPR(False)` is always False; the `unreviewed_files_map` branch in `get_diff_files()` is dead code (nothing sets incremental). `get_incremental_commits` is the base no-op.

**Error handling**: catches `giteapy.rest.ApiException` everywhere; most read failures log + return empty (`""`, `[]`, `{}`); `get_issue_comments` raises `RuntimeError` on a non-list; `publish_description` raises on falsy response; comment/inline publish return None/False on failure; 404 on file content/global settings is an expected "" (so it can be cached); pagination errors propagate.

**Capabilities**: `is_supported()` returns **True for everything** (including `gfm_markdown` and `get_labels`). `supports_comment_editing` effectively True (edit_comment overridden). No reactions removal by id (Gitea removes by comment — `reaction_id` ignored). Everything else inherits base defaults (no thread resolution, no checkbox commands, no persistent-comment markers beyond the base behavior, no incremental).

**Version sensitivity**: everything is Gitea **API v1** (`/api/v1`), stable since Gitea 1.x and shared by **Forgejo** (the code comments explicitly account for Gitea/Forgejo). The `.diff` endpoint, `/pulls/{n}/files`, review-with-event POST and raw endpoint all exist in Gitea ≥1.16; review-mcp can treat 1.19+ as baseline without branching. The one semantic trap is the draft-review default (`event` required), present in both Gitea and Forgejo.

**Key code**

- `pr_agent/git_providers/gitea_provider.py:GiteaProvider.__init__ @ 8e5a929` — auth, URL parse, eager PR + files + whole-diff + commits load in constructor.
- `pr_agent/git_providers/gitea_provider.py:__add_file_diff @ 8e5a929` — whole-PR `.diff` → per-file hunk-only patches (the lossy scanner).
- `pr_agent/git_providers/gitea_provider.py:get_diff_files @ 8e5a929` — merge `/files` metadata + patches + base/head contents into `FilePatchInfo`; 50-file full-content cap.
- `pr_agent/git_providers/gitea_provider.py:_resolve_base_url_html @ 8e5a929` — API-vs-web URL split (4-step resolution).
- `pr_agent/git_providers/gitea_provider.py:RepoApi._list_all_pages @ 8e5a929` — empty-page-terminated pagination.
- `pr_agent/git_providers/gitea_provider.py:RepoApi.create_inline_comment @ 8e5a929` — review POST with `event: "COMMENT"`.

**Data flow**

PR URL → parse owner/repo/number → GET pull → GET files (paged) → GET `.diff` → split per file → GET commits (paged, reversed) → per valid file: GET raw@head + GET raw@base → `FilePatchInfo[]` → review/ask prompt. Publish: review table → POST issue comment; inline findings → one POST review with comments[].

**Porting notes (Go)**

- Do **not** use a generated SDK; the upstream wrapper already bypasses giteapy for half the endpoints. A thin typed client over `net/http` with the 10 endpoints above is smaller and auditable (token never logged; set `Authorization: token <PAT>`).
- Replace the `diff --git` scanner: fetch the whole-PR `.diff` and parse it with a real unified-diff parser (port of `diff_parsing.parse_unified_diff` semantics: strip `a/`/`b/` prefixes, handle `rename from/to`, deleted files via `/dev/null` target, quoted paths, binary-file skip). This fixes upstream's rename/space-in-path bugs instead of inheriting them. Alternative: Gitea also serves per-file patch in the `/files` response (`patch` field) in recent versions — verify live and prefer it if present.
- Keep: empty-page pagination rule; newest-first commit reversal; `event: "COMMENT"` on reviews; API-vs-web URL split (two distinct config values); raw-endpoint `?ref=` file fetch; 404→empty-content semantics; the 50-file full-content cap (make it a config knob).
- `get_issue_comments` must be paginated in the port (upstream bug: unpaginated SDK call caps at the server page size, breaking persistent-comment lookup on busy PRs).
- Labels publish takes **label IDs**, not names — /review's label flow upstream resolves names elsewhere; for review-mcp v1, skip labels on Gitea or resolve names→ids via GET `/repos/{owner}/{repo}/labels`.
- Decode file bytes leniently (utf-8 with replacement; skip binaries by extension) mirroring `decode_if_bytes`.

**Config knobs**

| upstream key | default | keep/drop/rename |
|---|---|---|
| `gitea.url` | `https://gitea.com` | keep as `GITEA_URL` env — **required, no default** (removes upstream's "default-equals-unset" hack entirely) |
| `gitea.personal_access_token` | "" (secret) | keep as `GITEA_TOKEN` env/header, per-user, never logged |
| `gitea.web_url` | unset | keep (optional) — needed for self-hosted split API/web deployments |
| `gitea.ssl_ca_cert` | unset | keep (optional CA path) |
| `gitea.skip_ssl_verification` | false | keep but discourage; log a warning when on |
| `gitea.repo_setting` (`GITEA.REPO_SETTING`) | unset | drop (repo-side `.pr_agent.toml` loading not in v1) |
| `gitea.handle_push_trigger`, `gitea.push_commands` | false / list | drop (webhook server concern) |
| `gitea.webhook_secret` | "" | drop (webhook concern) |
| `MAX_FILES_ALLOWED_FULL` (code constant) | 50 | keep → rename `max_files_full_content`, config knob |

**Risks**

- The whole-PR `.diff` can be huge on large PRs (single request, no size guard upstream). Add a response-size cap.
- Rename metadata lost upstream → inline-comment anchoring on renamed files anchors against the new name with a patch that may include rename hunks; porting with a real parser changes behavior (for the better) — cover with canary tests.
- `base_url_html` derivation depends on the server's `ROOT_URL` being sane; with required explicit config this whole derivation chain can be dropped, but keep `web_url` for split deployments.
- One review POST per `publish_inline_comments` batch; a single invalid anchor can 422 the whole review on some Gitea versions — verify live, consider per-comment fallback.
- Forgejo compatibility is free today but not guaranteed forever; pin tests against both if Forgejo is a target.

DESIGN-QUESTION: Gitea per-file patch source
- Question: build per-file patches by parsing the whole-PR `.diff` (upstream approach, one request) or use the `patch` field of `/pulls/{n}/files` entries (per-file, but version-dependent presence/truncation)?
- Options: (a) whole-diff + robust parser; (b) `/files.patch` with whole-diff fallback; (c) `/files` metadata + local difflib from raw contents (Bitbucket-Server-style).
- Recommendation: (a) for v1 — one request, deterministic, version-independent; verify (b)'s availability during live acceptance and consider it as an optimization later.

DESIGN-QUESTION: Inline publish granularity on Gitea
- Question: one review containing all findings vs one review per finding?
- Options: (a) single review (upstream /improve path), atomic but all-or-nothing on anchor errors; (b) per-finding reviews, noisy timeline but partial success.
- Recommendation: (a) with pre-validation of every anchor against the parsed diff, falling back to appending unanchorable findings into the PR-level comment.

### F.3 Bitbucket Server (Data Center)

**Mechanism — construction & auth**

- Auth: either a **bearer token** (`BITBUCKET_SERVER.BEARER_TOKEN` → `Authorization: Bearer <token>`, an HTTP access token / PAT) or **username+password Basic auth** (`BITBUCKET_SERVER.USERNAME`/`PASSWORD`), via the `atlassian-python-api` `Bitbucket` client. Bearer wins when both set. Cloning works **only** with the bearer token (passed as `http.extraHeader: Authorization: Bearer ...` through git env — `x-token-auth` basic form does not work on Server).
- Base URL: **derived from the PR URL**, not from config — `_parse_bitbucket_server` splits the URL path at `/projects/` (or `/users/` for personal repos) and keeps everything before it as the server root, so context-path deployments (`https://bitbucket.example.com/stash/projects/...`) work. The `[bitbucket_server].url` config key exists (default `""`) but the provider itself never reads it (it serves the webhook server).
- URL parsing (`_parse_pr_url`): finds `projects` or `users` segment, then expects `.../projects/{PROJECT}/repos/{repo}/pull-requests/{id}`; personal repos become workspace `~{user}`.
- Version detection: GET `rest/api/1.0/application-properties` → `version`, parsed with `packaging.parse_version`; failure → `None` (degrades to the oldest diff strategy).

**REST endpoints actually called** (Bitbucket Server REST **1.0** — paths via atlassian-python-api unless marked raw)

| Purpose | Method + path |
|---|---|
| Server version | GET `rest/api/1.0/application-properties` |
| PR metadata | GET `rest/api/1.0/projects/{proj}/repos/{repo}/pull-requests/{id}` (`get_pull_request`) |
| Changed files | GET `.../pull-requests/{id}/changes` (`get_pull_requests_changes`, paginated by the lib) |
| PR commits | GET `.../pull-requests/{id}/commits` (`get_pull_requests_commits`) |
| Branch commits | GET `rest/api/1.0/projects/{proj}/repos/{repo}/commits?since={a}&until={b}` (`get_commits`, for ancestor calc) |
| Merge base (≥8.16) | GET `rest/api/latest/projects/{proj}/repos/{repo}/pull-requests/{id}/merge-base` (raw path built in `_get_merge_base`) |
| File content at rev | GET `rest/api/1.0/projects/{proj}/repos/{repo}/raw/{path}?at={commit}` (`get_content_of_file`) |
| PR-level comment | POST `.../pull-requests/{id}/comments` (`add_pull_request_comment`, body `{text}`) |
| Inline comment | POST `rest/api/latest/projects/{proj}/repos/{repo}/pull-requests/{id}/comments` (raw, `_get_pr_comments_path`) with `{text, severity:"NORMAL", anchor:{diffType:"EFFECTIVE", path, lineType:"ADDED", line, fileType:"TO"}}` |
| Edit comment | PUT `.../comments/{id}` with `{text, version}` (`update_pull_request_comment`; 409 → re-GET comment for current `version`, retry once) |
| Get one comment | GET `.../comments/{id}` (`get_pull_request_comment`, only in the 409 retry) |
| Delete comment | DELETE `.../comments/{id}?version=N` (`delete_pull_request_comment`) |
| List comments | GET `.../pull-requests/{id}/activities?limit=100` (`get_pull_requests_activities`) — filtered to `action=="COMMENTED"`, top-level (`no parent`, no `anchor`), not DELETED |
| Update PR | PUT `.../pull-requests/{id}` with `{version, title, description, reviewers}` (`update_pull_request`) |
| Default branch | GET `.../default-branch` (`get_default_branch`) |

No labels (unsupported), no commit-status/build-status API used by /review, no reactions (`add_eyes_reaction` hard-returns None).

**Pagination**: delegated entirely to `atlassian-python-api`, which internally follows `start`/`nextPageStart`/`isLastPage` for `changes`, `commits`, `activities` (generators; the code wraps them in `list(...)`). The provider itself never pages manually. Activities are fetched with `limit=100` per page.

**Diff format quirks — the big one**: Bitbucket Server's native diff API is *not used at all*. The provider:

1. Determines `head_sha = pr.fromRef['latestCommit']`.
2. Determines `base_sha` by server-version strategy (see below).
3. Lists `changes` (file inventory with `type` ∈ ADD/DELETE/MOVE/RENAME/modify and `path.toString`, `srcPath.toString`).
4. For each valid file, **downloads full base and head contents** via the raw endpoint and **locally regenerates** a unified diff with Python `difflib.unified_diff` (default 3 context lines) via `load_large_diff` → `to_hunk_only_patch` (hunks only, no file headers). `num_plus/minus_lines` counted from the generated patch.
- Renames: `srcPath` → `old_filename`; base content fetched at the old path, head at the new.
- Deletes: head content "", base fetched; Adds: base "".
- A file identical at both revs yields patch "" (filtered downstream).
- Base/head contents decoded with `decode_if_bytes`.

**Base-SHA strategy (version-sensitive)** (`get_diff_files`):
- API ≥ **8.16**: GET `merge-base` endpoint → true merge base (errors raise).
- **7.0 – 8.15**: take `source_commits[-1].parents[0].id` as guaranteed ancestor, GET destination branch commits between it and `toRef.latestCommit`, then `get_best_common_ancestor()` — first parent of a source commit found in the destination set.
- **< 7.0 or unknown**: just `source_commits[-1].parents[0].id` (first parent of the oldest PR commit) — a 3-way-ish approximation that over-reports when the target branch moved.

**Inline comments**: single-line only. The REST payload anchors `diffType: EFFECTIVE`, `fileType: TO`, `lineType: ADDED`, `line: N` — i.e., always a line on the *destination* side of the effective diff. Multi-line ranges are not supported by the API (`publish_inline_comments` collapses `start_line`..`line` to the start line; `_build_code_suggestion_payload` keeps range fields but the publisher ignores them). Line resolution via `find_line_number_of_relevant_line_in_file` against the locally generated diff; not-found → comment skipped with error, partial success still returns True (to prevent duplicate republish).

**Comment editing/versioning (differs from Cloud)**: every comment carries a `version`; PUT/DELETE require the current version and 409 on mismatch; the provider retries the edit once after re-fetching the version. `get_issue_comments` returns `SimpleNamespace(body, id, version, user.login)` sorted by id (the activities feed has no documented order; ids are monotonic — `get_issue_comments_newest_first` sorts desc).

**publish_description**: PR update is a full PUT needing `version`, `title`, and — critically — the existing `reviewers` list, otherwise reviewers get wiped. When title is None the PR is re-fetched first to avoid clobbering a concurrently edited title.

**Incremental review**: constructor accepts `incremental` but it is stored and never used — no incremental support.

**Error handling**: `requests.exceptions.HTTPError` checked by `status_code` (404 = expected-missing for files/settings → ""/skip; 409 = comment version conflict → refresh+retry). Diff-building errors (`merge-base`, commit lists) **raise** — a wrong base silently reviewing the wrong delta is treated as worse than failing. Publish failures log + return False/raise. `_get_pr` wraps the raw dict as `type('new_dict', (object,), pr)` (a class whose attributes are the dict keys — port as a proper struct).

**Capabilities / base-class features NOT supported**: `is_supported('get_labels')` → **False**, `is_supported('gfm_markdown')` → **False** (everything else True); `supports_markdown_tables()` → **True** (DC renders pipe tables, not GFM — review output uses the plain-table degradation path); `publish_labels`/`get_pr_labels` are no-ops; `get_commit_messages()` returns "" (not implemented!); `get_user_id()` returns 0; reactions disabled; `supports_review_comment_identity()` → True (comment author can be matched for persistent comments); no thread resolution, no checkboxes.

**Bitbucket Server vs Bitbucket Cloud — explicit differences** (Cloud = `bitbucket_provider.py`, REST 2.0):

| Aspect | Server/DC (this provider) | Cloud |
|---|---|---|
| REST API | `rest/api/1.0` (+`latest` for comments/merge-base) | `api.bitbucket.org/2.0` |
| Diff source | none — local difflib from full files | GET `.../pullrequests/{id}/diff` (whole PR patch from API) + `diffstat` |
| Base revision | computed (merge-base endpoint / ancestor walk, version-gated) | API provides diff directly |
| Auth | Bearer HTTP-access-token or Basic user/pass | Bearer (OAuth/app password flows) |
| Comment identity | `{text}` + `version` field, 409 retry | `{content: {raw}}`, no version field |
| Inline anchor | `anchor{diffType, path, lineType, line, fileType}` | `inline: {to: line, path}` |
| URL shape | `/projects/{PROJ}/repos/{repo}/pull-requests/{id}` (+ `/users/{u}` → `~u`) | `bitbucket.org/{workspace}/{repo}/pull/{id}` |
| Base URL | parsed from PR URL (self-hosted, context paths) | fixed `bitbucket.org` |
| Comments list | activities feed, filtered | comments endpoint |
| Pagination | `start`/`isLastPage`/`nextPageStart` | `page`/`next` links |
| Labels / GFM | both unsupported | same (labels unsupported, no GFM) |

**Key code**

- `pr_agent/git_providers/bitbucket_server_provider.py:__init__ @ 8e5a929` — auth selection, server-URL derivation from PR URL, version probe.
- `bitbucket_server_provider.py:get_diff_files @ 8e5a929` — the version-gated base-SHA strategy + local difflib patch generation; the heart of this provider.
- `bitbucket_server_provider.py:get_best_common_ancestor @ 8e5a929` — first source-commit parent present in destination history.
- `bitbucket_server_provider.py:publish_inline_comment @ 8e5a929` — EFFECTIVE/TO/ADDED anchor payload.
- `bitbucket_server_provider.py:edit_comment @ 8e5a929` — version-conflict (409) refresh-and-retry.
- `bitbucket_server_provider.py:get_issue_comments @ 8e5a929` — activities → normalized comment objects.
- `bitbucket_server_provider.py:publish_description @ 8e5a929` — full PUT with `version` + `reviewers` preservation.
- `bitbucket_server_provider.py:_parse_bitbucket_server / _parse_pr_url @ 8e5a929` — context-path-safe server/PR parsing incl. `/users/` → `~user`.

**Data flow**

PR URL → derive server root + project/repo/id → GET application-properties (version) → GET pull-request → GET changes (paged) → compute base SHA (merge-base | ancestor walk | first-parent) → per file: GET raw@base + GET raw@head → difflib → `FilePatchInfo[]` → prompts. Publish: PR comment POST; inline findings → N× single-comment POSTs with anchors.

**Porting notes (Go)**

- Thin `net/http` client again; the atlassian lib adds nothing a Go port needs. Implement `start/limit/isLastPage/nextPageStart` paging once, generically.
- Port the **version-gated base-SHA strategy intact**, including the version probe with graceful `nil` fallback — real DC fleets run old versions, and this is exactly the seam hermetic tests cannot catch (live acceptance item). Consider making the strategy overridable by config for stuck instances.
- Local patch generation: use a Go unified-diff lib producing `difflib`-compatible output (3 context lines, hunk-only). The compression layer's line anchoring assumes these patches — keep format identical to the Gitea patch shape (hunks only).
- Full-file downloads mean **2 requests per file**; add a per-file size cap and the 50-file cap here too (upstream Bitbucket Server has *no* `MAX_FILES_ALLOWED_FULL` guard — Gitea does; unify).
- Keep: comment `version` handling with one 409 retry; reviewers-preserving PR PUT (only needed if review-mcp ever edits descriptions — not in v1); activities-feed comment listing sorted by id; `~user` personal-repo workspaces; context-path server parsing.
- `get_commit_messages` is unimplemented upstream — decide whether to implement it in Go via the commits endpoint (cheap, improves prompts) rather than inheriting the gap.
- Raw file endpoint path segments must be URL-escaped per segment (`quote` semantics), and `?at=` takes a commit or `refs/heads/...`.

**Config knobs**

| upstream key | default | keep/drop/rename |
|---|---|---|
| `bitbucket_server.url` | `""` | keep as `BITBUCKET_URL` env — but note upstream's provider derives the server from the PR URL; recommend: require the env var and **validate** the PR URL against it (defense against SSRF via crafted PR URLs — see Risks) |
| `bitbucket_server.bearer_token` | "" (secret) | keep as `BITBUCKET_TOKEN` env/header, per-user |
| `bitbucket_server.username` / `.password` | unset | keep as optional Basic-auth pair (some DC installs only issue user tokens usable as passwords) |
| `bitbucket_server.webhook_secret`, `app_key` | "" | drop (webhook/app server concerns) |
| (none) file-size / file-count caps | — | add new knobs (upstream has none for this provider) |

**Risks**

- **Server URL from PR URL**: upstream trusts the pasted PR URL to name the server, then sends the configured token there. In a per-user-token MCP this is a credential-exfiltration vector (crafted URL → token sent to attacker host). review-mcp must pin the base URL from config and reject PR URLs on other hosts.
- Base-SHA correctness drives the whole review. The <7.0 first-parent fallback can include target-branch commits in the "diff". Flag the active strategy in debug output; verify on the live instance during acceptance.
- Local difflib patches differ from what the Bitbucket UI shows (its EFFECTIVE diff); anchors (`lineType: ADDED`, destination line numbers) usually agree, but whitespace-setting or merge-commit effects can misalign — live canary: publish an inline comment on a known line and verify placement.
- 2×N full-file downloads: slow and memory-heavy on big PRs; no upstream guard.
- Activities feed `limit=100` pages via the lib; if a port forgets paging, persistent-comment lookup silently misses older comments.
- `version` races on comment edit/delete: keep the single refresh-retry, don't loop.

DESIGN-QUESTION: Bitbucket Server diff acquisition
- Question: replicate upstream's full-files + local-difflib approach, or use the server's own diff endpoints (`.../pull-requests/{id}/diff/{path}` streaming, or `/changes`+`/diff` REST) that upstream ignores?
- Options: (a) port upstream as-is (proven, version-tolerant, heavy); (b) native per-file diff endpoint (lighter, but segment/truncation JSON format needs its own parser and has its own version quirks); (c) hybrid: native diff with full-file fallback.
- Recommendation: (a) for v1 — it is the behavior PR-Agent's downstream anchoring logic was tuned against, and correctness beats bandwidth for v1; record (b) as a measured optimization after live acceptance.

DESIGN-QUESTION: Minimum supported Bitbucket DC version
- Question: must review-mcp carry all three base-SHA strategies, or can it require ≥8.16 (merge-base endpoint)?
- Options: (a) all three (max compatibility, most code + test burden); (b) ≥7.0 only (drop the naive first-parent path); (c) ≥8.16 only (one code path).
- Recommendation: (b). DC <7.0 is long out of Atlassian support; keeping the 7.0–8.15 ancestor walk plus the 8.16 merge-base path covers realistic fleets with two tested paths, and the version probe already tells us which to use. Fail with an honest "unsupported server version" message below 7.0.

DESIGN-QUESTION: Basic auth support
- Question: v1 token model is "per-user tokens via env"; do we also carry username/password Basic auth for Bitbucket Server?
- Options: (a) bearer-only (simplest, matches HTTP access tokens in DC ≥5.5); (b) bearer + Basic pair like upstream.
- Recommendation: (a) for v1, with the config surface designed so (b) can be added without breaking changes; DC HTTP access tokens cover the auth need and keep the never-log-secrets surface minimal.

## G. Config surface

Study of PR-Agent's configuration surface at tag v0.47.0 (commit `8e5a929`), scoped to what `review-mcp` v1 needs: `pr_review` + `pr_ask`, the diff/compression pipeline, Gitea + Bitbucket Server providers, and one OpenAI-compatible LLM call. Everything else is catalogued as deliberately dropped.

Upstream `configuration.toml` is 594 lines across ~40 sections; for our v1 only **5 sections contribute keys** (`[config]`, `[pr_reviewer]`, `[pr_questions]`, `[gitea]`, `[bitbucket_server]`) plus the two data files (`ignore.toml`, `generated_code_ignore.toml`) and the extension catalogs in `language_extensions.toml`.

### Consolidated key table

Legend: **keep** = port with same semantics; **keep (data)** = port as embedded data, not user config; **drop** = out of scope for v1 (reason in note); **rename→** = port under proposed Go config name. Proposed names use the shape `section.key` of a single `Config` struct (serialized form and env mapping defined under *Porting notes*).

#### `[config]` — core / model / diff pipeline

| upstream key (section.name) | upstream default | relevant to v1? | recommendation | note |
|---|---|---|---|---|
| config.model | `"gpt-5.6"` | yes | rename→ `llm.model` | **Required, no default** (project rule: no embedded model defaults). Plain model name sent to the OpenAI-compatible endpoint. |
| config.fallback_models | `["gpt-5.6-terra"]` | partial | drop (v1), consider `llm.fallback_models` later | Retry-on-other-model chain. v1: single model, fail honestly. |
| config.model_reasoning / model_weak | commented out | no | drop | Multi-model orchestration; v1 has one model. |
| config.git_provider | `"github"` | yes | rename→ `provider.kind` | Enum `gitea` \| `bitbucket_server`. Required, no default. |
| config.publish_output | `true` | partial | drop (v1) | Upstream decides whether to post to the PR. v1 MCP tools *return* markdown to the client; publishing to the PR is a later feature. If/when added: `output.publish`. |
| config.publish_output_progress, progress_gif_url, progress_gif_width | `true`, `""`, `48` | no | drop | Comment-progress UX for webhook bots; meaningless over stdio. |
| config.verbosity_level | `0` | yes | rename→ `log.level` | Collapse with `config.log_level` (below) into one Go `slog` level. |
| config.log_level | `"DEBUG"` | yes | rename→ `log.level` | One knob, values `debug/info/warn/error`. **Logs go to stderr only** (stdout is the MCP stdio channel). |
| config.use_extra_bad_extensions | `false` | yes | keep → `diff.use_extra_bad_extensions` | Toggles the second binary-extension list (see `language_extensions.toml`). Low priority; acceptable to hard-wire `false` in v1. |
| config.use_repo_settings_file | `true` | yes (decision) | see DESIGN-QUESTION G-1 | Upstream fetches `.pr_agent.toml` from the PR's repo. Security-relevant (reviewed repo influences the reviewer). |
| config.use_global_settings_file | `true` | no | drop | Org-wide `pr-agent-settings` repo lookup; server-deployment feature. |
| config.enable_per_directory_settings (+ per_directory_settings_max_files, _max_tree_pages) | `false`, `20`, `10` | no | drop | Monorepo per-directory config merging; large machinery, GitLab-centric. |
| config.extra_config_url | `""` | no | drop | Remote config fetch (SSRF-hardened upstream). Against our leak-first posture; config comes from env/file on the user's machine only. |
| config.disable_auto_feedback, enable_auto_approval | `false`, `false` | no | drop | Webhook-automation / approval flows. |
| config.ai_timeout | `120` | yes | rename→ `llm.timeout_seconds` | Per-LLM-call timeout. Sensible generic default (e.g. 120) is fine — it is not a URL/endpoint/model. |
| config.retry_same_model_on_timeout | `true` | partial | drop (v1) | Only meaningful with fallback models / retries. v1: one attempt + honest error (or a simple `llm.max_retries`). |
| config.num_retries | unset | partial | rename→ `llm.max_retries` (optional) | Simple bounded retry on transient failure; default 0 or 1. |
| config.skip_keys | `[]` | no | drop | Prompt-variable suppression hook; our prompts are compiled in Go, we control variables directly. |
| config.custom_reasoning_model | `false` | partial | rename→ `llm.no_system_prompt` (defer) | Upstream: disables system message + temperature for non-chat models. Edge case; defer unless a live test against a local model needs it. |
| config.response_language | `"en-US"` | **yes** | rename→ `output.language` | Our explicit requirement: configurable output language, default `"en-US"` (a language default is allowed; it is not a URL/endpoint/model). Injected into prompts as an instruction. |
| config.repo_context_files (+ repo_context_from_default_branch, _max_lines, _sibling_repos, _max_sibling_files) | `["AGENTS.md"]`, `true`, `500`, `[]`, `5` | partial | drop (v1), revisit | Injects repo guidance files (AGENTS.md) into prompts. Nice later feature (`context.repo_files`); non-trivial security surface (sibling-repo reads are host-only upstream). |
| config.max_description_tokens | `500` | yes | keep → `diff.max_description_tokens` | Clips PR description before prompting. |
| config.max_commits_tokens | `500` | yes | keep → `diff.max_commits_tokens` | Clips commit-message block. |
| config.max_model_tokens | `32000` | yes | rename→ `llm.max_input_tokens` | Hard cap on prompt budget regardless of model registry. Since we have no model registry (no embedded model metadata), this becomes the **primary, required-or-defaulted** budget knob. |
| config.custom_model_max_tokens | `-1` | yes (merged) | merge into `llm.max_input_tokens` | Upstream needs it because unknown models have no registry entry; for us every model is "custom", so one knob suffices. |
| config.max_output_tokens | `0` (unset) | yes | rename→ `llm.max_output_tokens` | 0/unset = let endpoint default. Maps to `max_tokens` / `max_completion_tokens`. |
| config.model_token_count_estimate_factor | `0.3` | yes | keep → `diff.token_estimate_factor` | Safety factor over the token estimate; matters because our Go-side counting is an approximation of the server tokenizer. |
| config.image_input_token_allowance | `4096` | no | drop | Image inputs out of scope. |
| config.patch_extension_skip_types | `[".md",".txt"]` | yes | keep → `diff.patch_extension_skip_types` | Skip context-extension for these file types. |
| config.allow_dynamic_context | `true` | yes | keep → `diff.allow_dynamic_context` | Extends hunk context up to enclosing function/class. |
| config.max_extra_lines_before_dynamic_context | `10` | yes | keep → `diff.max_extra_lines_before_dynamic_context` | |
| config.patch_extra_lines_before / patch_extra_lines_after | `5` / `1` | yes | keep → `diff.extra_lines_before` / `diff.extra_lines_after` | Core hunk-extension knobs of the compression pipeline. |
| config.secret_provider | `""` | no | drop | GCS/AWS secret backends; our secrets come from env per user. |
| config.cli_mode | `false` | no | drop | Upstream CLI/server split; we are always a local stdio server. |
| config.output_relevant_configurations / output_run_details / output_run_cost | `false` ×3 | no | drop (v1) | Run-details footer; could return as a debug field in tool output later. |
| config.large_patch_policy | `"clip"` | yes | keep → `diff.large_patch_policy` | `clip` \| `skip` for oversized single patches. |
| config.duplicate_prompt_examples | `false` | yes | keep → `prompts.duplicate_examples` (or hard-wire `false`) | Repeats the output example in the user prompt for weak models. Cheap to port; fine to hard-wire off in v1. |
| config.persistent_inline_comments | `false` | no | drop | Cross-run comment fingerprinting; publishing feature. |
| config.seed | `-1` | yes | rename→ `llm.seed` | Positive value pins seed (upstream also forces temperature 0). Optional. |
| config.temperature | `0.2` | yes | rename→ `llm.temperature` | Default 0.2 is a safe generic default. |
| config.ignore_pr_title / _target_branches / _source_branches / _labels / _authors / ignore_repositories | `["^\\[Auto\\]","^Auto"]`, rest `[]` | no | drop | Webhook-trigger filters. Explicit tool invocation needs no auto-trigger filtering. |
| config.reaction_on_start / _success / _failure | `"eyes"`, `""`, `""` | no | drop | Comment-reaction UX for bot deployments. |
| config.ignore_language_framework | `[]` | yes | keep → `diff.ignore_generated_frameworks` | Selects which generated-code glob groups (from `generated_code_ignore.toml`) are excluded. |
| config.bot_user_indicators | `["codium","bot_",…]` | no | drop | Webhook bot-sender filtering. |
| config.restricted_mode, is_auto_command, propagate_tool_errors | `false` ×3 | no | drop | Deployment/automation plumbing. Our MCP tools always propagate errors as tool errors (MCP has a native error channel). |
| config.enable_ai_metadata | `false` | no | drop | |
| config.add_user_to_requests | `false` | no | drop | Sends command+PR URL in the OpenAI `user` field — a deliberate metadata leak to the LLM provider; against leak-first posture. |
| config.reasoning_effort, additional_reasoning_effort_models, no_temperature_models | `"medium"`, `[]`, long list | partial | rename→ `llm.reasoning_effort` (optional, pass-through) | Upstream depends on litellm's model metadata to decide applicability. We have no metadata: if the user sets `llm.reasoning_effort`, pass it through verbatim and let the endpoint accept/reject. `no_temperature_models` → see DESIGN-QUESTION G-5. |
| config.enable_claude_extended_thinking / extended_thinking_* / enable_claude_adaptive_thinking / claude_*_override | `false`, `2048`, `4096`, `false`, `[]`, `[]` | no | drop (v1) | Anthropic-specific thinking payloads; not part of plain OpenAI-compatible chat completion. Revisit only if a live target needs it. |
| config.extract_issue_from_branch, branch_issue_regex, description_issue_regex | `true`, `""`, `""` | no | drop | GitHub-only ticket extraction; ticket-compliance review is out of v1 scope. |

#### `[pr_reviewer]` — the `/review` tool

| upstream key | upstream default | relevant to v1? | recommendation | note |
|---|---|---|---|---|
| pr_reviewer.require_score_review | `false` | yes | keep → `review.require_score` | Each `require_*` toggle adds/removes a field in the review output schema and prompt. |
| pr_reviewer.require_tests_review | `true` | yes | keep → `review.require_tests` | |
| pr_reviewer.require_estimate_effort_to_review | `true` | yes | keep → `review.require_effort_estimate` | |
| pr_reviewer.require_can_be_split_review | `false` | yes | keep → `review.require_can_be_split` | |
| pr_reviewer.require_security_review | `true` | yes | keep → `review.require_security` | |
| pr_reviewer.require_estimate_contribution_time_cost | `false` | no | drop | Niche field, little value for v1. |
| pr_reviewer.require_todo_scan | `false` | partial | drop (v1) | Easy to add later as `review.require_todo_scan`. |
| pr_reviewer.require_ticket_analysis_review | `true` | no | drop | Depends on ticket providers (Jira/GitHub issues), out of v1. |
| pr_reviewer.require_risk_assessment / require_merge_recommendation / require_priority_files | `false` ×3 | partial | drop (v1), good later candidates | Opt-in structured fields; port after the base schema is stable. |
| pr_reviewer.publish_output_no_suggestions | `true` | no | drop | Comment-publishing UX. |
| pr_reviewer.publish_error_details | `false` | no | drop | Host-only upstream (`config_security.py`); we return errors through MCP directly. |
| pr_reviewer.persistent_comment / persistent_finding_state / review_heading / final_update_message | `true`, `true`, `"PR Reviewer Guide"`, `true` | no | drop | All about updating a persistent PR comment; v1 returns markdown to the client. |
| pr_reviewer.inline_key_issues | `false` | no | drop (v1) | Inline-comment publication; later feature with provider capability checks. |
| pr_reviewer.extra_instructions | `""` | yes | keep → `review.extra_instructions` | Free-text user guidance appended to the prompt. Also a natural **tool argument** (per-call override). |
| pr_reviewer.num_max_findings | `3` | yes | keep → `review.max_findings` | |
| pr_reviewer.enable_review_labels_security / _effort | `true` ×2 | no | drop | PR label publishing. |
| pr_reviewer.require_all_thresholds_for_incremental_review, minimal_commits_for_incremental_review, minimal_minutes_for_incremental_review | `false`, `0`, `0` | no | drop | Incremental review (`/review -i`) out of v1. |
| pr_reviewer.enable_intro_text / enable_help_text / enable_review_coverage_footer | `true`, `false`, `true` | partial | drop intro/help; keep coverage → `review.show_coverage_footer` | The coverage footer ("files omitted by token budget") is an honesty feature — aligns with our degrade-with-a-note rule. Default `true`. |
| pr_reviewer.enable_large_pr_chunking, max_number_of_calls | `false`, `3` | partial | drop (v1), note as v2 | Chunked multi-call review for huge PRs. v1: single call + honest omission note via coverage footer. |

#### `[pr_questions]` — the `/ask` tool

| upstream key | upstream default | relevant to v1? | recommendation | note |
|---|---|---|---|---|
| pr_questions.enable_help_text | `false` | no | drop | |
| pr_questions.use_conversation_history | `true` | partial | drop (v1) → `ask.use_conversation_history` later | Upstream pulls prior `/ask` comment threads from the provider. v1 `pr_ask` is stateless per call; MCP clients carry their own conversation. |
| pr_questions.ask_heading | `"Ask"` | no | drop | Comment-presentation heading. |
| pr_questions.resolve_threads | `false` | no | drop | GitLab-thread resolution feature. |
| pr_questions.extra_instructions | `""` | yes | keep → `ask.extra_instructions` | Same tool-argument duality as review. |

#### Provider sections

| upstream key | upstream default | relevant to v1? | recommendation | note |
|---|---|---|---|---|
| gitea.url | `"https://gitea.com"` | yes | rename→ `gitea.base_url` | **Required when `provider.kind=gitea`, NO default** (project rule: no embedded URLs). |
| gitea.web_url | commented (derived) | yes | keep → `gitea.web_url` (optional) | Split API-vs-browse URL for self-hosted instances; cheap and genuinely useful for anchored links in output. Default: derive from `base_url`/PR html_url. |
| gitea.handle_push_trigger / push_commands | `false`, `[…]` | no | drop | Webhook automation. |
| gitea.personal_access_token (secrets template) | `""` | yes | **env-only secret** → e.g. `REVIEW_MCP_GITEA_TOKEN` | Never in a config file. See Porting notes. |
| gitea.webhook_secret (secrets template) | `""` | no | drop | No webhook server. |
| bitbucket_server.url | `""` | yes | rename→ `bitbucket_server.base_url` | Required when `provider.kind=bitbucket_server`, no default (upstream default is already empty). |
| bitbucket_server.bearer_token (secrets template) | `""` | yes | **env-only secret** → e.g. `REVIEW_MCP_BITBUCKET_TOKEN` | HTTP access token (Bitbucket Server "personal access token", sent as Bearer). |
| bitbucket_server.webhook_secret / app_key (secrets template) | `""` | no | drop | Webhook/Connect-app deployment. |
| bitbucket.identity_request_timeout, bitbucket_app.* | `30`, … | no | drop | Bitbucket **Cloud** section; our target is Server/DC only. |
| openai.key (secrets template) | `""` | yes | **env-only secret** → e.g. `REVIEW_MCP_LLM_API_KEY` | Generic: the API key for whatever OpenAI-compatible endpoint is configured. |
| openai.api_base (secrets template, Azure branch) | commented | yes | rename→ `llm.base_url` | **Required, no default.** Any OpenAI-compatible endpoint (self-hosted or SaaS). Non-secret, so it may live in config file or env. |
| github.*, github_app.*, github_action_config.* | various | no | drop section | GitHub later (explicitly post-v1); the app/Action/webhook keys never return — only a future `github.base_url` + token. |
| gitlab.*, gerrit.*, local.*, azure_devops.*, azure_devops_server.* | various | no | drop sections | Out-of-scope providers. |

#### Data/ignore files (ship as embedded Go data, not user-facing config sections)

| upstream key | upstream default | relevant to v1? | recommendation | note |
|---|---|---|---|---|
| ignore.glob | `['vendor/**']` | yes | keep → `ignore.glob` (user-settable, default `["vendor/**"]`) | File-ignore patterns applied before diff processing. A pattern-list default is data, not an endpoint — allowed. |
| ignore.regex | `[]` | yes | keep → `ignore.regex` | Go `regexp` (RE2) instead of Python `re` — document the dialect difference. |
| generated_code.* (generated_code_ignore.toml: protobuf, openapi, swagger, graphql, grpc_*, go_gen, …) | glob lists per framework | yes | keep (data) → embedded map, selected by `diff.ignore_generated_frameworks` | Pure data; embed via `go:embed` or a Go map. Attribution to PR-Agent required (ported data). |
| bad_extensions.default / bad_extensions.extra (language_extensions.toml) | ~70 + extra extensions | yes | keep (data) → embedded list | Binary/non-reviewable file filter for the diff pipeline. |
| language_extension_map_org (language_extensions.toml) | ext→language map | partial | keep (data, minimal) | Upstream uses it to rank files by main language for compression ordering. Port only if we keep language-ranked sorting; otherwise sort by file size/tokens. |

#### Sections dropped wholesale

| upstream section | why dropped for v1 |
|---|---|
| [pr_description], [pr_code_suggestions], [pr_add_docs], [pr_update_changelog], [pr_config], [pr_help_docs], [pr_similar_issue] | Other tools; v1 is `pr_review` + `pr_ask` only. |
| [github], [github_app], [github_action_config], [gitlab], [gerrit], [local], [azure_devops], [azure_devops_server], [bitbucket] (cloud), [bitbucket_app] | Other providers / webhook-server deployment keys. |
| [litellm] | Python LLM-router plumbing (callbacks, provider inference, streaming workarounds, prompt-cache injection). Our Go client speaks one protocol (OpenAI chat completions) directly; nothing to port. `turn_off_message_logging` is moot: we never log prompt/response bodies at all. |
| [openrouter], [model_routing] | Provider-routing and cost-routing features; v1 = one configured endpoint+model. |
| **[otel] + pr_agent/telemetry/** (config.py, meter, tracer, prometheus, registry, shutdown) | **Deliberately omitted: review-mcp has NO telemetry.** Upstream default is `is_enabled=false`, but the whole subsystem (OTLP/Prometheus exporters, span attributes that can carry PR URLs and error text) does not get ported. Do not add a disabled stub either — no telemetry code at all. |
| [pinecone], [lancedb], [qdrant] | Vector DBs for `/similar_issue`. |
| [skills], [artifacts], [prompt_fragments], [mosaico], [asana], [jira] | Prompt-injection extensions, platform integrations, ticket providers. |
| [push_outputs] | Output sinks (webhook/Slack/file). Host-only upstream for exfiltration reasons; the MCP client already receives the output. |
| custom_labels.toml, pr_custom_labels.toml, all `pr_*_prompts.toml` for non-v1 tools | Labels feature + other tools' prompts. (The two prompt files we DO port — `pr_reviewer_prompts.toml`, `pr_questions_prompts.toml` — are covered in the prompts section of the porting map, not here.) |

### Mechanism (upstream config loading)

Layering, lowest to highest precedence:

1. **Packaged defaults** — `config_loader.py` builds a module-level Dynaconf (`global_settings`) from a fixed list of ~23 TOML files under `pr_agent/settings/` (`configuration.toml`, `ignore.toml`, `generated_code_ignore.toml`, `language_extensions.toml`, all prompt TOMLs, plus gitignored `.secrets.toml` in `settings/` and `settings_prod/`). Core Dynaconf loaders are disabled; a custom in-house loader (`custom_merge_loader.py`) parses each file with `tomllib`, enforces security checks (only `.toml`, 100 MB cap, forbidden `includes`/`preload`/`loaders`/`dynaconf_merge` directives, depth ≤ 50), and accumulates sections field-by-field across files so later files override individual keys, then `set()`s whole sections with merge disabled.
2. **`pyproject.toml` of the local repo** — at import time, `config_loader.py` walks up from CWD to find `.git`, then loads `[tool.pr-agent]` from `pyproject.toml` if present (CLI convenience layer).
3. **`extra_config_url`** — optional remote/org-wide TOML (http/https/file), fetched in `apply_repo_settings()` with 1 MB / 10 s caps and merged section-by-section. Applied *first* among runtime layers so everything else overrides it.
4. **Global settings repo** (`use_global_settings_file`) — a `pr_agent.toml` from an org-level `pr-agent-settings` repo.
5. **Repo-local `.pr_agent.toml`** (`use_repo_settings_file`) — fetched from the reviewed repo's config branch via the git provider, validated by the same security checks, then filtered through `config_security.py` allowlists: whole sections host-only (`push_outputs`, `prompt_fragments`, most of `skills`) and individual host-only keys (`config.extra_config_url`, `config.description_issue_regex`, `config.repo_context_sibling_repos`, `pr_reviewer.publish_error_details`, …) are dropped with a warning.
6. **Environment variables** — Dynaconf's `env_loader` with `envvar_prefix=False`: `SECTION__KEY` with a double underscore, e.g. `CONFIG__GIT_PROVIDER=local`, `OPENAI__KEY=sk-…`, `GITEA__PERSONAL_ACCESS_TOKEN=…`. Env is the top layer; because file merges `set()` whole sections (which would clobber env-sourced values), `_reapply_env_overrides()` replays `env_loader.load()` after every file merge to restore env precedence. `AUTO_CAST_FOR_DYNACONF=false` is forced, so env values are strings unless a file supplied a typed default.
7. **CLI/comment arguments** — `/review --pr_reviewer.extra_instructions=…` style args land last, validated by `CliArgs.validate_user_args` against `CLI_HOST_ONLY_KEYS_BY_SECTION` + the forbidden-args list.

Secrets: same key space as config (e.g. `openai.key` is just a key in the `[openai]` section), expected in gitignored `.secrets.toml` or env vars; optional AWS Secrets Manager / GCS providers fill *unset* keys only (`apply_secrets_manager_config`). There is **no type validation layer**: Dynaconf returns whatever the TOML/env produced, and call sites defend ad hoc (e.g. `get_verbosity_level()` int-coerces with a fallback).

### Key code

- `pr_agent/config_loader.py:global_settings @ 8e5a929` — the Dynaconf instance; fixed settings-file list; custom loader + env loader only; `merge_enabled: False`.
- `pr_agent/config_loader.py:get_settings @ 8e5a929` — request-scoped settings from `starlette_context`, falling back to the global object (server vs CLI split).
- `pr_agent/custom_merge_loader.py:load @ 8e5a929` — in-house TOML loader: per-file security validation, field-level accumulation across files, section-level `set()`.
- `pr_agent/custom_merge_loader.py:validate_file_security @ 8e5a929` — forbidden directives (`includes`, `preload`, `loaders`, `dynaconf_merge`), max nesting depth 50, 100 MB size cap.
- `pr_agent/git_providers/utils.py:apply_repo_settings @ 8e5a929` — runtime layering: extra-config URL → global settings repo → repo-local `.pr_agent.toml` → per-directory files, each followed by env replay.
- `pr_agent/git_providers/utils.py:_reapply_env_overrides @ 8e5a929` — replays `dynaconf.loaders.env_loader` so env stays the top layer after section-wholesale merges.
- `pr_agent/config_security.py:REPO_OVERRIDABLE_KEYS_BY_HOST_SECTION / REPO_HOST_ONLY_KEYS_BY_SECTION / CLI_HOST_ONLY_KEYS_BY_SECTION @ 8e5a929` — the host-only allowlists filtering repo-supplied and comment-supplied settings.
- `pr_agent/settings/configuration.toml @ 8e5a929` — every default; `pr_agent/settings/.secrets_template.toml @ 8e5a929` — the secret key catalog per provider/LLM.
- `pr_agent/telemetry/config.py @ 8e5a929` — OTel config read from `[otel]`; **not ported** (no telemetry in review-mcp).

### Porting notes (Go)

**Proposed shape — one typed struct, env-first, optional single file:**

```go
type Config struct {
    Provider        ProviderConfig        // kind: "gitea" | "bitbucket_server"
    Gitea           *GiteaConfig          // base_url (required if kind=gitea), web_url?
    BitbucketServer *BitbucketServerConfig// base_url (required if kind=bitbucket_server)
    LLM             LLMConfig             // base_url, model (both required), temperature,
                                          // seed?, timeout_seconds, max_input_tokens,
                                          // max_output_tokens, max_retries, reasoning_effort?
    Output          OutputConfig          // language (default "en-US")
    Diff            DiffConfig            // extra_lines_before/after, allow_dynamic_context,
                                          // max_extra_lines_before_dynamic_context,
                                          // patch_extension_skip_types, large_patch_policy,
                                          // max_description_tokens, max_commits_tokens,
                                          // token_estimate_factor, ignore_generated_frameworks
    Ignore          IgnoreConfig          // glob, regex
    Review          ReviewConfig          // require_* toggles, max_findings,
                                          // extra_instructions, show_coverage_footer
    Ask             AskConfig             // extra_instructions
    Log             LogConfig             // level (stderr only)
}
```

- **Loading order (mirrors upstream, minus the layers we drop):** compiled defaults (Go struct literals — only for non-URL/endpoint/model keys) → optional config file (path from `REVIEW_MCP_CONFIG` or a conventional location, see G-2) → environment variables → per-call tool arguments (`extra_instructions`, possibly per-call toggles). No remote config, no global settings repo, no pyproject equivalent. Repo-local file: see G-1.
- **Env naming:** flat `REVIEW_MCP_` prefix with single-underscore word separation and a fixed key table (`REVIEW_MCP_PROVIDER`, `REVIEW_MCP_GITEA_BASE_URL`, `REVIEW_MCP_LLM_BASE_URL`, `REVIEW_MCP_LLM_MODEL`, …) rather than Dynaconf-style generic `SECTION__KEY` reflection. A fixed table gives: typo detection ("unknown REVIEW_MCP_* variable" warning), typed parsing with errors at startup, and a greppable docs page. See G-3.
- **Secrets stay out of files:** the three secrets (`REVIEW_MCP_GITEA_TOKEN`, `REVIEW_MCP_BITBUCKET_TOKEN`, `REVIEW_MCP_LLM_API_KEY`) are **env-only — the file loader rejects (hard error, naming the key) any token/key/secret field found in a config file.** This is stricter than upstream (which happily reads `.secrets.toml`) and matches the MCP reality: the client config (e.g. an `mcpServers` block) passes env per user. Secret values never appear in logs or error strings; redact on echo.
- **Validation at startup, not at call time:** unlike upstream's "whatever Dynaconf returns" approach, validate once when the server starts (or at first tool call): required keys present for the chosen provider (`provider.kind` set; matching `base_url` + token; `llm.base_url` + `llm.model` + key), URLs parse as http(s), numeric ranges sane (`temperature` 0–2, positive timeouts, `large_patch_policy` ∈ {clip, skip}). Fail with one aggregated, token-free error message listing every missing/invalid key — stdio servers die silently otherwise and users see only a dead client.
- **No defaults for URLs/endpoints/models** (project rule): `llm.base_url`, `llm.model`, `gitea.base_url`, `bitbucket_server.base_url` have zero-value defaults and are required by validation. Upstream's `gitea.url="https://gitea.com"` and `model="gpt-5.6"` defaults are explicitly **not** carried over. Numeric/boolean pipeline defaults (temperature, patch lines, token caps) are carried over as compiled defaults — they are behavior data, not environment assumptions.
- **Dropped security machinery that must not silently reappear:** `config_security.py` exists because upstream lets the *reviewed repo* inject config. If G-1 resolves to "no repo-local config in v1", none of the allowlist machinery is needed — but the moment a repo-sourced config layer is added, a Go equivalent of the host-only key filter must come with it.
- **Env replay problem is moot in Go:** upstream's `_reapply_env_overrides()` dance exists because Dynaconf merges whole sections; a Go loader that applies layers field-by-field in a fixed order has no such hazard.
- **Data files:** port `ignore.toml` defaults, `generated_code_ignore.toml` globs and `bad_extensions` lists as embedded Go data (`go:embed` of TOML/JSON or generated Go maps), with PR-Agent attribution in NOTICE. Convert Python `glob`/`re` semantics to Go (`path.Match`/`doublestar` for `**` globs; RE2 for regex — document that backreferences/lookarounds from user regex are unsupported).

### Risks

- **Regex/glob dialect drift:** upstream user-facing patterns (`ignore.regex`, `ignore_pr_title`-style regexes) are Python `re`; Go RE2 rejects lookarounds/backreferences and `**` is not native in `path.Match`. Users copying PR-Agent patterns into review-mcp may get different matches. Mitigate: doublestar library for globs, startup-time regex compile errors, docs note.
- **Token-budget semantics without a model registry:** upstream resolves `max_model_tokens` against litellm's per-model metadata; we have none, so a wrong user-supplied `llm.max_input_tokens` silently over- or under-clips the diff. Mitigate: required-with-explicit-default, coverage footer always reports clipping.
- **Env strings vs types:** everything arriving via env is a string; sloppy parsing (e.g. `"true "`, `"0.2,"`) must fail loudly at startup, not default silently — silent fallback was an upstream pattern (`get_verbosity_level`) we should not copy for behavior-critical keys.
- **Config key explosion:** upstream's 594-line surface grew by accretion. Every key we keep is API surface for a v0.1 project; the table above already trims ~85% — resist re-adding keys "for parity" without a live-use driver.
- **Secrets in client configs:** MCP client configs (JSON with env blocks) live in user dotfiles; we cannot prevent users committing them. Docs must show the env-reference pattern of each client (e.g. passing through from the parent environment) and never show literal tokens in examples.
- **`extra_instructions` as prompt injection:** config- or argument-supplied free text goes verbatim into the prompt (upstream behaves the same). For a per-user local tool this is the user instructing their own reviewer — acceptable — but worth a docs note once outputs can be published to shared PRs.

### DESIGN-QUESTION blocks

**DESIGN-QUESTION G-1: Repo-local config file in v1?**
- *Question:* Should review-mcp read a config file from the reviewed repository (upstream `.pr_agent.toml`, `use_repo_settings_file=true`), e.g. a `.review-mcp.toml` fetched via the provider API, letting repo maintainers tune review behavior per repo?
- *Options:* (a) No repo-sourced config in v1 — config comes only from the user's own env/file/tool-arguments. (b) Port it, including a Go version of the `config_security.py` host-only-key allowlist. (c) Read a repo-local file only from the user's *local working copy* path if provided, never via the provider API.
- *Recommendation:* **(a).** The repo-local layer is upstream's single largest security surface (an entire module of allowlists exists to contain it) and its value is low for a per-user stdio tool where the invoking user already controls all config. Revisit when/if review-mcp grows team/CI usage. Keep the loader layered so inserting the layer later is mechanical.

**DESIGN-QUESTION G-2: Config file — have one at all, and in what format?**
- *Question:* Is env alone enough for v1, or do we support an optional config file; and if so, TOML, YAML, or JSON?
- *Options:* (a) Env-only: ~25 keys, all settable from the MCP client's env block; zero file-discovery logic. (b) Env + optional TOML file at `REVIEW_MCP_CONFIG` (no default search path), env overrides file. (c) Env + file with XDG default search path (`~/.config/review-mcp/config.toml`).
- *Recommendation:* **(b) with TOML.** Pure env gets ugly for list/nested values (`ignore.glob`, require-toggles) and MCP client env blocks are a poor place for 20 lines of tuning; a file referenced by one env var keeps client config to 4–5 env entries (config path + secrets). TOML over YAML/JSON: upstream parity makes porting docs/examples trivial, Go has solid decoders (BurntSushi/pelletier), comments are supported, and no YAML type-coercion footguns. Avoid (c) in v1 — implicit search paths create "works on my machine" config invisibility; an explicit path is self-documenting in the client config.

**DESIGN-QUESTION G-3: Env naming scheme**
- *Question:* How do env vars map to config keys?
- *Options:* (a) Dynaconf-style generic reflection `REVIEW_MCP_<SECTION>__<KEY>` (double underscore as section separator), auto-derived from struct tags. (b) Fixed hand-written table of flat names (`REVIEW_MCP_LLM_BASE_URL`, `REVIEW_MCP_REVIEW_MAX_FINDINGS`, …). (c) Hybrid: generic reflection plus a short alias table for the common keys.
- *Recommendation:* **(b).** With ~25 keys a fixed table costs little and buys exact docs, startup "unknown variable" warnings, and freedom to keep names ergonomic (`REVIEW_MCP_GITEA_TOKEN`, not `REVIEW_MCP_GITEA__PERSONAL_ACCESS_TOKEN`). Generic reflection is what forced upstream into the env-replay workaround and undocumentable key spellings.

**DESIGN-QUESTION G-4: Per-call overrides vs static config**
- *Question:* Which config keys should also be MCP tool arguments on `pr_review`/`pr_ask` (overriding static config per call)?
- *Options:* (a) None — tools take only `pr_url` (+ `question` for ask); all tuning is static. (b) A small curated set: `extra_instructions`, `output_language`, maybe `max_findings`. (c) Full passthrough of any config key as a tool argument (upstream's `--section.key=value` comment-args model).
- *Recommendation:* **(b).** `extra_instructions` is per-task by nature ("focus on concurrency"), and language can legitimately vary per call. Full passthrough (c) reimports upstream's host-only-key problem and bloats the tool schema the LLM client must read. Document that tool arguments beat env beats file.

**DESIGN-QUESTION G-5: Temperature/parameter stripping for strict endpoints**
- *Question:* Upstream maintains `no_temperature_models` and litellm metadata to avoid sending `temperature`/`reasoning_effort` to models that reject them. With no model registry, how does review-mcp avoid hard failures against strict OpenAI-compatible endpoints (e.g. o-series style models rejecting temperature)?
- *Options:* (a) Always send configured parameters; let the endpoint error and surface it. (b) A boolean `llm.omit_sampling_params` that drops temperature/seed from the request. (c) Only send a parameter when the user explicitly set it (no compiled default for temperature), so an unset config sends a minimal request.
- *Recommendation:* **(c), with (a)'s honesty.** Minimal-by-default requests are maximally compatible with the long tail of OpenAI-compatible servers and fit the no-embedded-model-assumptions rule; a user who sets `temperature=0.2` gets it sent verbatim and owns the compatibility consequence, with the endpoint's error relayed unmodified. Avoid (b): one more boolean that interacts confusingly with explicit values. This drops upstream's `temperature=0.2` default — note it in the porting map as a deliberate behavior change.

---

## Attribution

This document is a study of [PR-Agent](https://github.com/The-PR-Agent/pr-agent),
an open-source, AI-powered code review agent licensed under the MIT License,
at tag **v0.47.0**, commit **8e5a9295973b24af4b70cafd0b660a230811ef9e**. The
diff compression strategy, token budgeting approach, tool prompt design,
structured output schemas, repair tactics, and provider behaviors described
here were designed and battle-tested by the PR-Agent authors, maintainers, and
community — review-mcp ports this design to Go with gratitude. See the
repository's `NOTICE` file for the project-level attribution statement.

No PR-Agent source code is reproduced in this repository. Code references are
given as `path:function @ 8e5a929` pointers into the pinned upstream revision;
short illustrative pseudocode and configuration key/default listings are used
for documentation purposes only.
