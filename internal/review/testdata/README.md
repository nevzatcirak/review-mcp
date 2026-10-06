# Prompt goldens for `internal/review`

`prompts/<case>/` holds one prompt-golden case (spec P4 §4.2):

| File | Written by | Meaning |
|---|---|---|
| `case.json` | `oracle/make_cases.py` | toggles (`effort`, `tests`, `security`), `max_findings`, `extra_instructions`, `language`, and the PR fields `title`, `branch`, `description`, `date`, `diff` |
| `upstream.system.txt`, `upstream.user.txt` | `oracle/render_upstream.py` | **upstream's** prompts: PR-Agent at `8e5a9295973b24af4b70cafd0b660a230811ef9e` rendering its own `pr_reviewer_prompts.toml` with matching variables (MIT data, see `NOTICE`) |
| `system.txt`, `user.txt` | `go test -run TestPromptGoldens -update` | **our** prompts: `RenderPrompts` on the case |
| `upstream.diff` | `go test -run TestPromptGoldens -update` | unified diff (1 line of context) from upstream's prompts to ours; empty when they are identical |

`TestPromptGoldens` (`../golden_test.go`) renders every case and compares
it with `system.txt` / `user.txt`, then recomputes the diff against the
upstream files and compares it with `upstream.diff`. A change to the
templates or the descriptor table therefore shows up as a changed diff
against upstream, which must stay within the expected differences below.
The oracle is not run by the tests; regenerate it as described in
`oracle/README.md`.

## Cases

| Case | Toggles (effort, tests, security) | Language | Extra instructions | Other |
|---|---|---|---|---|
| `all_fields` | on, on, on | en-US | — | |
| `all_fields_extra` | on, on, on | en-US | yes | |
| `key_issues_only` | off, off, off | en-US | — | |
| `key_issues_only_extra` | off, off, off | en-US | yes | |
| `non_english` | on, on, on | tr-TR | — | |
| `non_english_extra` | on, on, on | de-DE | yes | |
| `tests_and_security_no_description` | off, on, on | en-US | — | empty description (the `PR Description` block is omitted) |
| `effort_only_max_findings_5` | on, off, off | en-US | — | `max_findings` 5 |

## Differences from upstream (the content of the `upstream.diff` files)

The oracle switches off every upstream part review-mcp removes (related
tickets, todo scan, can-be-split, score, risk level, merge recommendation,
priority files, contribution time cost, the question/answer block, skills
and repository context, AI metadata, duplicated examples), so their
removal produces no diff lines; the templates simply do not contain them.
The remaining differences, all expected:

1. **Renamed effort key** (X-4): upstream's `estimated_effort_to_review_[1-5]`
   is `estimated_effort_to_review` in the `Review` class and in the example.
   The description, which carries the 1–5 range, is unchanged. (Every case
   with the effort toggle on.)
2. **Example follows the toggles:** upstream's example always shows
   `relevant_tests` and `security_concerns`, even when the schema does not
   ask for them; ours is generated from the descriptor table and shows only
   the enabled fields. (`key_issues_only`, `key_issues_only_extra`,
   `effort_only_max_findings_5`.)

There are no other differences: the role, the diff-format notes (upstream's
numbered `diff_hunk_format`), "Determining what to flag", "Constructing
comments", the extra-instructions block, the output-language sentence and
its separator, the descriptor-generated schema classes (including
upstream's `Field("…")` form on the `key_issues_to_review` line), the
closing instruction and the whole user prompt are byte-identical to
upstream's renderings. Case `tests_and_security_no_description` has an
empty diff.
