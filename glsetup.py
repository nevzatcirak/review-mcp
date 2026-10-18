#!/usr/bin/env python3
"""Set up the V-13a acceptance fixture on a GitLab project (throwaway).

Usage: glsetup.py setup | bump | notes MR_IID | close MR_IID

setup creates branch accept-v23-base (adds contract-v23/openapi.yaml with userName),
branch accept-v23-rename (renames the field to displayName and adds three
unrelated files), and a merge request from the second into the first.
The token is read from REVIEW_MCP_GITLAB_TOKEN and only sent as a header.
"""
import json, os, sys, urllib.parse, urllib.request

BASE = os.environ.get("REVIEW_MCP_GITLAB_BASE_URL", "https://gitlab.com").rstrip("/")
CMD = sys.argv[1] if len(sys.argv) > 1 else "setup"
PROJECT = urllib.parse.quote(os.environ.get("RMCP_GL_PROJECT", "nevzatcirak/rmcp-accept"), safe="")
TOKEN = os.environ["REVIEW_MCP_GITLAB_TOKEN"]


def api(method, path, body=None):
    req = urllib.request.Request(f"{BASE}/api/v4/projects/{PROJECT}{path}", method=method,
                                 data=json.dumps(body).encode() if body is not None else None,
                                 headers={"PRIVATE-TOKEN": TOKEN, "Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req) as r:
            return json.load(r)
    except urllib.error.HTTPError as e:
        sys.exit(f"{method} {path}: HTTP {e.code} {e.read()[:300]!r}")


def openapi(field):
    return f"""openapi: 3.0.3
info:
  title: Profile service
  version: 1.0.0
paths:
  /users/{{id}}:
    get:
      operationId: getUser
      parameters:
        - name: id
          in: path
          required: true
          schema:
            type: string
      responses:
        "200":
          description: One user.
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/User"
components:
  schemas:
    User:
      type: object
      required: [id, {field}]
      properties:
        id:
          type: string
        {field}:
          type: string
          description: The name shown for the user.
"""


def filler(kind, n):
    if kind == "css":
        return "".join(f".theme-block-{i} {{\n  margin: {i % 7}px;\n  padding: {i % 5}px;\n  color: #{i * 2654435761 % 0xffffff:06x};\n}}\n\n" for i in range(n))
    if kind == "py":
        return '"""Seed data for local runs."""\n\nROWS = [\n' + "".join(f'    {{"id": "u{i}", "city": "City {i % 37}", "score": {i * 7 % 101}}},\n' for i in range(n)) + "]\n"
    return "# Release checklist\n\n" + "".join(f"- Step {i}: check item {i} of the list and write down the result.\n" for i in range(n))


if CMD == "bump":
    api("POST", "/repository/commits", {"branch": "accept-v23-rename", "commit_message": "Add one more checklist step",
                                        "actions": [{"action": "update", "file_path": "contract-v23/notes/checklist.md",
                                                     "content": filler("md", 301)}]})
    print("pushed a new commit to accept-v23-rename")
    sys.exit(0)
if CMD == "notes":
    notes = api("GET", f"/merge_requests/{sys.argv[2]}/notes?per_page=100")
    print("notes on the MR:", len(notes), "| by review-mcp:", sum("review-mcp" in n.get("body", "") for n in notes))
    sys.exit(0)
if CMD == "close":
    api("PUT", f"/merge_requests/{sys.argv[2]}", {"state_event": "close"})
    print("closed")
    sys.exit(0)

default = api("GET", "")["default_branch"]
for branch, ref in (("accept-v23-base", default), ("accept-v23-rename", None)):
    if ref is None:
        api("POST", "/repository/commits", {"branch": "accept-v23-base", "commit_message": "Add the profile service contract",
                                            "actions": [{"action": "create", "file_path": "contract-v23/openapi.yaml", "content": openapi("userName")}]})
        ref = "accept-v23-base"
    api("POST", f"/repository/branches?branch={branch}&ref={urllib.parse.quote(ref)}")
api("POST", "/repository/commits", {"branch": "accept-v23-rename", "commit_message": "Rename the user name field and tidy the project",
                                    "actions": [
                                        {"action": "update", "file_path": "contract-v23/openapi.yaml", "content": openapi("displayName")},
                                        {"action": "create", "file_path": "contract-v23/web/theme.css", "content": filler("css", 400)},
                                        {"action": "create", "file_path": "contract-v23/scripts/seed_data.py", "content": filler("py", 600)},
                                        {"action": "create", "file_path": "contract-v23/notes/checklist.md", "content": filler("md", 300)},
                                    ]})
mr = api("POST", "/merge_requests", {"source_branch": "accept-v23-rename", "target_branch": "accept-v23-base",
                                     "title": "Rename the user name field",
                                     "description": "Acceptance fixture for review-mcp 2.3.0-rc.1; it will be closed without merging."})
print("MR:", mr["web_url"])
