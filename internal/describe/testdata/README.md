# Goldens for `internal/describe`

All files below are written by `go test ./internal/describe -run <Test> -update`
after the change has been checked by hand. There is no upstream oracle for
pr_describe: the upstream renderings of the ask and review prompts were made by
running PR-Agent, which was not done for this tool, so these goldens pin
review-mcp's own renderings only. The adaptations from upstream are listed in
the header comments of `../prompts/*.tmpl`.

## `prompts/<case>/` — `TestPromptGoldens`

`case.json` is written by hand: `kind` (`call` for one call or a part,
`reduce` for the reduce call), `language` (output language), `title`,
`branch`, `target_branch`, `description`, `commit_messages` (already numbered),
`files_only`, `part_header`, `diff`, `walkthrough`. `system.txt` and `user.txt`
are the rendered prompts.

| Case | Kind | Other |
|---|---|---|
| `base` | call | en-US, every PR field set |
| `no_description` | call | no description, no commit messages, no target branch: those blocks are omitted |
| `non_english` | call | tr-TR: the extra-instructions block carries only the language instruction |
| `part` | call | part-mode variant (`files_only`) with the part line of part 2 of 3 |
| `reduce` | reduce | the files walkthrough in place of the diff |
| `reduce_non_english` | reduce | de-DE |

## `runs/<run>/` — `TestOneCallRun`, `TestThreePartRun`

The prompts of every model call of a run with the fake LLM of
`pipeline_test.go`, and the structured result (`result.json`):

- `one_call/`: two changed files and a filtered one, one call.
- `three_parts/`: six files with `diff.max_tokens` 1500, three part calls
  (`part1`…`part3`) and the reduce call (`reduce`).

`../render/testdata/*.md` are the client renderings of these results
(`TestClientGoldens` in `../render`), so a change to a run golden shows up in
the rendering goldens too.
