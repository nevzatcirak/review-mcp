# Prompt goldens for `internal/review`

`prompts/<case>/` holds one prompt-golden case (spec P4 §4.2):

| File | Written by | Meaning |
|---|---|---|
| `case.json` | `oracle/make_cases.py` | toggles (`effort`, `tests`, `security`, `performance`; the oracle ignores `performance`, see difference 3), `max_findings`, `extra_instructions`, `language`, the PR fields `title`, `branch`, `description`, `date`, `diff`, and, for `with_discussion`, the rendered `discussion` block (ours; the oracle ignores it, see difference 4) |
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

| Case | Toggles (effort, tests, security, performance) | Language | Extra instructions | Other |
|---|---|---|---|---|
| `all_fields` | on, on, on, on | en-US | — | |
| `all_fields_extra` | on, on, on, on | en-US | yes | |
| `key_issues_only` | off, off, off, off | en-US | — | |
| `key_issues_only_extra` | off, off, off, off | en-US | yes | |
| `non_english` | on, on, on, on | tr-TR | — | |
| `non_english_extra` | on, on, on, on | de-DE | yes | |
| `tests_and_security_no_description` | off, on, on, off | en-US | — | empty description (the `PR Description` block is omitted) |
| `effort_only_max_findings_5` | on, off, off, off | en-US | — | `max_findings` 5 |
| `with_discussion` | on, on, on, on | en-US | — | the existing-discussion block (X-13); its `upstream.*` files are those of `all_fields` |

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
3. **Performance field** (X-12, spec P7 §4.1; an intentional deviation):
   upstream has no `performance_concerns`. With the performance toggle on,
   our `Review` class gains its schema line after `security_concerns`, and
   the example gains `performance_concerns: |` / `No` after the security
   answer. The description is ours: the spec wording plus security's
   no-translation sentence ("Answer with the exact English literal 'No', …";
   lead decision, reported to the architect as a DESIGN-QUESTION), so that
   the No-detector works under any output language. (`all_fields`, `all_fields_extra`,
   `non_english`, `non_english_extra`; the cases with the toggle off show
   that switching it off removes both lines.)

4. **Existing-discussion block** (X-13, spec P7 §5.2; an intentional
   deviation): upstream has no block for what people already said on the
   PR. When the pipeline supplies one, our user prompt has it after the
   description and before the diff: a fixed header that says the text is
   data, then the threads inside an adaptive backtick fence (one backtick
   longer than the longest run in the comments). Without a discussion the
   user prompt is byte-identical to upstream's. The block of this case is
   `discussion/sample.txt`, which `TestDiscussionGolden` pins from a thread
   list (a comment cut, replies capped at 2, a resolved thread, a thread of
   ours whose root is excluded, a foreign comment carrying our marker, a
   code fence in a comment); `TestPromptGoldens` checks that `case.json`
   holds the same text. (`with_discussion`; upstream's files for it are a
   copy of `all_fields`', since upstream has no such input.)

There are no other differences: the role, the diff-format notes (upstream's
numbered `diff_hunk_format`), "Determining what to flag", "Constructing
comments", the extra-instructions block, the output-language sentence and
its separator, the descriptor-generated schema classes (including
upstream's `Field("…")` form on the `key_issues_to_review` line), the
closing instruction and the whole user prompt (without a discussion) are byte-identical to
upstream's renderings. Case `tests_and_security_no_description` has an
empty diff.

`discussion/sample.txt` is the rendered discussion block that
`TestDiscussionGolden` compares (`go test -run TestDiscussionGolden -update`
rewrites it; rerun `oracle/make_cases.py` afterwards so that
`prompts/with_discussion/case.json` follows).
