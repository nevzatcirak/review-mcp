# Upstream parity goldens for `internal/patch`

The `out.*` files in this directory are **outputs of PR-Agent**
(https://github.com/The-PR-Agent/pr-agent) at commit
`8e5a9295973b24af4b70cafd0b660a230811ef9e` (tag v0.47.0), produced by running
the upstream functions on the synthetic inputs next to them. They are MIT
data from PR-Agent and are attributed through the repository's `NOTICE`
(X-7, spec P3 §0.3). No upstream code is part of this repository.

Every golden in this directory is **oracle-generated**. None is
hand-derived. (The only test expectation not produced by the oracle is the
unnumbered decoupled view, which upstream does not have at this commit;
`TestRenderDecoupledUnnumbered` derives it from the oracle's numbered golden
by removing the line-number prefixes.) Cases where review-mcp deliberately
differs from upstream live in `../deviations/` instead (see
[Deviations](#deviations)).

## Layout

One directory per case. Inputs:

| File | Meaning |
|---|---|
| `case.json` | `path`, `old_path`, `type` (provider change type), `head_status`, `before`/`after` (extra context lines), `skip_extend_extensions` |
| `patch.diff` | hunk-only patch, as the providers produce it |
| `base.txt`, `head.txt` | file contents; a missing file means "not fetched" (`nil`) |

Expected outputs (written by the oracle):

| File | Upstream source |
|---|---|
| `out.extended.patch` | `extend_patch(base, patch, before, after, path, new_file_str=head)` |
| `out.plain.txt` | fast path, plain mode (`pr_generate_extended_diff`, `add_line_numbers_to_hunks=False`) |
| `out.numbered.txt` | fast path, numbered mode (`decouple_and_convert_to_hunks_with_lines_numbers` of the extended patch) |
| `out.raw-numbered.txt` | `decouple_and_convert_to_hunks_with_lines_numbers` of the unextended patch |
| `out.deletions.patch` | `handle_patch_deletions` result (compressed path) |
| `out.deleted` | present when `handle_patch_deletions` returned `None` (deleted file) |
| `out.compressed-plain.txt`, `out.compressed-numbered.txt` | the per-file entry `generate_full_patch` builds on the compressed path |

A `fetch_failed` case maps to an upstream `FilePatchInfo` with an empty
patch and `content_fetch_failed=True`, upstream's own trigger for
`_unreadable_file_notice`, which upstream renders on both paths. No oracle case
here renders the notice: review-mcp's notice names review-mcp instead of
PR-Agent (D4), so the former `unreadable` case is now the deviation case
`deviation_unreadable_notice`.

`TestUpstreamGoldens` (`golden_test.go`) runs every case in this directory
through `ParseHunks`, `ExtendFile`, `HandleDeletions` and every renderer and
compares byte for byte. It fails if a case here names a deviation.
`TestDeviationGoldens` runs the cases in `../deviations/` the same way.

## Cases

| Case | Covers |
|---|---|
| `ordinary` | an ordinary hunk |
| `start_of_file` | a hunk at the start of the file (pre-context clipped at line 1) |
| `eof` | a hunk at EOF (after-context clipped, size cap) |
| `overlap` | two hunks whose extensions overlap (upstream does not merge them) |
| `mismatch_header` | a header whose start line does not match the file: not extended |
| `crlf` | a CRLF file |
| `skip_md` | a `.md` file: extension skipped |
| `deleted` | a deleted file |
| `no_newline_eof` | `\ No newline at end of file` on both sides |
| `additions_only` | a hunk without deletions (no `__old hunk__`) |
| `multi_hunk` | three hunks, one deletion-only (dropped on the compressed path) |
| `malformed_header` | a combined-diff `@@@` pseudo-hunk between two hunks (skip rules) |
| `renamed` | a renamed file |
| `deletion_only` | every hunk deletion-only: upstream keeps the original patch |
| `omitted_counts` | difflib-style headers with omitted counts, no context |
| `added` | an added file (no base: not extended) |
| `section_in_context` | the section text appears in the pre-context and is dropped |
| `mini_match` | pre-context partly differs between base and head ("mini match") |
| `no_mini_match` | pre-context windows differ: extension reset, after-context still added |
| `zero_extra` | `before = after = 0` |
| `hunk_start_zero` | `@@ -0,0 +1,2 @@` on a non-empty base (Python negative slicing) |
| `after_only` | `before = 0`, `after = 3` |
| `max_context` | `before = after = 10` near both ends of a short file |
| `context_only_hunk` | a hand-crafted hunk with only context lines |

## Deviations

Cases where review-mcp deliberately differs from upstream are **not** oracle
output. They live in `internal/patch/testdata/deviations/`, outside this
directory, so regenerating the oracle goldens never touches them. Each one
names its decision in `case.json` (`"deviation": "<id>"`) and has a `NOTE`
file. `TestDeviationGoldens` requires both.

| Case | Decision | Covers |
|---|---|---|
| `deviation_python_line_breaks` | **D5** (architect, PR #4) | `\f` inside lines and inside the section text. review-mcp splits at `\n` only, so the numbered views carry the real file line numbers; upstream's `str.splitlines` splits there too and drifts. Formerly the oracle case `python_line_breaks`, with unchanged inputs. |
| `deviation_unreadable_notice` | **D4** (architect, PR #4) | The unreadable-file notice: the head-content fetch failed and the patch is empty, so every view renders the notice. The notice names review-mcp instead of PR-Agent. Formerly the oracle case `unreadable`, with unchanged inputs. |
| `deviation_head_fetch_failed_with_patch` | **D3** (architect, PR #4) | Gitea case: the head-content fetch failed (`head_status: fetch_failed`, no `head.txt`) but the patch from the PR's `.diff` is present. review-mcp renders that patch normally in every view (plain, numbered, compressed), unextended because the head content is nil (spec §3.2). It never shows the notice, which would claim that no diff is available. |

**D5, intentional deviation.** Upstream splits lines with `str.splitlines`,
which also breaks at `\f`, `\v`, `\x1c`–`\x1e`, `\x85`, U+2028, U+2029 and a
lone `\r`. A line containing one of them becomes several numbered lines, and
every later number drifts from the file's real line numbers. That is an
upstream bug, and numbered line numbers must equal real file line numbers:
the DQ-12 snippets and links (P4) and the v2 anchoring depend on it. So
review-mcp splits at `\n` only (`splitLines`, `pystr.go`). Per the lead
decision on the D5 implementation, `\r\n` is one line ending, dropped wherever
upstream drops it, so CRLF files (the `crlf` oracle case) render byte for
byte as upstream does. A lone `\r` is ordinary content, as in git.

Side effect: a patch line such as `" a\f@@ -1 +1 @@"` used to contain a Python
sub-line starting with `@@`, which upstream takes for a hunk header. It was a
documented parity gap: `Extend` and `OmitDeletionHunks` returned such a patch
unchanged. With `\n`-only splitting it is one ordinary line, and the patch is
extended and compacted normally (`TestExtendSubLineHeaderIsContent`).

The expected outputs come from the oracle, run on a **placeholder twin**.
The twin has the case's inputs with every `\f` replaced by U+E000, which is
neither a Python line break nor whitespace and does not occur otherwise, so
upstream treats it as review-mcp treats `\f`: as an ordinary character. Every
U+E000 in the twin's outputs is then replaced back by `\f`. The pre-context
check strips both sides of its comparison, so the substitution cannot change
it. `TestNumberedLineNumbersAreRealLineNumbers` checks the same property
independently, without the oracle. It uses a file whose lines contain `\f`,
`\v`, U+2028, `\x85` and a lone `\r`, and computes each expected number by
counting `\n`.

Running `random_cases.py` with `exotic` or `atpiece` now produces cases that
differ from upstream by design (D5). A local differential run is only
meaningful without those modes.

**D4, intentional deviation.** The model and the users should see the name
of the tool they are actually running. So the notice adapted from upstream's
`_unreadable_file_notice` says "review-mcp failed to fetch its contents"
where upstream says "PR-Agent failed to fetch its contents"; the rest of the
wording is unchanged. The literal is `unreadableNoticeBody` in
`internal/patch/literals.go`, and `NOTICE` covers it as adapted material.
The expected outputs come from the oracle, run on a twin with exactly the
case's inputs; the twin's outputs are byte-identical to the former oracle
golden. Exactly one substitution was then applied to every `out.*` file:
`** PR-Agent failed` becomes `** review-mcp failed`.
`TestNoUpstreamProductNameInGoldens` checks that no `out.*` file, oracle or
deviation, contains "PR-Agent". The golden runner also rejects any render
that contains it. Only provenance text may name the upstream product: this
README, the NOTE files and code comments.

**D3, intentional deviation.** Upstream shows the notice only for
`content_fetch_failed` with an empty patch (`deviation_unreadable_notice`
above), and review-mcp now uses that same trigger. For a fetch-failed file
that has a patch, upstream renders the patch but extends it against an empty
head string (`new_file_str=""`), which skips the pre-context check.
review-mcp does not extend without head content, so upstream cannot produce
this case's outputs.

How the expected outputs were derived: by the oracle, on a twin case. The
twin has the same path, type, patch and settings, `head_status: "full"`, and
no `base.txt` or `head.txt`. Upstream then has no original file, so
`extend_patch` returns the patch unchanged and every view renders it
normally. `handle_patch_deletions` does not read the base, so the compressed
outputs are unaffected. Every `out.*` file of the twin was copied unchanged
into the deviation case, whose `base.txt` is present but plays no part,
since review-mcp skips extension when the head is nil.

The script below writes every deviation case and its twin, reusing
`gitdiff`, `code` and `edit` from `make_cases.py`:

```sh
python3.12 make_deviation.py internal/patch/testdata/deviations <twin dir>
PYTHONPATH=... python3.12 gen_goldens.py /home/user/pr-agent-upstream <twin dir>
# D3: copy unchanged
cp <twin dir>/deviation_head_fetch_failed_with_patch/out.* \
   internal/patch/testdata/deviations/deviation_head_fetch_failed_with_patch/
# D4: copy with the single product-name substitution
for f in <twin dir>/deviation_unreadable_notice/out.*; do
  sed 's/\*\* PR-Agent failed/** review-mcp failed/' "$f" \
    > internal/patch/testdata/deviations/deviation_unreadable_notice/"$(basename "$f")"
done
# D5: copy with the placeholder replaced back by \f
for f in <twin dir>/deviation_python_line_breaks/out.*; do
  python3 -c 'import sys; d = open(sys.argv[1], encoding="utf-8", newline="").read(); open(sys.argv[2], "w", encoding="utf-8", newline="").write(d.replace("\ue000", "\f"))' \
    "$f" internal/patch/testdata/deviations/deviation_python_line_breaks/"$(basename "$f")"
done
```

```python
"""Write the intentional-deviation cases and the oracle twins used to derive
their outputs.

Usage: python3.12 make_deviation.py <deviations dir> <oracle twin dir>

Each deviation case gets a twin with the same name in the twin directory.
gen_goldens.py writes the twin's out.* files, which become the deviation
case's outputs as described in testdata/upstream/README.md ("Deviations").
"""
import json
import os
import sys

DEV, TWIN = sys.argv[1], sys.argv[2]

# Reuse gitdiff / code / edit from make_cases.py without running it.
src = open(os.path.join(os.path.dirname(os.path.abspath(__file__)), "make_cases.py")).read()
ns = {"__name__": "make_cases_lib"}
exec(src[:src.index("\nbase = ")], ns)
gitdiff, code, edit = ns["gitdiff"], ns["code"], ns["edit"]

base = "def run():\n" + code(29)
extra = {"before": 5, "after": 1, "skip_extend_extensions": [".md", ".txt"]}


def write(root, name, meta, patch, with_base):
    d = os.path.join(root, name)
    os.makedirs(d, exist_ok=True)
    with open(os.path.join(d, "case.json"), "w") as fh:
        fh.write(json.dumps(meta, indent=2) + "\n")
    with open(os.path.join(d, "patch.diff"), "w", newline="") as fh:
        fh.write(patch)
    if with_base:
        with open(os.path.join(d, "base.txt"), "w", newline="") as fh:
            fh.write(base)


# D3: head fetch failed, patch present. Twin: head_status "full" and no
# content, so upstream renders the patch unextended.
head = edit(base, {8: ["    value_7 = compute(77)\n"], 22: []})
patch = gitdiff(base, head)
name = "deviation_head_fetch_failed_with_patch"
common = {"path": "src/gitea_app.py", "old_path": "", "type": "modified"}
write(DEV, name, {**common, "head_status": "fetch_failed", **extra, "deviation": "D3"}, patch, True)
write(TWIN, name, {**common, "head_status": "full", **extra}, patch, False)

# D4: head fetch failed, empty patch: the unreadable notice, with the product
# name adapted. Twin: the same inputs; upstream renders its notice.
name = "deviation_unreadable_notice"
common = {"path": "src/unreadable.py", "old_path": "", "type": "modified",
          "head_status": "fetch_failed"}
write(DEV, name, {**common, **extra, "deviation": "D4"}, "", True)
write(TWIN, name, {**common, **extra}, "", True)

# D5: Python line breaks inside lines (a form feed page break, and a function
# line, which git copies into the section text, containing a form feed); the
# inputs are those of the former oracle case python_line_breaks. Twin: the
# same inputs with every \f replaced by PLACEHOLDER, a character that is
# neither a Python line break nor whitespace and does not occur otherwise;
# the twin's outputs with PLACEHOLDER replaced back by \f are the expected
# outputs.
PLACEHOLDER = "\ue000"
pbase = ("def page\fone():\n" + "".join(f"    a_{i} = {i}\n" for i in range(1, 6)) + "\f\n"
         + "".join(f"    b_{i} = {i}\n" for i in range(1, 12)))
phead = edit(pbase, {14: ["    b_7 = 70\n"]})
ppatch = gitdiff(pbase, phead)
assert PLACEHOLDER not in pbase + phead + ppatch


def twin(text):
    return text.replace("\f", PLACEHOLDER)


name = "deviation_python_line_breaks"
meta = {"path": "src/pages.py", "old_path": "", "type": "modified", "head_status": "full",
        "before": 8, "after": 2, "skip_extend_extensions": [".md", ".txt"]}
for root, conv, extra_meta in ((DEV, str, {"deviation": "D5"}), (TWIN, twin, {})):
    d = os.path.join(root, name)
    os.makedirs(d, exist_ok=True)
    with open(os.path.join(d, "case.json"), "w") as fh:
        fh.write(json.dumps({**meta, **extra_meta}, indent=2) + "\n")
    for fn, val in (("patch.diff", ppatch), ("base.txt", pbase), ("head.txt", phead)):
        with open(os.path.join(d, fn), "w", newline="") as fh:
            fh.write(conv(val))
```

Canaries:

- D3: restoring the earlier status-only trigger (`HeadStatus ==
  fetch_failed`) makes
  `TestDeviationGoldens/deviation_head_fetch_failed_with_patch` fail, and
  the unit test `TestFetchFailedWithPatchRendersPatch`.
- D4: restoring "PR-Agent" in the notice literal makes
  `TestDeviationGoldens/deviation_unreadable_notice` fail, and the unit test
  `TestRenderEmptyAndUnreadable`.
- D5: restoring `str.splitlines` semantics in `splitLines` makes
  `TestDeviationGoldens/deviation_python_line_breaks` fail, along with the
  unit tests `TestNumberedLineNumbersAreRealLineNumbers`,
  `TestExtendSubLineHeaderIsContent` and `TestSplitLines`.

## Regenerating

Requirements: the pinned upstream checkout (outside this repository), a
virtualenv with its pure-Python dependencies (`dynaconf`, `loguru`, ...), and
Python **3.12+**, because `pr_processing.py` uses PEP 701 f-strings that
Python 3.11 cannot parse. The 3.11 virtualenv's site-packages are reused
under `python3.12`; only pure-Python packages are imported.

```sh
# 1. inputs (git 2.43.0 produced the committed patches)
python3.12 make_cases.py internal/patch/testdata/upstream
# 2. expected outputs
PYTHONPATH=/home/user/pr-agent-upstream:/home/user/pr-agent-venv/lib/python3.11/site-packages \
  python3.12 gen_goldens.py /home/user/pr-agent-upstream internal/patch/testdata/upstream
```

Settings the oracle uses (see `gen_goldens.py`):

- `config.allow_dynamic_context = False` (DQ-1: static context only);
- `config.patch_extension_skip_types` = the case's `skip_extend_extensions`
  (`[".md", ".txt"]` everywhere);
- `config.verbosity_level = 0`;
- `extend_patch` is called with the case's `before`/`after` directly, which is
  what `get_pr_diff` passes after `cap_and_log_extra_lines`;
- a missing `base.txt` / `head.txt` becomes upstream's empty string.

`git_patch_processing` is imported and called. `pr_processing` cannot be
imported without its LLM dependencies, so `gen_goldens.py` takes the few
render statements it needs verbatim from the parsed upstream source (`ast`)
and executes them.

For a wider local differential run, generate random cases with
`random_cases.py <dir> <count> <seed>` (not `exotic` or `atpiece`, which now
differ from upstream by design, see D5), run `gen_goldens.py` on that
directory and point the test at it with
`REVIEW_MCP_PATCH_GOLDEN_DIR=<dir> go test -run TestUpstreamGoldens ./internal/patch/`.

## `make_cases.py`

```python
"""Build the synthetic input fixtures for internal/patch/testdata/upstream.

Each case directory gets: case.json, patch.diff, and base.txt / head.txt when
that side's content is present. Patches are produced by `git diff --no-index`
(hunk-only, like the providers) unless a case hand-crafts them.
"""
import difflib
import json
import os
import subprocess
import sys
import tempfile

OUT = sys.argv[1]


def gitdiff(base, head, ctx=3):
    with tempfile.TemporaryDirectory() as d:
        a, b = os.path.join(d, "a"), os.path.join(d, "b")
        with open(a, "w", newline="") as fh:
            fh.write(base)
        with open(b, "w", newline="") as fh:
            fh.write(head)
        r = subprocess.run(["git", "diff", "--no-index", "--no-color", f"-U{ctx}", a, b],
                           capture_output=True)
        out = r.stdout.decode()
    i = out.find("\n@@")
    return out[i + 1:]


def code(n, prefix="value"):
    return "".join(f"    {prefix}_{i} = compute({i})\n" for i in range(1, n + 1))


def lines(text):
    return text.splitlines(keepends=True)


def write(name, path, ctype, patch, base=None, head=None, before=5, after=1,
          skip=(".md", ".txt"), head_status=None, old_path=""):
    d = os.path.join(OUT, name)
    os.makedirs(d, exist_ok=True)
    if head_status is None:
        head_status = "not_applicable" if ctype == "deleted" else "full"
    meta = {"path": path, "old_path": old_path, "type": ctype, "head_status": head_status,
            "before": before, "after": after, "skip_extend_extensions": list(skip)}
    with open(os.path.join(d, "case.json"), "w") as fh:
        fh.write(json.dumps(meta, indent=2) + "\n")
    with open(os.path.join(d, "patch.diff"), "w", newline="") as fh:
        fh.write(patch)
    for fn, val in (("base.txt", base), ("head.txt", head)):
        p = os.path.join(d, fn)
        if val is not None:
            with open(p, "w", newline="") as fh:
                fh.write(val)
        elif os.path.exists(p):
            os.remove(p)


def edit(base, repl):
    ls = lines(base)
    for idx, new in sorted(repl.items(), reverse=True):
        ls[idx - 1:idx] = new
    return "".join(ls)


base = "def run():\n" + code(29)

# ordinary hunk in the middle of a file
head = edit(base, {15: ["    value_14 = compute(14) + 1\n", "    extra = value_14 * 2\n"]})
write("ordinary", "src/service/handler.py", "modified", gitdiff(base, head), base, head)

# hunk at the start of the file
head = edit(base, {2: ["    value_1 = compute(100)\n"]})
write("start_of_file", "src/app.go", "modified", gitdiff(base, head), base, head)

# hunk at EOF
head = edit(base, {30: ["    value_29 = compute(290)\n", "    return value_29\n"]})
write("eof", "lib/util.js", "modified", gitdiff(base, head), base, head)

# two hunks whose extensions overlap
head = edit(base, {10: ["    value_9 = compute(90)\n"], 18: ["    value_17 = compute(170)\n"]})
write("overlap", "src/overlap.py", "modified", gitdiff(base, head), base, head)

# mismatching hunk header: the header start line points two lines too far
head = edit(base, {15: ["    value_14 = compute(140)\n"]})
p = gitdiff(base, head)
first, rest = p.split("\n", 1)
assert first.startswith("@@ -12,7 +12,7 @@"), first
p = first.replace("@@ -12,7 +12,7 @@", "@@ -14,7 +14,7 @@", 1) + "\n" + rest
write("mismatch_header", "src/mismatch.py", "modified", p, base, head)

# CRLF file
crlf_base = base.replace("\n", "\r\n")
crlf_head = edit(base, {12: ["    value_11 = compute(110)\n"]}).replace("\n", "\r\n")
write("crlf", "src/Program.cs", "modified", gitdiff(crlf_base, crlf_head), crlf_base, crlf_head)

# .md file: extension is skipped
md_base = "# Guide\n\n" + "".join(f"Paragraph {i}.\n" for i in range(1, 21))
md_head = edit(md_base, {12: ["Paragraph 10, revised.\n"]})
write("skip_md", "docs/guide.md", "modified", gitdiff(md_base, md_head), md_base, md_head)

# deleted file
dbase = "import os\n\n\ndef legacy():\n    return os.getcwd()\n"
write("deleted", "old/legacy.py", "deleted", gitdiff(dbase, ""), dbase, None)

# no newline at end of file on both sides
nb = "[server]\nhost = localhost\nport = 8080\nworkers = 4\ntimeout = 30"
nh = "[server]\nhost = localhost\nport = 8080\nworkers = 8\ntimeout = 60"
write("no_newline_eof", "config/settings.ini", "modified", gitdiff(nb, nh), nb, nh)

# hunk without deletions
head = edit(base, {20: ["    value_19 = compute(19)\n", "    added_a = 1\n", "    added_b = 2\n"]})
write("additions_only", "pkg/store/cache.go", "modified", gitdiff(base, head), base, head)

# multi-hunk file: modification, deletion-only hunk, addition
mbase = "fn main() {\n" + code(59, "item")
mhead = edit(mbase, {8: ["    item_7 = compute(700)\n"], 25: [], 26: [],
                     45: ["    item_44 = compute(44)\n", "    item_extra = compute(0)\n"]})
write("multi_hunk", "src/engine/core.rs", "modified", gitdiff(mbase, mhead), mbase, mhead)

# malformed "@@" header (combined-diff style pseudo-hunk) between two hunks
head = edit(base, {6: ["    value_5 = compute(50)\n"], 22: ["    value_21 = compute(210)\n"]})
p = gitdiff(base, head)
h2 = p.index("\n@@ ", 1) + 1
p = p[:h2] + "@@@ -12,2 -12,2 +12,2 @@@\n- value_x\n +value_y\n+value_z\n" + p[h2:]
write("malformed_header", "src/malformed.py", "modified", p, base, head)

# renamed file with changes
head = edit(base, {8: ["    value_7 = compute(77)\n"]})
write("renamed", "src/new_name.py", "renamed", gitdiff(base, head), base, head,
      old_path="src/old_name.py")

# The unreadable-notice case (head fetch failed, empty patch) is written by
# make_deviation.py: it is the D4 deviation deviation_unreadable_notice.

# every hunk is deletion-only
head = edit(base, {5: [], 22: []})
write("deletion_only", "src/prune.py", "modified", gitdiff(base, head), base, head)

# omitted hunk counts (difflib style, no context)
head = edit(base, {9: ["    value_8 = compute(80)\n"], 21: ["    value_20 = compute(200)\n"]})
ud = "".join(difflib.unified_diff(lines(base), lines(head), n=0))
ud = ud[ud.index("@@"):]
write("omitted_counts", "src/counts.py", "modified", ud, base, head)

# added file
ahead = "package main\n\nfunc helper() int {\n\treturn 1\n}\n"
write("added", "cmd/helper.go", "added", gitdiff("", ahead), None, ahead)

# section header that falls inside the extended context
sbase = ("import sys\n\n\ndef handle(request):\n"
         + "".join(f"    step_{i} = request.get({i})\n" for i in range(1, 12))
         + "    return step_1\n")
shead = edit(sbase, {12: ["    step_8 = request.get(800)\n"]})
write("section_in_context", "src/section.py", "modified", gitdiff(sbase, shead), sbase, shead)

# pre-hunk lines differ partially between base and head (mini match), -U0
head = edit(base, {10: ["    value_9 = compute(900)\n"], 13: ["    value_12 = compute(1200)\n"]})
write("mini_match", "src/mini.py", "modified", gitdiff(base, head, 0), base, head)

# pre-hunk windows of different length in base and head (no mini match), -U0:
# two lines inserted at the top shift the second hunk's new start by two.
head = edit(base, {1: ["# top a\n", "# top b\n", "def run():\n"],
                   3: ["    value_2 = compute(200)\n"]})
write("no_mini_match", "src/nomini.py", "modified", gitdiff(base, head, 0), base, head)

# no extra lines configured
head = edit(base, {15: ["    value_14 = compute(14) + 1\n"]})
write("zero_extra", "src/zero.py", "modified", gitdiff(base, head), base, head, before=0, after=0)

# insertion at the very start with -U0: "@@ -0,0 +1,2 @@"
head = "# header a\n# header b\n" + base
write("hunk_start_zero", "src/start0.py", "modified", gitdiff(base, head, 0), base, head)

# after-lines only
head = edit(base, {15: ["    value_14 = compute(14) + 1\n"]})
write("after_only", "src/after.py", "modified", gitdiff(base, head), base, head, before=0, after=3)

# maximum context near both ends of a short file
short = "".join(f"row {i}\n" for i in range(1, 13))
shorth = edit(short, {6: ["row 6 changed\n"]})
write("max_context", "src/short.txt.go", "modified", gitdiff(short, shorth), short, shorth,
      before=10, after=10)

# The Python-line-breaks case (\f inside lines and inside the section text)
# is written by make_deviation.py: it is the D5 deviation
# deviation_python_line_breaks.

# hand-crafted context-only hunk after a normal hunk
head = edit(base, {6: ["    value_5 = compute(50)\n"]})
ctx = "".join(" " + line for line in lines(base)[19:22])
p = gitdiff(base, head) + "@@ -20,3 +20,3 @@ def run():\n" + ctx
write("context_only_hunk", "src/ctxonly.py", "modified", p, base, head)
```

## `gen_goldens.py`

```python
"""Parity oracle for internal/patch: run the pinned upstream functions on the
fixture inputs and write the expected outputs (out.* files) next to them.

Usage:
  PYTHONPATH=<upstream>:<venv>/lib/python3.11/site-packages \
      python3.12 gen_goldens.py <upstream> <testdata/upstream>

git_patch_processing is imported and called directly. pr_processing cannot be
imported without its LLM dependencies, so the few render statements the
goldens need are taken verbatim from its parsed source (ast) and executed:
_unreadable_file_notice, the plain branch of pr_generate_extended_diff, the
notice trimming of pr_generate_compressed_diff and the patch_final branches of
generate_full_patch. Python 3.12+ is needed because that file uses PEP 701
f-strings.
"""
import ast
import json
import os
import sys
import textwrap

UPSTREAM, CASES = sys.argv[1], sys.argv[2]

from pr_agent.config_loader import get_settings  # noqa: E402
import pr_agent.algo.git_patch_processing as gpp  # noqa: E402
from pr_agent.algo.types import EDIT_TYPE, FilePatchInfo  # noqa: E402

settings = get_settings(use_context=False)
settings.set("CONFIG.VERBOSITY_LEVEL", 0)
settings.config.allow_dynamic_context = False  # DQ-1: static context only

PP = os.path.join(UPSTREAM, "pr_agent", "algo", "pr_processing.py")
with open(PP) as fh:
    SRC = fh.read()
TREE = ast.parse(SRC)
FUNCS = {n.name: n for n in TREE.body if isinstance(n, ast.FunctionDef)}


def seg(node):
    return textwrap.dedent(ast.get_source_segment(SRC, node, padded=True))


def find_if(func, test_src):
    for node in ast.walk(FUNCS[func]):
        if isinstance(node, ast.If) and ast.get_source_segment(SRC, node.test) == test_src:
            return node
    raise SystemExit(f"{func}: no `if {test_src}` found")


def make(name, params, stmts, result):
    body = "\n".join(seg(s) for s in stmts)
    code = f"def {name}({params}):\n" + textwrap.indent(body, "    ") + f"\n    return {result}\n"
    ns = {"decouple_and_convert_to_hunks_with_lines_numbers":
          gpp.decouple_and_convert_to_hunks_with_lines_numbers}
    exec(compile(code, PP, "exec"), ns)
    return ns[name]


ns = {}
exec(compile(ast.Module([FUNCS["_unreadable_file_notice"]], []), PP, "exec"), ns)
unreadable_notice = ns["_unreadable_file_notice"]

# pr_generate_extended_diff: `if add_line_numbers_to_hunks: ... else: <plain>`
_ext = find_if("pr_generate_extended_diff", "add_line_numbers_to_hunks")
render_extended_plain = make("render_extended_plain", "file, extended_patch",
                             _ext.orelse, "full_extended_patch")
render_extended_numbered = make("render_extended_numbered", "file, extended_patch",
                                _ext.body, "full_extended_patch")

# pr_generate_compressed_diff: `if not convert_hunks_to_line_numbers: note = ...`
_note = find_if("pr_generate_compressed_diff", "not convert_hunks_to_line_numbers")
trim_note = make("trim_note", "note", _note.body, "note")

# generate_full_patch: `if not convert_hunks_to_line_numbers: patch_final = ... else: ...`
_full = find_if("generate_full_patch", "not convert_hunks_to_line_numbers")
final_plain = make("final_plain", "filename, patch", _full.body, "patch_final")
final_numbered = make("final_numbered", "filename, patch", _full.orelse, "patch_final")

EDIT = {"added": EDIT_TYPE.ADDED, "modified": EDIT_TYPE.MODIFIED,
        "deleted": EDIT_TYPE.DELETED, "renamed": EDIT_TYPE.RENAMED}


def read(path):
    if not os.path.exists(path):
        return None
    with open(path, newline="") as fh:
        return fh.read()


def write(path, text):
    with open(path, "w", newline="") as fh:
        fh.write(text)


for name in sorted(os.listdir(CASES)):
    d = os.path.join(CASES, name)
    if not os.path.isdir(d):
        continue
    for fn in os.listdir(d):
        if fn.startswith("out."):
            os.remove(os.path.join(d, fn))
    with open(os.path.join(d, "case.json")) as fh:
        case = json.load(fh)
    patch = read(os.path.join(d, "patch.diff"))
    base = read(os.path.join(d, "base.txt")) or ""
    head = read(os.path.join(d, "head.txt")) or ""
    settings.config.patch_extension_skip_types = case["skip_extend_extensions"]
    unreadable = case["head_status"] == "fetch_failed"
    file = FilePatchInfo(base_file=base, head_file=head, patch="" if unreadable else patch,
                         filename=case["path"], edit_type=EDIT[case["type"]],
                         old_filename=case["old_path"] or None,
                         content_fetch_failed=unreadable)

    if unreadable:
        # Upstream renders the notice for a fetch-failed file in both paths.
        note = unreadable_notice(file)
        write(os.path.join(d, "out.plain.txt"), note)
        write(os.path.join(d, "out.numbered.txt"), note)
        write(os.path.join(d, "out.compressed-plain.txt"), final_plain(file.filename, trim_note(note)))
        write(os.path.join(d, "out.compressed-numbered.txt"), final_numbered(file.filename, note))
        continue

    # Fast (extended) path: pr_generate_extended_diff.
    extended = gpp.extend_patch(base, patch, case["before"], case["after"], file.filename,
                                new_file_str=head)
    write(os.path.join(d, "out.extended.patch"), extended)
    write(os.path.join(d, "out.plain.txt"), render_extended_plain(file, extended))
    write(os.path.join(d, "out.numbered.txt"), render_extended_numbered(file, extended))
    # Numbered render of the unextended patch.
    write(os.path.join(d, "out.raw-numbered.txt"),
          gpp.decouple_and_convert_to_hunks_with_lines_numbers(patch, file))

    # Compressed path: pr_generate_compressed_diff + generate_full_patch.
    handled = gpp.handle_patch_deletions(patch, base, head, file.filename, file.edit_type)
    if handled is None:
        write(os.path.join(d, "out.deleted"), "")
        continue
    write(os.path.join(d, "out.deletions.patch"), handled)
    write(os.path.join(d, "out.compressed-plain.txt"), final_plain(file.filename, handled))
    write(os.path.join(d, "out.compressed-numbered.txt"),
          final_numbered(file.filename,
                         gpp.decouple_and_convert_to_hunks_with_lines_numbers(handled, file)))
    print(f"{name}: ok", file=sys.stderr)
```

## `random_cases.py`

```python
"""Generate many random fixture directories for a local differential run.

Usage: python3.12 random_cases.py <outdir> <count> <seed>
"""
import json
import os
import random
import shutil
import subprocess
import sys
import tempfile

OUT, COUNT, SEED = sys.argv[1], int(sys.argv[2]), int(sys.argv[3])
EXOTIC = len(sys.argv) > 4 and sys.argv[4] == "exotic"
rnd = random.Random(SEED)
VOCAB = ["def handler(request):", "    return value", "", "    x = compute(1)", "class Store:",
         "    # comment", "}", "{", "  item = 2", "\tindented()", "import os", "    pass",
         "func main() {", "  y := 3", "end", "  ", "@@ looks like a header", "+plus in text",
         "-minus in text", "\\ backslash line"]
if EXOTIC:
    VOCAB += ["page\fbreak", "vt\x0bhere", "fs\x1csep", "nel\x85line", "ls x", "lone\rcr",
              "\\ No newline at end of file", "tab\x1f unit", "  　wide space"]
if len(sys.argv) > 5 and sys.argv[5] == "atpiece":
    VOCAB += ["a\f@@ -1 +1 @@"]


def gitdiff(base, head, ctx):
    with tempfile.TemporaryDirectory() as d:
        a, b = os.path.join(d, "a"), os.path.join(d, "b")
        with open(a, "w", newline="") as fh:
            fh.write(base)
        with open(b, "w", newline="") as fh:
            fh.write(head)
        r = subprocess.run(["git", "diff", "--no-index", "--no-color", f"-U{ctx}", a, b],
                           capture_output=True)
        out = r.stdout.decode()
    i = out.find("\n@@")
    if i < 0:
        return ""
    return out[i + 1:]


def text(lines, eol, final):
    s = eol.join(lines)
    if lines and final:
        s += eol
    return s


shutil.rmtree(OUT, ignore_errors=True)
os.makedirs(OUT)
made = 0
while made < COUNT:
    n = rnd.randint(1, 40)
    base_lines = [rnd.choice(VOCAB) + (f" {i}" if rnd.random() < 0.7 else "") for i in range(n)]
    head_lines = list(base_lines)
    for _ in range(rnd.randint(1, 5)):
        op = rnd.random()
        pos = rnd.randint(0, len(head_lines))
        if op < 0.35 and head_lines:
            del head_lines[min(pos, len(head_lines) - 1)]
        elif op < 0.7:
            head_lines.insert(pos, rnd.choice(VOCAB) + " new")
        elif head_lines:
            head_lines[min(pos, len(head_lines) - 1)] += " changed"
    eol = "\r\n" if rnd.random() < 0.15 else "\n"
    bfinal = rnd.random() > 0.15
    hfinal = rnd.random() > 0.15
    kind = rnd.choices(["modified", "deleted", "added", "renamed"], [0.7, 0.1, 0.1, 0.1])[0]
    base = text(base_lines, eol, bfinal)
    head = text(head_lines, eol, hfinal)
    if kind == "deleted":
        head = ""
    if kind == "added":
        base = ""
    patch = gitdiff(base, head, rnd.choice([0, 1, 3, 3, 3, 5]))
    if not patch:
        continue
    # Occasional corruptions.
    r = rnd.random()
    lines = patch.split("\n")
    hdrs = [i for i, line in enumerate(lines) if line.startswith("@@ ")]
    if r < 0.1 and hdrs:
        i = rnd.choice(hdrs)
        lines.insert(i, "@@@ -1,2 -1,2 +1,2 @@@")
        lines.insert(i + 1, rnd.choice(["+pseudo add", "-pseudo del", " pseudo ctx"]))
    elif r < 0.2 and hdrs:
        i = rnd.choice(hdrs)
        old = lines[i]
        parts = old.split(" ")
        a = parts[1][1:].split(",")
        a[0] = str(max(0, int(a[0]) + rnd.choice([-2, -1, 1, 2])))
        parts[1] = "-" + ",".join(a)
        lines[i] = " ".join(parts)
    patch = "\n".join(lines)
    d = os.path.join(OUT, f"r{made:04d}")
    os.makedirs(d)
    path = rnd.choice(["src/a.py", "docs/readme.md", "notes.txt", "pkg/b.go", "  spaced.go "])
    meta = {"path": path, "old_path": "src/old.py" if kind == "renamed" else "", "type": kind,
            "head_status": "not_applicable" if kind == "deleted" else "full",
            "before": rnd.randint(0, 10), "after": rnd.randint(0, 10),
            "skip_extend_extensions": [".md", ".txt"]}
    with open(os.path.join(d, "case.json"), "w") as fh:
        json.dump(meta, fh)
    with open(os.path.join(d, "patch.diff"), "w", newline="") as fh:
        fh.write(patch)
    if kind != "added":
        with open(os.path.join(d, "base.txt"), "w", newline="") as fh:
            fh.write(base)
    if kind != "deleted":
        with open(os.path.join(d, "head.txt"), "w", newline="") as fh:
            fh.write(head)
    made += 1
```
