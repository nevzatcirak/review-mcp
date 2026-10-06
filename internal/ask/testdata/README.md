# Prompt goldens for `internal/ask`

`prompts/<case>/` holds one prompt-golden case (spec P5 §1.1):

| File | Written by | Meaning |
|---|---|---|
| `case.json` | `oracle/make_cases.py` | `extra_instructions`, `language` (output language), `main_language`, and the PR fields `title`, `branch`, `description`, `question`, `diff` |
| `upstream.system.txt`, `upstream.user.txt` | `oracle/render_upstream.py` | **upstream's** prompts: PR-Agent at `8e5a9295973b24af4b70cafd0b660a230811ef9e` rendering its own `pr_questions_prompts.toml` with matching variables (MIT data, see `NOTICE`) |
| `system.txt`, `user.txt` | `go test -run TestPromptGoldens -update` | **our** prompts: `RenderPrompts` on the case |
| `upstream.diff` | `go test -run TestPromptGoldens -update` | unified diff (1 line of context) from upstream's prompts to ours |

`TestPromptGoldens` (`../golden_test.go`) renders every case, compares it
with `system.txt` / `user.txt`, then recomputes the diff against the
upstream files and compares it with `upstream.diff`. The oracle is not run
by the tests; regenerate it as described in `oracle/README.md`.

## Cases

| Case | Extra instructions | Output language | Main language | Other |
|---|---|---|---|---|
| `base` | — | en-US | Go | |
| `extra` | yes | en-US | Go | |
| `no_main_language` | — | en-US | (none) | line omitted |
| `other_main_language` | — | en-US | `Other` | line omitted (the oracle maps `Other` to an empty language) |
| `no_description` | — | en-US | Go | the `Description` block is omitted |
| `non_english` | — | tr-TR | Go | |
| `non_english_extra` | yes | de-DE | Go | |
| `multiline_question` | — | en-US | Go | multi-line question with a trailing newline (trimmed) |

## Differences from upstream (the content of the `upstream.diff` files)

The oracle switches off the parts review-mcp removes (`skills_context`,
`conversation_history`), so their removal produces no diff lines; the
templates do not contain them. The only difference, identical in every
case, is the honesty deviation in the system prompt (spec P5 §1.1):

1. upstream's two-sentence must-answer instruction `Don't avoid answering
   the questions. You must answer the questions, as best as you can,
   without adding any unrelated content.` is replaced by `Answer only from
   the PR information and diff provided. If the answer cannot be determined
   from them, say so explicitly and state what information is missing. Do not
   guess.` (the first sentence is removed too, because it contradicts the
   honesty instruction);
2. one added line: `The diff may list files that were omitted because of its
   size; do not draw conclusions about the content of those files.`

The whole user prompt, the role statement, the first two sentences of the
goal paragraph, the extra-instructions block with its precedence sentence and
the output-language sentence with its separator are byte-identical to
upstream's renderings.
