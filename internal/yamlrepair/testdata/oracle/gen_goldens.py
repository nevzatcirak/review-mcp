"""Write golden.json for every case under internal/yamlrepair/testdata/cases.

Usage (from the repository root):
  PYTHONPATH=<upstream checkout>:<venv site-packages> \
    python3.12 -I internal/yamlrepair/testdata/oracle/gen_goldens.py <cases dir>

(-I ignores PYTHONPATH, so leave it out when PYTHONPATH carries the upstream
checkout; see README.md for the exact command.)

For each case it calls upstream's load_yaml(input, keys_fix_yaml=[name + ":"
for each name], first_key, last_key) and records:
  upstream_type    the Python type name of the result (dict, list, str, ...);
  upstream         the result, JSON-normalized: json.dumps with default=str
                   (dates become "YYYY-MM-DD"), so non-string keys become
                   strings the way JSON writes them;
  upstream_tactic  "direct" when try_fix_yaml was not called; otherwise
                   upstream's tactic number (1-12, porting map §D) whose
                   `return data` ended try_fix_yaml, or "none" when it
                   returned None.
The tactic is found by tracing which return statement of try_fix_yaml ran
and mapping its line to the nearest preceding fallback comment in upstream's
source. Nothing is copied from upstream: the module is imported and called.
"""
import importlib.util
import inspect
import json
import os
import sys
import types

from loguru import logger


def stub_missing(name, **attrs):
    """Satisfy an import of utils.py that the parity venv lacks. The functions
    under test (load_yaml, try_fix_yaml and their helpers) never use it."""
    if importlib.util.find_spec(name) is not None:
        return
    module = types.ModuleType(name)
    module.__dict__.update(attrs)
    sys.modules[name] = module


class _StubModel:
    def __init_subclass__(cls, **kwargs):
        pass


stub_missing("html2text")
stub_missing("pydantic", BaseModel=_StubModel, Field=lambda *a, **k: None, ValidationError=Exception)

from pr_agent.algo import utils  # noqa: E402

# Upstream's fallback comments in try_fix_yaml, in source order, and the
# porting-map tactic number of the code that follows each one.
MARKERS = [
    ("# first fallback", "1"),
    ("# 1.5 fallback", "2"),
    ("# try to add spaces to lines that are not indented properly", "3"),
    ("# second fallback", "4"),
    ("# third fallback", "5"),
    ("# forth fallback", "6"),
    ("# fifth fallback", "7"),
    ("# 5.5 fallback", "8"),
    ("# sixth fallback - replace tabs", "9"),
    ("# seventh fallback", "10"),
    ("# eighth fallback", "11"),
    ("# ninth fallback", "12"),
]


def marker_lines():
    lines, first = inspect.getsourcelines(utils.try_fix_yaml)
    found = []
    for marker, number in MARKERS:
        hits = [first + i for i, line in enumerate(lines) if line.strip().startswith(marker)]
        assert len(hits) == 1, (marker, hits)
        found.append((hits[0], number))
    assert [n for _, n in sorted(found)] == [n for _, n in MARKERS], "marker order changed"
    return sorted(found)


MARKER_LINES = marker_lines()
CODE = utils.try_fix_yaml.__code__


def tactic_for_line(lineno):
    number = None
    for line, n in MARKER_LINES:
        if line <= lineno:
            number = n
    assert number is not None, lineno
    return number


def run(text, names, first, last):
    state = {"called": False, "tactic": None}

    def local(frame, event, arg):
        if event == "return":
            state["tactic"] = "none" if arg is None else tactic_for_line(frame.f_lineno)
        return local

    def tracer(frame, event, arg):
        if frame.f_code is CODE and event == "call":
            state["called"] = True
            return local
        return None

    sys.settrace(tracer)
    try:
        result = utils.load_yaml(text, keys_fix_yaml=[n + ":" for n in names], first_key=first, last_key=last)
    finally:
        sys.settrace(None)
    tactic = state["tactic"] if state["called"] else "direct"
    return result, tactic


def main():
    logger.remove()  # upstream logs answer text; keep the oracle quiet
    root = sys.argv[1]
    for name in sorted(os.listdir(root)):
        d = os.path.join(root, name)
        with open(os.path.join(d, "case.json"), encoding="utf-8") as fh:
            meta = json.load(fh)
        with open(os.path.join(d, "input.txt"), encoding="utf-8", newline="") as fh:
            text = fh.read()
        result, tactic = run(text, meta["names"], meta["first"], meta["last"])
        golden = {
            "upstream_type": type(result).__name__,
            "upstream": json.loads(json.dumps(result, default=str)),
            "upstream_tactic": tactic,
        }
        with open(os.path.join(d, "golden.json"), "w", encoding="utf-8") as fh:
            json.dump(golden, fh, indent=2, sort_keys=True, ensure_ascii=True)
            fh.write("\n")
        print(f"{name}: {tactic} {golden['upstream_type']}")


main()
