#!/usr/bin/env python3
"""V-14 (X-47) acceptance on a throwaway local Gitea: pr_review_decision.

Usage:
  python3 v27gitea.py setup      # start Gitea, create users, repo and pull requests
  python3 v27gitea.py run        # run Q1-Q8 against review-mcp (RM, default @next)
  python3 v27gitea.py teardown   # remove the container and the state file

Environment:
  RM          review-mcp command (default: "npx -y @nevzatcirak/review-mcp@next")
  V27_PORT    host port for Gitea on 127.0.0.1 (default 3300)
  V27_IMAGE   Gitea image (default gitea/gitea:1.24)
  V27_GITEA_BIN  path to a Gitea binary: run it directly instead of Docker
              (work dir ~/.v27gitea-work; run as root, it sets
              I_AM_BEING_UNSAFE_RUNNING_AS_ROOT in app.ini)

State (tokens included) is kept in ~/.v27gitea.json with mode 0600.
The record written by `run` (~/v27gitea-record.json) carries no token.
Everything here is synthetic: two local users, one demo repository.
"""
import base64
import json
import os
import shlex
import subprocess
import sys
import time
import urllib.error
import urllib.request

NAME = "rm-v27-gitea"
PORT = int(os.environ.get("V27_PORT", "3300"))
IMAGE = os.environ.get("V27_IMAGE", "gitea/gitea:1.24")
BASE = f"http://127.0.0.1:{PORT}"
STATE = os.path.expanduser("~/.v27gitea.json")
RECORD = os.path.expanduser("~/v27gitea-record.json")
GITEA_BIN = os.environ.get("V27_GITEA_BIN", "")
WORK = os.path.expanduser("~/.v27gitea-work")
RM = shlex.split(os.environ.get("RM", "npx -y @nevzatcirak/review-mcp@next"))

AUTHOR, REVIEWER = "rmauthor", "rmreviewer"
PASSWORD = "Accept-v27-" + str(int(time.time()))


def sh(*args, check=True):
    r = subprocess.run(args, capture_output=True, text=True)
    if check and r.returncode != 0:
        sys.exit(f"command failed: {' '.join(args[:4])} ...\n{r.stderr.strip()}")
    return r.stdout.strip()


def api(method, path, token=None, body=None, ok=(200, 201, 204)):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(BASE + "/api/v1" + path, data=data, method=method)
    req.add_header("Content-Type", "application/json")
    if token:
        req.add_header("Authorization", "token " + token)
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            raw = resp.read()
            return json.loads(raw) if raw else None
    except urllib.error.HTTPError as e:
        if e.code in ok:
            return None
        sys.exit(f"Gitea API {method} {path}: HTTP {e.code}")


def b64(s):
    return base64.b64encode(s.encode()).decode()


# ---------------------------------------------------------------- setup

CALC_V1 = '''def add(a, b):
    return a + b


def divide(a, b):
    return a / b
'''
CALC_V2 = CALC_V1 + '''

def ratio(a, b):
    """Return a / b, or None when b is zero."""
    if b == 0:
        return None
    return divide(a, b)
'''
CALC_V3 = CALC_V2 + '''

def percent(a, b):
    r = ratio(a, b)
    return None if r is None else r * 100
'''


def native_env():
    env = dict(os.environ, GITEA_WORK_DIR=WORK)
    return env


def native_cli(*a):
    return [GITEA_BIN, *a, "--config", os.path.join(WORK, "app.ini"), "--work-path", WORK]


def start_gitea():
    if not GITEA_BIN:
        sh("docker", "run", "-d", "--name", NAME, "-p", f"127.0.0.1:{PORT}:3000",
           "-e", "GITEA__security__INSTALL_LOCK=true",
           "-e", "GITEA__database__DB_TYPE=sqlite3",
           "-e", f"GITEA__server__ROOT_URL={BASE}/",
           "-e", "GITEA__service__DISABLE_REGISTRATION=true",
           IMAGE)
        return
    os.makedirs(WORK, exist_ok=False)
    with open(os.path.join(WORK, "app.ini"), "w") as f:
        root = ""
        if os.geteuid() == 0:
            root = "RUN_USER = root\nI_AM_BEING_UNSAFE_RUNNING_AS_ROOT = true\n"
        f.write(root + f"""[database]
DB_TYPE = sqlite3
PATH = {WORK}/gitea.db
[repository]
ROOT = {WORK}/repos
[server]
HTTP_ADDR = 127.0.0.1
HTTP_PORT = {PORT}
ROOT_URL = {BASE}/
APP_DATA_PATH = {WORK}/data
LFS_START_SERVER = false
[security]
INSTALL_LOCK = true
[service]
DISABLE_REGISTRATION = true
[log]
ROOT_PATH = {WORK}/log
""")
    log = open(os.path.join(WORK, "web.log"), "w")
    p = subprocess.Popen(native_cli("web"), stdout=log, stderr=log, env=native_env(), start_new_session=True)
    with open(os.path.join(WORK, "pid"), "w") as f:
        f.write(str(p.pid))


def gitea(*a):
    if GITEA_BIN:
        r = subprocess.run(native_cli(*a), capture_output=True, text=True, env=native_env())
        if r.returncode != 0:
            sys.exit(f"gitea {a[0]} {a[1]} failed:\n{r.stderr.strip()[-500:]}")
        return r.stdout.strip()
    return sh("docker", "exec", "-u", "git", NAME, "gitea", *a)


def setup():
    if os.path.exists(STATE):
        sys.exit(f"{STATE} exists: run teardown first")
    start_gitea()
    for _ in range(60):
        try:
            with urllib.request.urlopen(BASE + "/api/v1/version", timeout=3) as r:
                version = json.loads(r.read())["version"]
                break
        except Exception:
            time.sleep(2)
    else:
        sys.exit("Gitea did not come up in 120 s")
    print(f"Gitea {version} at {BASE}")

    for user, admin in ((AUTHOR, True), (REVIEWER, False)):
        args = ["admin", "user", "create", "--username", user, "--password", PASSWORD,
                "--email", f"{user}@example.com", "--must-change-password=false"]
        if admin:
            args.append("--admin")
        gitea(*args)
    tokens = {u: gitea("admin", "user", "generate-access-token", "--username", u,
                       "--token-name", "v27", "--scopes", "all", "--raw").splitlines()[-1].strip()
              for u in (AUTHOR, REVIEWER)}
    ta, tr = tokens[AUTHOR], tokens[REVIEWER]

    api("POST", "/user/repos", ta, {"name": "demo", "auto_init": True, "default_branch": "main"})
    api("PUT", f"/repos/{AUTHOR}/demo/collaborators/{REVIEWER}", ta, {"permission": "write"})
    api("POST", f"/repos/{AUTHOR}/demo/contents/src/calc.py", ta,
        {"branch": "main", "content": b64(CALC_V1), "message": "Add calc module"})
    # PR 1: opened by the author; the reviewer decides on it.
    cur = api("GET", f"/repos/{AUTHOR}/demo/contents/src/calc.py?ref=main", ta)
    api("PUT", f"/repos/{AUTHOR}/demo/contents/src/calc.py", ta,
        {"branch": "main", "new_branch": "feature-ratio", "sha": cur["sha"],
         "content": b64(CALC_V2), "message": "Add ratio"})
    pr1 = api("POST", f"/repos/{AUTHOR}/demo/pulls", ta,
              {"head": "feature-ratio", "base": "main", "title": "Add ratio helper"})
    # PR 2: opened by the reviewer (the token's user): a decision on it must be refused.
    api("PUT", f"/repos/{AUTHOR}/demo/contents/src/calc.py", tr,
        {"branch": "main", "new_branch": "reviewer-own", "sha": cur["sha"],
         "content": b64(CALC_V1 + "\n# note\n"), "message": "Add a note"})
    pr2 = api("POST", f"/repos/{AUTHOR}/demo/pulls", tr,
              {"head": "reviewer-own", "base": "main", "title": "Add a note"})

    state = {"base": BASE, "version": version, "tokens": tokens,
             "pr1": pr1["number"], "pr2": pr2["number"]}
    fd = os.open(STATE, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w") as f:
        json.dump(state, f)
    print(f"PR #{pr1['number']} by {AUTHOR}: {BASE}/{AUTHOR}/demo/pulls/{pr1['number']}")
    print(f"PR #{pr2['number']} by {REVIEWER}: {BASE}/{AUTHOR}/demo/pulls/{pr2['number']}")
    print(f"web login: {AUTHOR} / {REVIEWER}, password {PASSWORD}")


def teardown():
    if GITEA_BIN:
        import shutil
        import signal
        try:
            os.killpg(int(open(os.path.join(WORK, "pid")).read()), signal.SIGTERM)
        except Exception:
            pass
        shutil.rmtree(WORK, ignore_errors=True)
    else:
        sh("docker", "rm", "-f", NAME, check=False)
    if os.path.exists(STATE):
        os.remove(STATE)
    print("removed")


# ---------------------------------------------------------------- MCP client

class Server:
    def __init__(self, env_extra, log):
        env = dict(os.environ)
        for k in list(env):
            if k.startswith("REVIEW_MCP_"):
                del env[k]
        env.update(env_extra)
        self.log = open(log, "w")
        self.p = subprocess.Popen(RM, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                  stderr=self.log, text=True, env=env)
        self.n = 0
        init = self.req("initialize", {"protocolVersion": "2025-06-18", "capabilities": {},
                                       "clientInfo": {"name": "v27gitea", "version": "1"}})
        self.instructions = init.get("instructions", "")
        self.send({"jsonrpc": "2.0", "method": "notifications/initialized"})

    def send(self, msg):
        self.p.stdin.write(json.dumps(msg) + "\n")
        self.p.stdin.flush()

    def req(self, method, params):
        self.n += 1
        self.send({"jsonrpc": "2.0", "id": self.n, "method": method, "params": params})
        while True:
            line = self.p.stdout.readline()
            if not line:
                sys.exit(f"server exited; see {self.log.name}")
            msg = json.loads(line)
            if msg.get("id") == self.n:
                if "error" in msg:
                    return {"rpc_error": msg["error"]}
                return msg["result"]

    def tools(self):
        return {t["name"]: t for t in self.req("tools/list", {})["tools"]}

    def call(self, name, args):
        r = self.req("tools/call", {"name": name, "arguments": args})
        text = "".join(c.get("text", "") for c in r.get("content", []))
        return bool(r.get("isError")), text, r.get("structuredContent")

    def close(self):
        self.p.stdin.close()
        try:
            self.p.wait(10)
        except subprocess.TimeoutExpired:
            self.p.kill()
        self.log.close()


# ---------------------------------------------------------------- run

def run():
    st = json.load(open(STATE))
    tr, ta = st["tokens"][REVIEWER], st["tokens"][AUTHOR]
    pr1 = f"{BASE}/{AUTHOR}/demo/pulls/{st['pr1']}"
    pr2 = f"{BASE}/{AUTHOR}/demo/pulls/{st['pr2']}"
    common = {
        "REVIEW_MCP_GITEA_BASE_URL": BASE,
        "REVIEW_MCP_GITEA_TOKEN": tr,
        # No model is called by pr_info or pr_review_decision; the LLM settings
        # only satisfy the configuration check.
        "REVIEW_MCP_LLM_BASE_URL": "http://127.0.0.1:9/v1",
        "REVIEW_MCP_LLM_MODEL": "unused",
        "REVIEW_MCP_LLM_API_KEY": "unused",
        "REVIEW_MCP_LLM_CONTEXT_WINDOW": "32000",
        "REVIEW_MCP_LOG_LEVEL": "debug",
    }
    results = []

    def check(q, name, ok, detail=""):
        results.append({"q": q, "check": name, "pass": bool(ok), "detail": detail})
        print(f"{'PASS' if ok else 'FAIL'}  {q:4} {name}" + (f"  [{detail}]" if detail and not ok else ""))

    def reviews(n):
        return api("GET", f"/repos/{AUTHOR}/demo/pulls/{n}/reviews", ta) or []

    def mine(n):
        return [r for r in reviews(n) if r["user"]["login"] == REVIEWER]

    # Q1: default configuration.
    s = Server(common, "v27-q1.log")
    tools = s.tools()
    check("Q1", "tool absent by default", "pr_review_decision" not in tools)
    check("Q1", "instructions do not name it", "pr_review_decision" not in s.instructions)
    _, _, info = s.call("server_info", {})
    val = ((info or {}).get("config", {}).get("values", {}).get("decision.enabled") or {}).get("value")
    check("Q1", "server_info decision.enabled false", val is False, f"value={val!r}")
    s.close()

    # Enabled server for Q2-Q6.
    s = Server(dict(common, REVIEW_MCP_DECISION_ENABLED="true"), "v27-q2-q6.log")
    tools = s.tools()
    t = tools.get("pr_review_decision")
    check("Q1", "tool listed when enabled", t is not None)
    ann = (t or {}).get("annotations", {})
    check("Q1", "annotations destructive+idempotent, not read-only",
          ann.get("destructiveHint") is True and ann.get("idempotentHint") is True and not ann.get("readOnlyHint"),
          json.dumps(ann))
    check("Q1", "instructions name it when enabled", "pr_review_decision" in s.instructions)

    def head(url):
        _, _, r = s.call("pr_info", {"pr_url": url})
        return r["head_sha"], r

    def decide(url, decision, sha, body=None):
        a = {"pr_url": url, "decision": decision, "head_sha": sha}
        if body is not None:
            a["body"] = body
        return s.call("pr_review_decision", a)

    # Q2: approve at the reviewed head; repeat is unchanged.
    h1, _ = head(pr1)
    err, text, r = decide(pr1, "approve", h1)
    check("Q2", "approve succeeds", not err and r and r["state"] == "APPROVED" and not r["unchanged"], text.strip())
    check("Q2", "result pr.kind is gitea", r and r.get("pr", {}).get("kind") == "gitea", json.dumps((r or {}).get("pr")))
    m = mine(st["pr1"])
    check("Q2", "Gitea has one APPROVED review at the head",
          len(m) == 1 and m[0]["state"] == "APPROVED" and m[0]["commit_id"] == h1, json.dumps([(x["state"], x["commit_id"][:7]) for x in m]))
    _, info1 = head(pr1)
    rv = [x for x in (info1.get("reviewers") or []) if x["user"]["login"] == REVIEWER]
    check("Q2", "pr_info lists the reviewer as approved", rv and rv[0]["state"] == "approved", json.dumps(rv))
    err, text, r = decide(pr1, "approve", h1)
    check("Q2", "second approve is unchanged", not err and r and r["unchanged"] and r["state"] == "", text.strip())
    check("Q2", "no second review written", len(mine(st["pr1"])) == 1)

    # Q3: the author pushes; approve with the old head is refused.
    cur = api("GET", f"/repos/{AUTHOR}/demo/contents/src/calc.py?ref=feature-ratio", ta)
    api("PUT", f"/repos/{AUTHOR}/demo/contents/src/calc.py", ta,
        {"branch": "feature-ratio", "sha": cur["sha"], "content": b64(CALC_V3), "message": "Add percent"})
    for _ in range(15):
        h2, _ = head(pr1)
        if h2 != h1:
            break
        time.sleep(2)
    check("Q3", "head moved after the push", h2 != h1)
    before = len(mine(st["pr1"]))
    err, text, _ = decide(pr1, "approve", h1)
    check("Q3", "approve at the old head refused", err and "the pull request head is now" in text, text.strip())
    check("Q3", "nothing written", len(mine(st["pr1"])) == before)

    # Q4: the token user's own pull request.
    h_own, _ = head(pr2)
    err, text, _ = decide(pr2, "approve", h_own)
    check("Q4", "own pull request refused", err and "author of this pull request" in text, text.strip())
    check("Q4", "nothing written on it", len(reviews(st["pr2"])) == 0)

    # Q5: request_changes needs a body on Gitea; with one it writes a review.
    err, text, _ = decide(pr1, "request_changes", h2)
    check("Q5", "request_changes without body refused", err and "body is required for request_changes on Gitea" in text, text.strip())
    err, text, _ = decide(pr1, "request_changes", h2, body="   ")
    check("Q5", "blank body counts as no body", err and "body is required" in text, text.strip())
    n_before = len(mine(st["pr1"]))
    body = "Please add a test for percent with b == 0."
    err, text, r = decide(pr1, "request_changes", h2, body=body)
    check("Q5", "request_changes succeeds", not err and r and r["state"] == "REQUEST_CHANGES" and r["body_posted"] == "review", text.strip())
    m = mine(st["pr1"])
    last = m[-1] if m else {}
    check("Q5", "Gitea review REQUEST_CHANGES with the body at the new head",
          len(m) == n_before + 1 and last.get("state") == "REQUEST_CHANGES" and last.get("body") == body and last.get("commit_id") == h2,
          json.dumps({"state": last.get("state"), "commit": (last.get("commit_id") or "")[:7], "body_ok": last.get("body") == body}))
    _, info1 = head(pr1)
    rv = [x for x in (info1.get("reviewers") or []) if x["user"]["login"] == REVIEWER]
    check("Q5", "pr_info shows changes_requested", rv and rv[0]["state"] == "changes_requested", json.dumps(rv))

    # Q6: clear is not supported on Gitea.
    n_before = len(reviews(st["pr1"]))
    err, text, _ = decide(pr1, "clear", h2)
    check("Q6", "clear refused on Gitea", err and "Gitea does not support clear through its API" in text, text.strip())
    check("Q6", "nothing written", len(reviews(st["pr1"])) == n_before)
    s.close()

    # Q7: decision.allowed = request_changes only.
    s = Server(dict(common, REVIEW_MCP_DECISION_ENABLED="true", REVIEW_MCP_DECISION_ALLOWED="request_changes"), "v27-q7.log")
    n_before = len(reviews(st["pr1"]))
    err, text, _ = s.call("pr_review_decision", {"pr_url": pr1, "decision": "approve", "head_sha": h2})
    check("Q7", "approve refused by decision.allowed", err and "decision.allowed does not include approve" in text, text.strip())
    check("Q7", "nothing written", len(reviews(st["pr1"])) == n_before)
    s.close()

    # Q8: serve refuses decision.enabled at startup.
    env = {k: v for k, v in os.environ.items() if not k.startswith("REVIEW_MCP_")}
    env.update({k: v for k, v in common.items() if k != "REVIEW_MCP_GITEA_TOKEN"})
    env.update({"REVIEW_MCP_DECISION_ENABLED": "true", "REVIEW_MCP_SERVE_LISTEN": "127.0.0.1:0"})
    p = subprocess.Popen(RM + ["serve"], stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, env=env)
    try:
        out, _ = p.communicate(timeout=60)
        rc = p.returncode
    except subprocess.TimeoutExpired:
        p.kill()
        out, rc = p.communicate()[0], "still running after 60 s"
    sentence = "decision.enabled: review decisions are not available in serve mode"
    check("Q8", "serve refuses decision.enabled", sentence in out and rc not in (0, "still running after 60 s"),
          f"rc={rc}")

    # Secrets never in the logs.
    leaked = [f for f in ("v27-q1.log", "v27-q2-q6.log", "v27-q7.log")
              if any(tok in open(f).read() for tok in (tr, ta))]
    check("all", "no token in any server log", not leaked, ",".join(leaked))

    version = sh(*RM, "version", check=False)
    record = {"review_mcp": version, "gitea": st["version"], "results": results,
              "passed": sum(r["pass"] for r in results), "total": len(results)}
    with open(RECORD, "w") as f:
        json.dump(record, f, indent=2)
    print(f"\n{record['passed']}/{record['total']} passed; record: {RECORD}")


if __name__ == "__main__":
    cmd = sys.argv[1] if len(sys.argv) > 1 else ""
    {"setup": setup, "run": run, "teardown": teardown}.get(cmd, lambda: sys.exit(__doc__))()
