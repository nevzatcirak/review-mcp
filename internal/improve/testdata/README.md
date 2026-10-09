# Goldens for `internal/improve`

All files below are written by `go test ./internal/improve -run <Test> -update`
after the change has been checked by hand. There is no upstream oracle for
pr_improve: PR-Agent was not run for this tool, so these goldens pin
review-mcp's own renderings only. The adaptations from upstream are listed in
the header comments of `../prompts/*.tmpl`.

## `prompts/<case>/` — `TestPromptGoldens`

`case.json` is written by hand: `kind` (`suggest` for the suggestion call of
one call or a part, `reflect` for a self-review call), `language` (output
language), `title`, `date`, `branch`, `target_branch`, `description`,
`discussion` and `repo_context` (already rendered blocks), `max_suggestions`,
`part_header`, `diff` (a numbered diff) and `suggestions` (the self-review's
suggestions, with the Go field names of `Candidate`). `system.txt` and
`user.txt` are the rendered prompts.

| Case | Kind | Other |
|---|---|---|
| `base` | suggest | en-US, every PR field set, a discussion block |
| `minimal` | suggest | no date, description, target branch or discussion: those blocks are omitted; one suggestion asked for |
| `non_english` | suggest | tr-TR: the extra-instructions block carries only the language instruction |
| `part` | suggest | the part line of part 2 of 3 and a repository-context block |
| `reflect` | reflect | two suggestions, one with characters JSON escapes |
| `reflect_non_english` | reflect | de-DE |

## `runs/<run>/` — `TestOneCallRun`, `TestThreePartRun`

The prompts of every model call of a run with the fake LLM of
`pipeline_test.go`, and the structured result (`result.json`):

- `one_call/`: two changed files and a filtered one, a discussion with a
  human thread and a comment of ours; the suggestion call (`system.txt`,
  `user.txt`) and its self-review (`reflect.system.txt`, `reflect.user.txt`).
- `three_parts/`: six files with `diff.max_tokens` 2000; per part the
  suggestion call (`partN.*`) and its self-review (`partN.reflect.*`).

`../render/testdata/*.md` are the client renderings of these results
(`TestClientGoldens` in `../render`), so a change to a run golden shows up in
the rendering goldens too.
