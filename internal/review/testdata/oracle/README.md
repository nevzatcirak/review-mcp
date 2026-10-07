# Parity oracle for the review prompts

Two scripts produce the upstream side of the prompt goldens in
`../prompts` (see `../README.md` for the layout and the expected
differences). The oracle is not run by the tests; its outputs are
committed.

- `make_cases.py` writes every case's `case.json` (synthetic PR sample,
  toggles, language, extra instructions; for `with_discussion` also our
  discussion block, read from `../discussion/sample.txt`; the upstream
  renderer ignores it, and that case's `upstream.*` files are a copy of
  `all_fields`', because upstream has no such block).
- `render_upstream.py` renders upstream's `[pr_review_prompt]` system and
  user templates for each case and writes `upstream.system.txt` and
  `upstream.user.txt`. It uses upstream's own settings loader and
  `render_diff_hunk_format`, and renders with
  `jinja2.Environment(undefined=StrictUndefined)` as upstream's
  `TokenHandler` does. The variable mapping is documented at the top of the
  script. The output-language sentence is applied the way
  `PRAgent._handle_request` does it; the script asserts that the sentence
  and the separator still appear in upstream's `pr_agent/agent/pr_agent.py`.

## Regenerating

Requirements: the pinned upstream checkout (commit
`8e5a9295973b24af4b70cafd0b660a230811ef9e`, outside this repository) and
the parity virtualenv (Python 3.11 site-packages with `jinja2`, `dynaconf`
and `loguru`). `render_upstream.py` runs under Python 3.12 with the 3.11
site-packages, as the earlier oracles do.

```sh
# from the repository root
# 1. cases
/home/user/pr-agent-venv/bin/python -I \
  internal/review/testdata/oracle/make_cases.py internal/review/testdata/prompts
# 2. upstream renderings
PYTHONPATH=/home/user/pr-agent-upstream:/home/user/pr-agent-venv/lib/python3.11/site-packages \
  python3.12 internal/review/testdata/oracle/render_upstream.py internal/review/testdata/prompts
# 3. our renderings and the diffs against upstream
go test ./internal/review -run TestPromptGoldens -update
```

Upstream's settings loader logs warnings about missing `.secrets.toml`
files on stderr; they are harmless.
