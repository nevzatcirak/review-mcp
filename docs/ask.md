# Asking questions about a pull request with `pr_ask`

`pr_ask` answers one free-text question about a pull request with your LLM,
grounded in the PR's title, description and diff. It is stateless: every call
stands alone, there is no conversation history. With `publish=true` it also
posts the question and the answer as one PR-level comment.

## Calling the tool

| Argument | Required | Meaning |
|---|---|---|
| `pr_url` | yes | The pull request URL, on a configured Gitea or Bitbucket Server host. |
| `question` | yes | The question, at most 8000 characters (counted in characters, not bytes). Surrounding whitespace is ignored. |
| `extra_instructions` | no | Extra guidance for the model for this call; replaces `ask.extra_instructions` (`REVIEW_MCP_ASK_EXTRA_INSTRUCTIONS`). An empty value means "not given". |
| `output_language` | no | Locale code for the answer, such as `en-US` or `tr-TR`; replaces `output.language`. Same format as the config key. |
| `publish` | no | `true` also posts the question and answer as a PR comment. Default `false`. |

An empty question, a question over 8000 characters and an invalid
`output_language` are rejected with a fixed message before anything is sent
to the provider or the LLM. A too-long question is never truncated. The
messages are "question must not be empty", "question is too long: at most
8000 characters are allowed" and "output_language must be a locale code such
as en-US or tr-TR"; none of them repeats your input.

The result has two parts: the question and the answer as portable markdown
(the text content; no raw HTML) and the same data as structured content
(`question`, `answer`, `coverage`, `notes`, `metadata` and, when requested,
`publish`). While it runs, a client that sent a progress token receives the
stages `fetching`, `preparing diff`, `calling model` and `rendering`.

## What is sent to the LLM

One chat completion request per question goes to `llm.base_url`. It contains:

- the instructions for answering questions about a PR;
- the PR's **title**, source **branch** and **description** (the description
  is cut to `diff.max_description_tokens`, 500 by default);
- the **main language** of the PR, when it has one;
- the **diff**, filtered by `ignore.glob`, `ignore.regex` and the built-in
  rules for generated files, lockfiles and binaries, in its plain form
  (`+`, `-` and space prefixes, no line numbers);
- your **question**, `extra_instructions`, if any, and the output-language
  instruction.

Never sent: provider tokens, the LLM API key (it is only the `Authorization`
header of the request itself), configuration files, comments, commit
messages, or anything outside the PR.

The question, the prompts, the model's answer and the diff are never written
to the logs, at any level. A failed call reports one of a fixed set of
sentences, never a response body (see [Troubleshooting](troubleshooting.md)).
As with `pr_review`, the PR text and code leave your machine for the LLM
endpoint: point review-mcp only at endpoints you trust with that code.

The context-window budget works as for reviews; see
[Reviewing pull requests](review.md#choosing-llmcontext_window). The question
counts as part of the prompt scaffolding, so a very long question leaves less
room for the diff. If nothing of the diff fits, the call fails with "the pull
request diff does not fit the configured context window" and nothing is sent
to the model.

## Grounding and honesty

The prompts are adapted from PR-Agent's `/ask` prompts with two deliberate
changes that make the answers honest:

- The model is told to answer **only from the PR information and diff
  provided**. If the answer cannot be determined from them, it must say so
  explicitly, state what information is missing, and not guess. (PR-Agent's
  prompt tells the model it must answer as best it can.)
- The model is told that the diff may list files that were left out because
  of its size, and that it must not draw conclusions about their content.

So a question such as "what is the deployment schedule for this change?"
should get an answer that says the PR does not contain that information. This
is an instruction to the model, not a guarantee: models can still be wrong.
Read answers about code the way you would read a colleague's guess, and check
the file and line it names.

## Coverage: what the model did not see

Every answer ends with the same **coverage** section as a review (see
[Reading the result](review.md#coverage)): which changed files were included,
clipped, omitted to fit the context window, skipped, or filtered, and why.

Read it together with the answer. If your question is about a file listed as
omitted, skipped or filtered, the model did not see that file's diff, and a
good answer says so; an answer that sounds confident about such a file is not
grounded in the diff. Raise `llm.context_window`, narrow the PR, or adjust
`ignore.*`, then ask again. If nothing reviewable remains after filtering, the
model is not called, the answer is empty, and the notes say "No reviewable
changes after filtering."

The **notes** section appears when something deserves attention: the answer
was cut off by the output limit ("The answer was cut off by the model's output
limit."; raise `llm.max_output_tokens`), files were clipped, or the diff was
shortened by the request-size guard.

## Publishing

`publish=true` (or `diag ask --publish`) posts one PR-level comment after the
answer was produced. Nothing is ever posted without it. The comment shows the
question in a fenced block, then the answer, then a short coverage section
(and the notes, when there are any). Gitea gets PR-Agent's headings, `Ask`
with a question-mark emoji and `Answer:`; Bitbucket Server gets plain
`Question` and `Answer` headings. The token needs write access to pull
requests or comments (see the [token scopes](troubleshooting.md#token-scopes)).

**Quick-action sanitization.** Some providers run a line that starts with `/`
in a comment as a command (for example `/close`). Because the model's answer
and your question are free text, the published comment puts a space in front
of every line that would start with `/`, including the first line and lines
after a carriage return, as PR-Agent does. The comment therefore shows
` /close` instead of triggering anything. The text returned to your client is
not changed.

A failure to post never discards the answer: the result carries the answer and
a `publish` object with `published: false` and the reason. Publishing is not
idempotent; calling the tool twice posts two comments.

## Checking the budget with `diag ask --dry-run`

```sh
review-mcp diag ask https://your-gitea.example/octo/demo/pulls/7 \
  --question "Which files change the request validation?" --dry-run
```

runs everything up to the model call and prints a JSON report; the model is
not called and nothing is posted. It needs the same configuration as a real
call (the LLM API key must be set, but it is not used).

```json
{
  "dry_run": true,
  "empty": false,
  "budget": { "context_window": 32000, "soft_limit": 28300, "hard_limit": 28800, "prompt_tokens": 2200, "factor": 0.3 },
  "tokens": { "prompt": 2200, "diff": 5120, "request": 7380, "context_window": 32000 },
  "fast_path": true,
  "coverage": { "included": ["src/app.go"], "clipped": [], "omitted": { "added": [], "modified": [], "deleted": [] }, "skipped": [], "filtered": [] },
  "notes": [],
  "elapsed_ms": 120
}
```

`tokens.prompt` includes your question. The report does not contain the
question, the prompts or any PR text. `--show-prompt` additionally prints the
rendered system and user prompts to stdout, after the JSON, under the lines
`--- system prompt ---` and `--- user prompt ---`: this is exactly what the
model would receive, including the PR description, the diff and your question,
so handle it like the PR itself. The prompts are never logged.

Without `--dry-run`, `diag ask` runs the full call and prints the same
markdown the tool returns (`--show-prompt` appends the prompts); with
`--publish` it also posts the comment. `--question` is required. Usage errors
exit 2 and send nothing: a missing, empty or over-long question, no PR URL,
more than one, an unknown flag, or `--dry-run` together with `--publish`.
Other exit codes are as for `diag review`: 0, and 1 on a failure (a failed
publish also exits 1, after printing the answer).
