# Parity oracle for `internal/yamlrepair`

Two scripts produce the fixtures in `../cases` (see `../README.md` for the
layout and how the test uses them). The oracle is not run by the tests;
its outputs are committed.

- `make_cases.py` writes every case's `input.txt` and `case.json`. Inputs
  from upstream's unit tests are restated there with their source test.
- `gen_goldens.py` runs upstream's `load_yaml` on each case and writes
  `golden.json`: the JSON-normalized result, its Python type, and the
  winning tactic. The tactic is found by tracing which `return` of
  `try_fix_yaml` ran and mapping its line to the nearest preceding fallback
  comment in upstream's source; the script asserts that every expected
  comment exists once and in order, so a moved comment fails loudly.

`deviation.json` files are hand-derived and are not touched by either
script.

## Regenerating

Requirements: the pinned upstream checkout (commit
`8e5a9295973b24af4b70cafd0b660a230811ef9e`, outside this repository) and
the parity virtualenv (Python 3.11 site-packages with PyYAML 6.0.1,
upstream's pin, and `loguru`, `dynaconf`). `gen_goldens.py` runs under
Python 3.12 with the 3.11 site-packages, as the earlier oracles do.

```sh
# from the repository root
# 1. inputs (PyYAML is needed for the mojibake inputs)
/home/user/pr-agent-venv/bin/python -I \
  internal/yamlrepair/testdata/oracle/make_cases.py internal/yamlrepair/testdata/cases
# 2. goldens
PYTHONPATH=/home/user/pr-agent-upstream:/home/user/pr-agent-venv/lib/python3.11/site-packages \
  python3.12 internal/yamlrepair/testdata/oracle/gen_goldens.py internal/yamlrepair/testdata/cases
```

Import stubs: `pr_agent/algo/utils.py` imports `html2text` and `pydantic`
at module level, and the parity virtualenv has neither. `gen_goldens.py`
installs empty stand-ins for them only when they are missing. `load_yaml`,
`try_fix_yaml` and their helpers use neither (they use `re`, `copy`,
`yaml` and the logger), so the stubs cannot change a result. Upstream's
logger is silenced (`logger.remove()`), because upstream logs answer text.
