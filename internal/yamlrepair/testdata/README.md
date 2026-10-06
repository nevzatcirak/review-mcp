# Fixtures and parity goldens for `internal/yamlrepair`

Each directory under `cases/` is one fixture: a raw model answer, the keys
it is loaded with, and the expected parse.

The `golden.json` files are **outputs of PR-Agent**
(https://github.com/The-PR-Agent/pr-agent) at commit
`8e5a9295973b24af4b70cafd0b660a230811ef9e` (tag v0.47.0). They were
produced by running upstream's `load_yaml` on the inputs next to them (see
[`oracle/README.md`](oracle/README.md)). Many inputs are the inputs of
upstream's own unit tests. Both are MIT data from PR-Agent and are
attributed through the repository's `NOTICE` (X-7, spec P4 §3, §6.4).
No upstream code is part of this repository.

Every `golden.json` is **oracle-generated**. The only expectations that are
not are the eight `deviation.json` files, which are **hand-derived** (see
[Hand-derived expectations](#hand-derived-expectations)).

## Layout

| File | Written by | Meaning |
|---|---|---|
| `input.txt` | `oracle/make_cases.py` | the raw answer, byte for byte (no trailing newline is added) |
| `case.json` | `oracle/make_cases.py` | `names`, `first`, `last` (the `Keys`), `source`, and `canary` (the tactic name) for a canary fixture |
| `golden.json` | `oracle/gen_goldens.py` | `upstream` (the JSON-normalized `load_yaml` result), `upstream_type` (`dict`, `list`, ...), `upstream_tactic` (`direct`, `none`, or upstream's tactic number 1-12) |
| `deviation.json` | by hand | `reason`, `want`, `tactic`: the expectation where review-mcp deliberately differs from upstream |

The oracle passes `keys_fix_yaml = [name + ":" for name in names]`, which
is exactly the match string tactic 1 uses for `Keys.Names` in Go.

## How the test compares (`golden_test.go`)

- Results are compared as **parsed structures**, after a JSON round trip on
  both sides, never as text.
- A non-mapping upstream result (a list, a scalar, `None`) is expected as an
  empty map: `Load` returns only mappings, and the caller treats both as
  "no review".
- The trace must name upstream's winning tactic (`direct` and `none`
  included). A case that upstream wins with tactic 3 or 10, or with a
  non-mapping result from a tactic, must carry a `deviation.json`; the test
  fails otherwise.
- Only the dialect drift rules below are tolerated.

## Dialect drift (PyYAML 6.0.1 vs gopkg.in/yaml.v3 v3.0.1)

PyYAML resolves YAML 1.1 scalars, yaml.v3 YAML 1.2. `Load` keeps yaml.v3's
resolution and normalizes nothing, except that timestamps stay strings
(yaml.v3 would return `time.Time`). The comparison tolerates exactly two
rules, both exercised (and asserted to be needed) by `rv_yaml11_words` in
`TestDriftRules`:

| Rule | Input | PyYAML | Load | Consequence |
|---|---|---|---|---|
| R1 | `yes`, `no`, `on`, `off` (three case forms) | `True` / `False` | the string | `relevant_tests: Yes` arrives as `"Yes"`, `security_concerns: No` as `"No"`; `IsNo` accepts both shapes, and the review descriptors (WP-PR-4c) normalize `relevant_tests` |
| R2 | `1e3` (exponent, no dot) | the string `"1e3"` | the number 1000 | none for v1 fields |

Known differences no fixture relies on (not tolerated by the comparator; a
fixture hitting one would fail and need a decision):

- `0o17`: PyYAML a string, yaml.v3 the integer 15. (`0755` is 493 in both.)
- `1:20` (sexagesimal): PyYAML the integer 80, yaml.v3 the string.
- `2024-01-01T10:00:00Z`: PyYAML a `datetime` (JSON-normalized as
  `2024-01-01 10:00:00+00:00`), `Load` the string as written. A date
  without a time (`2024-01-01`) normalizes to the same string in both.
- Duplicate mapping keys: PyYAML keeps the last value. yaml.v3's own
  decoder rejects the document, so `parse` converts yaml.v3's node tree
  itself and keeps the last value too (`rv_duplicate_keys`,
  `TestDuplicateKeysLastWins`).
- Tags: PyYAML's `safe_load` rejects unknown tags such as `!foo`; yaml.v3
  ignores them. `parse` rejects them, like PyYAML.
- Several documents (`---`): PyYAML rejects them; yaml.v3's `Unmarshal`
  silently keeps the first. `parse` rejects them, like PyYAML.

## Canaries (spec P4 §3)

One fixture per kept tactic that **only** that tactic repairs: the direct
parse and every earlier tactic fail, and the tactic succeeds. Upstream wins
each of them with the same tactic (`golden.json`). `TestCanaryTrace`
asserts the trace name and that the chain cut before the tactic repairs
nothing.

| Upstream tactic | Name | Fixture |
|---|---|---|
| 1 | `block_scalar_keys` | `c01_block_scalar_keys` |
| 2 (1.5) | `explicit_indent` | `c02_explicit_indent` |
| 4 | `fenced_snippet` | `c04_fenced_snippet` |
| 5 | `strip_braces` | `c05_strip_braces` |
| 6 | `key_window` | `c06_key_window` |
| 7 | `strip_plus` | `c07_strip_plus` |
| 8 (5.5) | `diff_markers` | `c08_diff_markers` |
| 9 | `tabs_to_spaces` | `c09_tabs_to_spaces` |
| 11 | `root_pipe` | `c11_root_pipe` |
| 12 | `reencode` | `c12_reencode` |

## Review keys and tactic 1

The review fixtures use the v1 review keys as `Keys.Names` (the
descriptor-derived list is WP-PR-4c's). Upstream's own review key list
includes `key_issues_to_review:`. Tactic 1 then turns that list key into an
empty block scalar, and the list that follows no longer parses, so tactic 1
can never repair a review that has key issues: upstream returns nothing for
`rv_list_colon_all_keys`, while the same answer with the list key left out
of `Names` is repaired by tactic 1 (`rv_list_colon_leaf_keys`). This is
upstream behaviour, reproduced exactly; whether the review descriptors
should leave list-valued keys out is a decision for WP-PR-4c.

Upstream's tactic 1 also always adds a built-in key list for other tools
(`relevant line`, `suggestion content`, `relevant file`, `existing code`,
`improved code`, `label`, `why`, `suggestion_summary`). review-mcp does not
(DQ-8). Fixtures taken from upstream tests pass these keys explicitly in
`names`, so both sides use the same list; the review fixtures do not contain
them.

## Hand-derived expectations

Each was derived by stepping through the kept tactics by hand on the
preprocessed input, then confirmed against the Go result.

| Fixture | Upstream | review-mcp | Reason |
|---|---|---|---|
| `up_load_invalid2` | tactic 1, a list | `none`, empty | a tactic wins only with a non-empty mapping (spec P4 §3); no later tactic yields one |
| `up_tf_brace_after_else` | tactic 3 | `strip_braces`, the value without its closing `}` line | tactic 3 not ported (DQ-8); tactic 5 removes the trailing `}` |
| `up_tf_brace_in_string` | tactic 3 | `strip_braces`, the value without its closing `}` line | as above |
| `up_tf_brace_insufficient` | tactic 3 | `strip_braces`, the value without its closing `}` line | as above |
| `up_tf_stray_brace` | tactic 3 | `strip_braces`, upstream's value | as above; here the removed `}` was stray |
| `up_tf_stray_brace_before_key` | tactic 3 | `none`, empty | the stray `}` is not at the end; the caller re-asks (DQ-9) |
| `up_tf_wrong_indentation` | tactic 10 | `none`, empty | tactic 10 (improve/describe sections) not ported (DQ-8) |
| `up_tt_second_block` | tactic 10 (value absorbs both fences) | `none`, empty | as above |

## Sources

### Reused upstream unit-test inputs

All at `tests/unittest/` in the pinned upstream checkout.

| Upstream test file | Tests reused (fixture prefix) |
|---|---|
| `test_load_yaml.py` | all: `test_load_valid_yaml`, `test_load_invalid_yaml1`, `test_load_invalid_yaml2`, both control-character tests, the mojibake test, the sanitized-to-empty and empty-input tests, the info-string tests (space, case, non-YAML labels), the snippet-with-prefix test, and the three `TestFenceLabelIsNotStrippedByPrefix` tests (`up_load_*`, `up_control_*`, `up_mojibake`, `up_*empty`, `up_fence_*`, `up_snippet_*`, `up_yml*`) |
| `test_load_yaml_closing_fence.py` | all seven (`up_cf_*`) |
| `test_load_yaml_trailing_text.py` | all except `test_a_fence_inside_a_block_scalar_is_not_treated_as_the_wrapper`, whose input is identical to `up_cf_inner_fence` (`up_tt_*`) |
| `test_load_yaml_unparseable.py` | the unparseable input, the parseable review, and the three non-list `code_suggestions` payloads (`up_unparseable`, `up_parseable_review`, `up_non_list_payload_*`) |
| `test_try_fix_yaml.py` | all, through `load_yaml` (`up_tf_*`) except `test_empty_yaml_fixed` (same input as `up_empty`) |
| `test_output_models.py` | the two `load_yaml` inputs (`up_effort_int`, `up_ranking`) |
| `test_markdown_ticket_output_core.py` | `TestIsValueNo` inputs, in `TestIsNo` |
| `test_review_security_concerns_shape.py` | the "no" answers, in `TestIsNo` |

The `test_try_fix_yaml.py` inputs run through `load_yaml` (the function
`Load` ports), so preprocessing applies to them too; their expected values
come from the oracle, not from the upstream assertions.

### Skipped upstream inputs

- The tool-level tests in `test_load_yaml_unparseable.py`
  (`PRReviewer`, `PRCodeSuggestions`, `PRGenerateLabels`, the coverage
  footer): they feed the same unparseable input to other tools' code.
- `test_pr_description.py`: its predictions are built by the describe
  tool's multi-call pipeline, which v1 does not have.
- `test_pr_reviewer_core.py`, `test_pr_reviewer_finding_state.py`,
  `test_review_large_diff_chunking.py`: they patch `load_yaml` out.
- `test_review_merge_field_shapes.py`: it tests the chunk-merge path, not
  ported in v1.

### Synthetic inputs

`rv_*` (well-formed and edge-case review answers) and the `c*` canaries
were written for review-mcp. Their goldens are oracle-generated too.
