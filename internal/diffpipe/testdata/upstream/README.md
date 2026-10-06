# Upstream parity goldens for `internal/diffpipe`

The `out.*` files in this directory are **outputs of PR-Agent**
(https://github.com/The-PR-Agent/pr-agent) at commit
`8e5a9295973b24af4b70cafd0b660a230811ef9e` (tag v0.47.0), produced by running
upstream's `get_pr_diff` (`pr_agent/algo/pr_processing.py`) end to end on the
synthetic pull requests next to them. They are MIT data from PR-Agent and are
attributed through the repository's `NOTICE` (X-7, spec P3 §0.3). No upstream
code is part of this repository.

Every golden is **oracle-generated**. None is hand-derived.

## Layout

One directory per case.

| File | Meaning |
|---|---|
| `input.json` | input (written by `make_cases.py`): `budget` (`context_window`, `prompt_tokens`, `factor`, `max_output_tokens`), `mode` (`plain` / `numbered`), `diff` (the `config.Diff` keys), `files` (`path`, `old_path`, `type`, `patch`, `base`, `head`, `head_status`; `null` content = not fetched) and the provider's `skipped` files |
| `out.diff.txt` | upstream's returned diff text, byte for byte |
| `out.json` | `fast_path`, `included` (upstream's admitted files, `files_in_patch`; on the fast path every file with a patch, in ranking order), `omitted` (`added`, `modified` incl. renamed, `deleted`: `deleted_files_list` then the unadmitted `file_dict` entries, in `file_dict` order), `tokens` (estimate of the text), `languages` (the ranking fed to upstream), `upstream_dropped` (files upstream skips silently: no patch, readable), `file_dict_order`, `policy_case`, `clip_overshoot_offset` |

`TestUpstreamGoldens` (`golden_test.go`) builds an `Input` from each
`input.json`, checks the DQ-2 ranking against `languages`, runs `Prepare` and
compares `Text`, `FastPath`, `Tokens`, `Included`, `Omitted` and `Skipped`
(`upstream_dropped` must come back as `empty_diff`), and checks the
accounting invariant.

Two kinds of case are compared against the spec instead of byte for byte,
because v1 deliberately deviates there:

- `policy_case: true` — upstream admitted no file although a non-deleted
  file exists (it then returns only the omitted-file sections). Spec §4.5:
  `clip` must clip the top-ranked non-deleted file (first in
  `file_dict_order`) into `Clipped`, `skip` must return `ErrDoesNotFit`.
- `clip_overshoot_offset` set — upstream's `clip_tokens` returned a
  heuristic cut that estimates above the section budget it was clipped to,
  and the section was appended anyway because the whole-text recount fit.
  `tokens.Clip` verifies and shrinks its cut (spec §2.3, §4.4.6). The text
  must match upstream byte for byte up to that offset; the remaining
  sections must be well-formed clips of the full omitted lists, and the
  text must fit the hard limit. (One committed case,
  `many_omitted_numbered`; none in 800 randomized cases.)

## Cases

| Case | Covers |
|---|---|
| `fast_plain`, `fast_numbered` | the fast path in both modes (extension, render, fit test) |
| `fast_skipped_passthrough` | provider-skipped files pass through untouched |
| `compressed_plain`, `compressed_numbered` | compressed path: language groups, token-desc ranking, admission with the separator, all three sections |
| `compressed_numbered_factor0` | factor 0 |
| `compressed_max_output` | `llm.max_output_tokens` = 3000 (reserves 3000 / 3500) |
| `many_omitted_sections_clipped`, `many_omitted_numbered` | long omitted lists: sections clipped with `...(truncated)`, later sections squeezed out |
| `deleted_list_plain`, `only_deleted_plain` | `handle_patch_deletions` names under `Deleted files:`; a PR of deleted files only |
| `deletion_only_hunks` | `omit_deletion_hunks` on the compressed path |
| `unreadable_compressed`, `unreadable_compressed_plain` | the unreadable-file notice on the compressed path |
| `pure_rename_compressed`, `pure_rename_fast` | renames without hunks (upstream drops them silently; here `empty_diff`) and renamed files listed as modified |
| `single_language`, `other_only` | one group; only the `Other` group |
| `ties_stable` | equal token counts keep the provider order (stable sort) |
| `nothing_fits_clip`, `nothing_fits_skip` | §4.5 policy cases |

## Regenerating

Requirements: the pinned upstream checkout (outside this repository); the
Python 3.11 virtualenv with upstream's pure-Python dependencies and
`tiktoken`; Python **3.12+** to import `pr_processing.py` (PEP 701
f-strings). The scripts live outside the repository; their full source is
reproduced below.

```sh
cd <oracle dir>     # make_cases.py, gen_goldens.py, oracle_common.py, tokcount.py
# 1. inputs (git 2.43.0 produced the committed patches)
python3.12 make_cases.py <repo>/internal/diffpipe/testdata/upstream
# 2. expected outputs
PYTHONPATH=/home/user/pr-agent-upstream:/home/user/pr-agent-venv/lib/python3.11/site-packages \
  python3.12 gen_goldens.py <repo>/internal/diffpipe/testdata/upstream
```

For a wider local differential run, generate randomized cases into a
scratch directory, run the oracle on it and point the test at it:

```sh
python3.12 make_cases.py <dir> 300 7
PYTHONPATH=... python3.12 gen_goldens.py <dir>
REVIEW_MCP_DIFFPIPE_GOLDEN_DIR=<dir> go test -run TestUpstreamGoldens ./internal/diffpipe/
```

(`make_cases.py` also writes the committed named cases into `<dir>`.) Seeds 7
(300 random cases) and 99 (500 random cases) were run this way at the time
the goldens were committed: all of them, plus the named cases written alongside, passed.

### Settings and monkeypatches

Everything on `get_pr_diff`'s path is upstream's own code, including
`sort_files_by_main_languages`, `extend_patch`,
`decouple_and_convert_to_hunks_with_lines_numbers`, `handle_patch_deletions`,
`generate_full_patch`, `_find_verified_fitting_prefix_length`,
`_append_metadata_section`, `clip_tokens`, `AttemptTokenBudget` and
`get_max_tokens`. Only the following is replaced (see `gen_goldens.py`):

1. **Missing modules.** `openai`, `html2text` and `pydantic` are not
   installed; placeholder modules stand in for them (nothing on this path
   uses them).
2. **Token counting.** `tiktoken` is replaced by a stub whose encoder's
   `encode(text)` has length `ceil(raw × (1 + factor))`, where `raw` is the
   real tiktoken `o200k_base` count (computed by `tokcount.py` in the 3.11
   virtualenv, special-token text counted as ordinary text) and `factor` is
   the case's `factor`. So every `count_tokens` call returns review-mcp's
   `tokens.Estimate` (DQ-5; upstream would count without the factor).
3. **Language ranking (DQ-2).** The fake provider's `get_languages()`
   returns review-mcp's ranking: each file classified by upstream's
   `build_language_file_matcher`, weight = UTF-8 bytes of its patch, order =
   weight descending then name, `Other` last; the map values are strictly
   decreasing so upstream's sort reproduces that order.
4. **Settings.** `config.model` = a name outside the model registry;
   `custom_model_max_tokens` = `max_model_tokens` = the case's
   `context_window`; `patch_extra_lines_before/after` and
   `patch_extension_skip_types` from the case; `allow_dynamic_context =
   false` (DQ-1); `verbosity_level = 0`; `enable_ai_metadata = false`.
5. **Budget inputs.** `TokenHandler(model=...).prompt_tokens` = the case's
   `prompt_tokens`. With `max_output_tokens = m`, the `output_token_reserve`
   hook returns `m + (default − 1000)`, so upstream's soft/hard reserves are
   `max(m, 1000) + 500` / `max(m, 1000)` (DQ-4).
6. **Fetch failures.** A `fetch_failed` file is given to upstream with an
   empty patch and `content_fetch_failed = True` (upstream's trigger for the
   unreadable notice; review-mcp keys it on `HeadStatus`, spec §3.4).
7. **Recording wrappers.** `pr_generate_compressed_diff`,
   `_append_metadata_section` and `clip_tokens` are wrapped: the original
   functions run unchanged, and their arguments and results are recorded to
   produce `out.json`.

## `make_cases.py`

```python
"""Build the synthetic fixture PRs for internal/diffpipe/testdata/upstream.

Usage: python3.12 make_cases.py <out_dir> [random_count] [random_seed]

Each case directory gets input.json: the budget, the mode, the diff settings,
the changed files (path, old_path, type, patch, base, head, head_status) and
the provider-skipped files. Patches are produced by `git diff --no-index`
(hunk-only, like the providers). Content is synthetic and generic. Budgets
are chosen from the oracle's own token estimate so that each case lands in
the regime it is named after; the regime actually reached is recorded by
gen_goldens.py in out.json.
"""
import json
import os
import random
import shutil
import subprocess
import sys
import tempfile

from oracle_common import RawCounter

OUT = sys.argv[1]
RANDOM_COUNT = int(sys.argv[2]) if len(sys.argv) > 2 else 0
RANDOM_SEED = int(sys.argv[3]) if len(sys.argv) > 3 else 1
COUNTER = RawCounter()

WORDS = ("alpha beta gamma delta value total count index buffer record item node "
         "handler result state config option request response cache entry").split()
EXTS = [".go", ".py", ".js", ".ts", ".java", ".rb", ".c", ".rs", ".md", ".zzz"]
DIRS = ["src/app", "lib/core", "pkg/util", "docs", "scripts", "internal/store"]


def gitdiff(base, head):
    with tempfile.TemporaryDirectory() as d:
        a, b = os.path.join(d, "a"), os.path.join(d, "b")
        with open(a, "w", newline="") as fh:
            fh.write(base)
        with open(b, "w", newline="") as fh:
            fh.write(head)
        r = subprocess.run(["git", "diff", "--no-index", "--no-color", "-U3", a, b],
                           capture_output=True)
        out = r.stdout.decode()
    i = out.find("\n@@")
    return out[i + 1:] if i >= 0 else ""


def source(rng, n):
    lines = []
    for i in range(n):
        if i % 12 == 0:
            lines.append(f"block_{rng.choice(WORDS)}_{i}:\n")
        else:
            a, b = rng.choice(WORDS), rng.choice(WORDS)
            lines.append(f"    {a}_{i} = {b}({rng.randint(0, 999)})\n")
    return "".join(lines)


def edit(rng, text, edits, deletion_only=False):
    lines = text.splitlines(keepends=True)
    for _ in range(edits):
        if len(lines) < 3:
            break
        i = rng.randrange(len(lines) - 1)
        k = rng.randint(1, 3)
        new = [] if deletion_only else [
            f"    {rng.choice(WORDS)}_new = {rng.choice(WORDS)}({rng.randint(0, 999)})\n"
            for _ in range(rng.randint(0 if rng.random() < 0.3 else 1, 4))]
        lines[i:i + k] = new
    return "".join(lines)


def make_file(rng, path, kind, size, edits=3):
    base = source(rng, size)
    f = {"path": path, "old_path": "", "type": "modified", "patch": "", "base": base,
         "head": None, "head_status": "full"}
    if kind == "added":
        f.update(type="added", base=None, head=base, patch=gitdiff("", base))
    elif kind == "deleted":
        f.update(type="deleted", head=None, patch=gitdiff(base, ""), head_status="not_applicable")
    elif kind in ("modified", "deletion_only", "fetch_failed"):
        head = edit(rng, base, edits, deletion_only=(kind == "deletion_only"))
        if head == base:
            head = base + "    tail_value = finish(1)\n"
        f.update(head=head, patch=gitdiff(base, head))
        if kind == "fetch_failed":
            f.update(head=None, head_status="fetch_failed")
    elif kind == "renamed":
        head = edit(rng, base, edits)
        f.update(type="renamed", old_path="old/" + path, head=head, patch=gitdiff(base, head))
    elif kind == "renamed_pure":
        f.update(type="renamed", old_path="old/" + path, head=base, patch="")
    else:
        raise SystemExit(kind)
    return f


def est(text, factor):
    return COUNTER.estimate(text, factor)


def write(name, files, frac, mode="plain", factor=0.3, prompt=300, max_output=None,
          policy="clip", skipped=(), before=5, after=1):
    total = sum(est(f["patch"], factor) for f in files) + 20 * len(files)
    soft = max(50, int(total * frac))
    hard_reserve = max(max_output or 0, 1000)
    case = {
        "budget": {"context_window": soft + hard_reserve + 500 + prompt, "prompt_tokens": prompt,
                   "factor": factor, "max_output_tokens": max_output},
        "mode": mode,
        "diff": {"extra_lines_before": before, "extra_lines_after": after,
                 "skip_extend_extensions": [".md", ".txt"], "large_patch_policy": policy},
        "files": files,
        "skipped": [{"path": p, "reason": r} for p, r in skipped],
    }
    d = os.path.join(OUT, name)
    os.makedirs(d, exist_ok=True)
    with open(os.path.join(d, "input.json"), "w", newline="\n") as fh:
        json.dump(case, fh, indent=1, ensure_ascii=False)
        fh.write("\n")


def path(rng, ext, i, long=False):
    d = rng.choice(DIRS)
    if long:
        d += "/" + "/".join(rng.choice(WORDS) for _ in range(4))
    return f"{d}/{rng.choice(WORDS)}_{i}{ext}"


def mixed(rng, n, kinds=None, sizes=(20, 120), exts=EXTS, long=False):
    kinds = kinds or ["modified"] * 5 + ["added", "added", "deleted", "renamed", "deletion_only"]
    return [make_file(rng, path(rng, rng.choice(exts), i, long), rng.choice(kinds), rng.randint(*sizes))
            for i in range(n)]


for d in os.listdir(OUT) if os.path.isdir(OUT) else []:
    if os.path.isdir(os.path.join(OUT, d)):
        shutil.rmtree(os.path.join(OUT, d))

R = random.Random
write("fast_plain", mixed(R(1), 6), 3.0)
write("fast_numbered", mixed(R(2), 6), 3.0, mode="numbered")
write("fast_skipped_passthrough", mixed(R(3), 4), 3.0,
      skipped=[("assets/logo.png", "binary"), ("vendor/lib/x.go", "filtered")])
write("compressed_plain", mixed(R(4), 14), 0.45)
write("compressed_numbered", mixed(R(5), 14), 0.45, mode="numbered")
write("compressed_numbered_factor0", mixed(R(6), 12), 0.4, mode="numbered", factor=0.0)
write("compressed_max_output", mixed(R(7), 12), 0.5, max_output=3000)
write("many_omitted_sections_clipped",
      mixed(R(8), 50, sizes=(10, 25), long=True), 0.12)
write("many_omitted_numbered",
      mixed(R(9), 50, sizes=(10, 25), long=True), 0.12, mode="numbered")
write("deleted_list_plain", mixed(R(10), 10, kinds=["deleted"] * 3 + ["modified"]), 0.35)
write("only_deleted_plain", mixed(R(11), 6, kinds=["deleted"]), 0.3)
write("deletion_only_hunks", mixed(R(12), 8, kinds=["deletion_only", "modified"]), 0.6)
write("unreadable_compressed", mixed(R(13), 8, kinds=["fetch_failed", "modified", "added"]), 0.5,
      mode="numbered")
write("unreadable_compressed_plain", mixed(R(14), 8, kinds=["fetch_failed", "modified"]), 0.25)
write("pure_rename_compressed", mixed(R(15), 10, kinds=["renamed_pure", "modified", "renamed"]), 0.5)
write("pure_rename_fast", mixed(R(16), 5, kinds=["renamed_pure", "modified"]), 3.0)
write("single_language", mixed(R(17), 9, exts=[".go"]), 0.5, mode="numbered")
write("other_only", mixed(R(18), 7, exts=[".zzz"]), 0.5)
# Equal-size files: identical bodies in same-length paths (stable ties).
rng = R(19)
twin = make_file(rng, "src/app/twin_0.py", "modified", 40)
ties = [dict(twin, path=f"src/app/twin_{i}.py") for i in range(6)] + mixed(R(20), 4, exts=[".py"])
write("ties_stable", ties, 0.5)
# Nothing admitted: one file larger than the soft limit (spec §4.5 deviation).
big = [make_file(R(21), "src/app/huge_0.go", "modified", 400, edits=40)]
write("nothing_fits_clip", big + mixed(R(22), 2, kinds=["deleted"], sizes=(10, 20)), 0.2, policy="clip")
write("nothing_fits_skip", big + mixed(R(22), 2, kinds=["deleted"], sizes=(10, 20)), 0.2, policy="skip")

rng = random.Random(RANDOM_SEED)
for i in range(RANDOM_COUNT):
    n = rng.randint(1, 30)
    kinds = rng.choice([None, ["modified", "added"], ["deleted", "modified", "renamed_pure"],
                        ["fetch_failed", "modified", "deletion_only", "renamed"]])
    files = mixed(random.Random(rng.random()), n, kinds=kinds, sizes=(5, rng.choice([30, 80, 200])),
                  long=rng.random() < 0.3)
    write(f"random_{i:03d}", files, rng.choice([0.05, 0.2, 0.4, 0.7, 0.95, 3.0]),
          mode=rng.choice(["plain", "numbered"]), factor=rng.choice([0.0, 0.1, 0.3, 1.0]),
          prompt=rng.randint(0, 800), max_output=rng.choice([None, None, 1500, 4000]),
          policy=rng.choice(["clip", "skip"]), before=rng.randint(0, 10), after=rng.randint(0, 10))
```

## `gen_goldens.py`

```python
"""Parity oracle for internal/diffpipe: run upstream get_pr_diff on each
fixture PR and write the expected outputs next to its input.json.

Usage:
  PYTHONPATH=<upstream>:<venv>/lib/python3.11/site-packages \
      python3.12 gen_goldens.py <cases_dir>

Upstream's real get_pr_diff (pr_agent/algo/pr_processing.py) runs end to end:
sort_files_by_main_languages, pr_generate_extended_diff (extend_patch,
decouple_and_convert_to_hunks_with_lines_numbers), the fast-path fit test,
pr_generate_compressed_diff (handle_patch_deletions), generate_full_patch,
_find_verified_fitting_prefix_length, the omitted-file lists,
_append_metadata_section and clip_tokens, with AttemptTokenBudget and
get_max_tokens computing the budgets. Only these things are replaced:

  1. Module stubs: `openai`, `html2text` and `pydantic` are not installed in
     the oracle environment; they are replaced by empty placeholder modules
     (none of their code is on get_pr_diff's path).
  2. Token counting: the `tiktoken` module is replaced by a stub whose
     encoder's encode(text) has length ceil(raw * (1 + factor)), where raw is
     the real tiktoken o200k_base count (tokcount.py in the 3.11 virtualenv)
     and factor is the case's llm.token_estimate_factor. Upstream's
     TokenHandler.count_tokens therefore returns review-mcp's
     tokens.Estimate (DQ-5) for every count it makes, prompt-free.
  3. The provider: a fake GitProvider whose get_diff_files() returns the
     case's files as FilePatchInfo and whose get_languages() returns a
     language map equal to review-mcp's DQ-2 ranking (see languages()).
  4. Settings: config.model = a name outside the registry,
     custom_model_max_tokens = max_model_tokens = the case's context window
     (so get_max_tokens returns it), patch_extra_lines_before/after and
     patch_extension_skip_types from the case, allow_dynamic_context = False
     (DQ-1), verbosity_level = 0.
  5. Prompt tokens: TokenHandler(model=...).prompt_tokens = the case's
     prompt_tokens. The output reserve: with llm.max_output_tokens = m, the
     output_token_reserve hook returns m + (default - 1000), so upstream's
     soft/hard reserves are max(m, 1000) + 500 and max(m, 1000) (DQ-4).

pr_generate_compressed_diff, _append_metadata_section and clip_tokens are
wrapped (the same functions run; arguments and results are only recorded) to
read the admitted files, deleted_files_list and the section appends; nothing
else is altered.

Known deviation recorded per case ("clip_overshoot"): upstream's clip_tokens
returns its first heuristic cut unverified, so a clipped section can
estimate above the section budget it was clipped to and still be appended
when the whole-text recount fits (counts are not additive). review-mcp clips
with tokens.Clip, which verifies and shrinks the cut (spec §2.3, §4.4.6).
For such a case, out.json records the byte offset of upstream's text before
that section; the Go test compares the text byte for byte up to there and
checks the rest against the spec rules.
"""
import importlib.abc
import importlib.machinery
import json
import os
import sys
import types

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from oracle_common import RawCounter  # noqa: E402

CASES = sys.argv[1]
COUNTER = RawCounter()
STATE = {"factor": 0.3}


class _Placeholder(types.ModuleType):
    def __getattr__(self, name):
        if name.startswith("__"):
            raise AttributeError(name)
        value = type(name, (Exception,), {})
        setattr(self, name, value)
        return value


class _StubFinder(importlib.abc.MetaPathFinder, importlib.abc.Loader):
    NAMES = {"openai", "html2text", "pydantic"}

    def find_spec(self, name, path, target=None):
        if name.split(".")[0] in self.NAMES:
            return importlib.machinery.ModuleSpec(name, self, is_package=True)
        return None

    def create_module(self, spec):
        return _Placeholder(spec.name)

    def exec_module(self, module):
        module.__path__ = []


sys.meta_path.insert(0, _StubFinder())


class _Len:
    def __init__(self, n):
        self.n = n

    def __len__(self):
        return self.n


class _Encoder:
    def encode(self, text, disallowed_special=()):
        return _Len(COUNTER.estimate(text, STATE["factor"]))


_tiktoken = types.ModuleType("tiktoken")
_tiktoken.get_encoding = lambda name: _Encoder()
_tiktoken.encoding_for_model = lambda model: _Encoder()
sys.modules["tiktoken"] = _tiktoken

from loguru import logger  # noqa: E402

logger.remove()  # upstream's log output is not part of the goldens
from pr_agent.config_loader import get_settings  # noqa: E402
import pr_agent.algo.pr_processing as pp  # noqa: E402
from pr_agent.algo.language_handler import (build_language_file_matcher,  # noqa: E402
                                            sort_files_by_main_languages)
from pr_agent.algo.token_handler import TokenHandler  # noqa: E402
from pr_agent.algo.types import EDIT_TYPE, FilePatchInfo  # noqa: E402

MODEL = "oracle-synthetic-model"
settings = get_settings(use_context=False)
settings.set("CONFIG.VERBOSITY_LEVEL", 0)
settings.config.allow_dynamic_context = False
settings.config.model = MODEL
settings.config.enable_ai_metadata = False
EDIT = {"added": EDIT_TYPE.ADDED, "modified": EDIT_TYPE.MODIFIED,
        "deleted": EDIT_TYPE.DELETED, "renamed": EDIT_TYPE.RENAMED}
GET_LANGUAGE = build_language_file_matcher(settings.language_extension_map_org)

_orig_compressed = pp.pr_generate_compressed_diff
CAPTURE = {}


def _compressed(*args, **kwargs):
    result = _orig_compressed(*args, **kwargs)
    CAPTURE["compressed"] = result
    return result


pp.pr_generate_compressed_diff = _compressed

_orig_append = pp._append_metadata_section
_orig_clip = pp.clip_tokens


def _clip(text, max_tokens, **kwargs):
    result = _orig_clip(text, max_tokens, **kwargs)
    CAPTURE["last_clip_overshoot"] = bool(result) and COUNTER.estimate(result, STATE["factor"]) > max_tokens
    return result


def _append(final_diff, curr_token, section, max_tokens, token_handler):
    CAPTURE["last_clip_overshoot"] = False
    result = _orig_append(final_diff, curr_token, section, max_tokens, token_handler)
    if CAPTURE["last_clip_overshoot"] and "clip_overshoot_offset" not in CAPTURE:
        CAPTURE["clip_overshoot_offset"] = len(final_diff.encode("utf-8"))
    return result


pp._append_metadata_section = _append
pp.clip_tokens = _clip


def languages(files):
    """review-mcp's DQ-2 ranking, expressed as an upstream language map.

    Weight = sum of the UTF-8 byte lengths of the files' patches (review-mcp
    computes it from the provider patch, also for a fetch-failed file);
    order = weight descending, then name; "Other" (no match) is upstream's
    catch-all bucket and is not in the map. The map's values are strictly
    decreasing so upstream's sort reproduces exactly this order.
    """
    weights = {}
    for f in files:
        lang = GET_LANGUAGE(f["path"]) or "Other"
        weights[lang] = weights.get(lang, 0) + len(f["patch"].encode("utf-8"))
    order = sorted((k for k in weights if k != "Other"), key=lambda k: (-weights[k], k))
    return {k: len(order) - i for i, k in enumerate(order)}, order + (["Other"] if "Other" in weights else [])


class FakeProvider:
    def __init__(self, files, langs):
        self.files, self.langs = files, langs

    def get_diff_files(self):
        return self.files

    def get_languages(self):
        return self.langs


def to_info(f):
    fetch_failed = f["head_status"] == "fetch_failed"
    return FilePatchInfo(
        base_file=f["base"] or "", head_file=f["head"] or "",
        # Upstream renders the unreadable notice for a fetch-failed file with
        # an empty patch; review-mcp keys it on HeadStatus (spec §3.4).
        patch="" if fetch_failed else f["patch"],
        filename=f["path"], edit_type=EDIT[f["type"]],
        old_filename=f["old_path"] or None, content_fetch_failed=fetch_failed)


def run(case):
    b = case["budget"]
    STATE["factor"] = b["factor"]
    d = case["diff"]
    settings.config.custom_model_max_tokens = b["context_window"]
    settings.config.max_model_tokens = b["context_window"]
    settings.config.patch_extra_lines_before = d["extra_lines_before"]
    settings.config.patch_extra_lines_after = d["extra_lines_after"]
    settings.config.patch_extension_skip_types = d["skip_extend_extensions"]
    m = b["max_output_tokens"]
    reserve = None if m is None else (lambda model, default: m + (default - 1000))

    langs, order = languages(case["files"])
    infos = [to_info(f) for f in case["files"]]
    handler = TokenHandler(model=MODEL)
    handler.prompt_tokens = b["prompt_tokens"]
    CAPTURE.clear()
    diff = pp.get_pr_diff(FakeProvider(infos, langs), handler, MODEL,
                          add_line_numbers_to_hunks=case["mode"] == "numbered",
                          output_token_reserve=reserve)

    out = {"languages": order, "tokens": COUNTER.estimate(diff, b["factor"]),
           "clip_overshoot_offset": CAPTURE.get("clip_overshoot_offset")}
    groups = sort_files_by_main_languages(langs, [to_info(f) for f in case["files"]])
    upstream_dropped = [f.filename for g in groups for f in g["files"]
                        if not f.patch and not f.content_fetch_failed]
    out["upstream_dropped"] = upstream_dropped
    if "compressed" not in CAPTURE:
        out["fast_path"] = True
        out["included"] = [f.filename for g in groups for f in g["files"]
                           if f.patch or f.content_fetch_failed]
        out["omitted"] = {"added": [], "modified": [], "deleted": []}
        out["policy_case"] = False
        return diff, out

    patches_list, _, deleted_files, _, file_dict, files_in = CAPTURE["compressed"]
    admitted = files_in[0]
    om = {"added": [], "modified": [], "deleted": list(deleted_files)}
    for name, v in file_dict.items():
        if name in admitted:
            continue
        if v["edit_type"] == EDIT_TYPE.ADDED:
            om["added"].append(name)
        elif v["edit_type"] == EDIT_TYPE.DELETED:
            om["deleted"].append(name)
        else:
            om["modified"].append(name)
    out["fast_path"] = False
    out["included"] = list(admitted)
    out["omitted"] = om
    out["file_dict_order"] = list(file_dict)
    # spec §4.5: nothing admitted while a non-deleted file exists. Upstream
    # returns only the omitted-file sections here; review-mcp applies
    # large_patch_policy instead (deliberate deviation), so the Go test checks
    # the policy outcome rather than the text for these cases.
    out["policy_case"] = not admitted and any(
        v["edit_type"] != EDIT_TYPE.DELETED for v in file_dict.values())
    return diff, out


for name in sorted(os.listdir(CASES)):
    d = os.path.join(CASES, name)
    if not os.path.isdir(d):
        continue
    with open(os.path.join(d, "input.json")) as fh:
        case = json.load(fh)
    diff, out = run(case)
    with open(os.path.join(d, "out.diff.txt"), "w", newline="") as fh:
        fh.write(diff)
    with open(os.path.join(d, "out.json"), "w", newline="\n") as fh:
        json.dump(out, fh, indent=1, ensure_ascii=False)
        fh.write("\n")
    print(f"{name}: fast={out['fast_path']} included={len(out['included'])} "
          f"omitted={sum(len(v) for v in out['omitted'].values())} policy={out['policy_case']} "
          f"overshoot={out['clip_overshoot_offset'] is not None}",
          file=sys.stderr)
```

## `oracle_common.py`

```python
"""Shared helpers of the 3d oracle: the deterministic token estimate.

estimate(text, factor) = ceil(raw_o200k(text) * (1 + factor)), which is
review-mcp's tokens.Estimate (DQ-5). raw_o200k comes from real tiktoken
(tokcount.py, run in the 3.11 virtualenv), not from review-mcp's Go code.
"""
import json
import math
import os
import subprocess

VENV_PY = os.environ.get("ORACLE_VENV_PYTHON", "/home/user/pr-agent-venv/bin/python")
HERE = os.path.dirname(os.path.abspath(__file__))


class RawCounter:
    def __init__(self):
        self.proc = subprocess.Popen([VENV_PY, "-I", os.path.join(HERE, "tokcount.py")],
                                     stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True,
                                     encoding="utf-8")
        self.cache = {}

    def raw(self, text):
        if text == "":
            return 0
        n = self.cache.get(text)
        if n is None:
            self.proc.stdin.write(json.dumps(text) + "\n")
            self.proc.stdin.flush()
            n = int(self.proc.stdout.readline())
            self.cache[text] = n
        return n

    def estimate(self, text, factor):
        return math.ceil(self.raw(text) * (1 + factor))
```

## `tokcount.py`

```python
"""Raw o200k_base token counter for the 3d oracle (runs under the Python 3.11
virtualenv, where tiktoken's compiled extension loads).

Protocol: one JSON string per stdin line; one decimal count per stdout line.
Special-token spellings are counted as ordinary text (disallowed_special=()),
as upstream's TokenHandler.count_tokens does.
"""
import json
import sys

import tiktoken

enc = tiktoken.get_encoding("o200k_base")
for line in sys.stdin:
    text = json.loads(line)
    sys.stdout.write(f"{len(enc.encode(text, disallowed_special=()))}\n")
    sys.stdout.flush()
```
