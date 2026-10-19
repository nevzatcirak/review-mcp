#!/usr/bin/env python3
"""Summarise a pr_review / pr_ask / pr_info result saved by ~/mcpcall.py (V-13b acceptance).

Usage: python3 v25sum.py OUT.json
Prints: tool error (if any), coverage.related, repository tool calls, notes,
findings (file, line, and any related/cross-repository fields), and for pr_ask
the answer's lines that cite R1..R4 or contain a link. Prints no token.
"""
import json, re, sys

d = json.load(open(sys.argv[1]))
res = d.get("result", d)
if res.get("isError"):
    print("TOOL ERROR:", res["content"][0]["text"])
    sys.exit(0)
sc = res.get("structuredContent")
if sc is None:
    try:
        sc = json.loads(res["content"][0]["text"])
    except Exception:
        sc = {}
r = sc.get("result", sc)

cov = r.get("coverage") or {}
print("== coverage.related")
for it in cov.get("related") or r.get("related") or []:
    print("  ", json.dumps(it, ensure_ascii=False))
tools = (cov.get("repo_context") or {}).get("tools")
if tools:
    print("== repo tools: status=%s reason=%s rounds=%s" % (tools.get("status"), tools.get("reason"), tools.get("rounds")))
    for c in tools.get("calls") or []:
        print("   ", c.get("repo", "-"), c.get("tool"), c.get("status"), c.get("target"))
rc = r.get("repo_config")
if rc:
    print("== repo_config:", json.dumps({k: rc.get(k) for k in ("status", "applied", "ignored")}))
print("== notes")
for n in r.get("notes") or []:
    print("   -", n)
fs = r.get("findings") or r.get("key_issues") or []
print("== findings:", len(fs))
for f in fs:
    extra = {k: v for k, v in f.items() if re.search(r"related|repo|link|url|cross|unverified", k, re.I)}
    print("   *", f.get("file") or f.get("relevant_file"), f.get("start_line") or f.get("line"), "|",
          (f.get("header") or f.get("title") or "")[:80], "|", json.dumps(extra, ensure_ascii=False))
ans = r.get("answer")
if ans:
    print("== answer lines citing R<n> or a link")
    for line in ans.splitlines():
        if re.search(r"\bR[1-4]\b|https?://", line):
            print("   ", line[:240])
md = r.get("metadata") or {}
print("== metadata:", json.dumps({k: md.get(k) for k in ("llm_calls", "tool_rounds", "incremental", "mode") if k in md}))
pub = r.get("publish")
if pub:
    print("== publish:", json.dumps({k: pub.get(k) for k in ("published", "overview_url", "inline_posted") if k in pub}))
