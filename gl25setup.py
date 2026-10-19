#!/usr/bin/env python3
"""Set up the V-13b consumer fixture on the throwaway GitLab project.

Usage: gl25setup.py setup | check

setup creates branch accept-v25-consumer from the default branch with a small
web client that reads the profile field userName and calls /profiles/{id}.
check prints whether the default branch and the consumer branch mention
userName (the default branch must not, for Q2).
The token is read from REVIEW_MCP_GITLAB_TOKEN and only sent as a header.
"""
import json, os, sys, urllib.error, urllib.parse, urllib.request

BASE = os.environ.get("REVIEW_MCP_GITLAB_BASE_URL", "https://gitlab.com").rstrip("/")
PROJECT = urllib.parse.quote(os.environ.get("RMCP_GL_PROJECT", "nevzatcirak/rmcp-accept"), safe="")
TOKEN = os.environ["REVIEW_MCP_GITLAB_TOKEN"]
BRANCH = "accept-v25-consumer"


def api(method, path, body=None):
    req = urllib.request.Request(f"{BASE}/api/v4/projects/{PROJECT}{path}", method=method,
                                 data=json.dumps(body).encode() if body is not None else None,
                                 headers={"PRIVATE-TOKEN": TOKEN, "Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req) as r:
            return json.load(r)
    except urllib.error.HTTPError as e:
        sys.exit(f"{method} {path}: HTTP {e.code} {e.read()[:300]!r}")


CLIENT = """// Profile API client used by the web UI.
export interface Profile {
  id: string;
  userName: string;
}

export async function loadProfile(id: string): Promise<Profile> {
  const res = await fetch(`/profiles/${encodeURIComponent(id)}`);
  if (!res.ok) {
    throw new Error(`profile ${id}: HTTP ${res.status}`);
  }
  return (await res.json()) as Profile;
}
"""

HEADER = """import { loadProfile } from "../api/profiles";

// Shows the signed-in user's name in the page header.
export async function renderHeader(el: HTMLElement, id: string): Promise<void> {
  const profile = await loadProfile(id);
  el.textContent = `Signed in as ${profile.userName}`;
}
"""

cmd = sys.argv[1] if len(sys.argv) > 1 else "setup"
default = api("GET", "")["default_branch"]
if cmd == "check":
    for ref in (default, BRANCH):
        hits = api("GET", f"/search?scope=blobs&search=userName&ref={urllib.parse.quote(ref)}")
        print(ref, "files mentioning userName:", sorted({h["path"] for h in hits}))
    sys.exit(0)

api("POST", f"/repository/branches?branch={BRANCH}&ref={urllib.parse.quote(default)}")
api("POST", "/repository/commits", {"branch": BRANCH, "commit_message": "Add the profile client and the page header",
                                    "actions": [
                                        {"action": "create", "file_path": "contract-v25/web/src/api/profiles.ts", "content": CLIENT},
                                        {"action": "create", "file_path": "contract-v25/web/src/ui/header.ts", "content": HEADER},
                                    ]})
print("branch:", f"{BASE}/nevzatcirak/rmcp-accept/-/tree/{BRANCH}")
