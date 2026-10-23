#!/usr/bin/env python3
"""V-4 acceptance Q4: a GitLab merge request pipeline on the throwaway project.

Usage: gl26setup.py vars | setup | status MR_IID | close MR_IID | unvars

vars    copies four values from YOUR environment into masked project CI/CD
        variables (never printed): REVIEW_MCP_GITLAB_TOKEN,
        REVIEW_MCP_LLM_BASE_URL, REVIEW_MCP_LLM_MODEL, REVIEW_MCP_LLM_API_KEY.
setup   creates branch accept-v26-base (a small Go file) and accept-v26-gl
        (a change plus .gitlab-ci.yml) and a merge request between them.
status  prints the latest pipeline and job states of the MR and how many
        notes review-mcp posted.
close   closes the MR.  unvars deletes the four CI/CD variables.
The token is read from REVIEW_MCP_GITLAB_TOKEN and only sent as a header.
"""
import json, os, sys, urllib.error, urllib.parse, urllib.request

BASE = os.environ.get("REVIEW_MCP_GITLAB_BASE_URL", "https://gitlab.com").rstrip("/")
PROJECT = urllib.parse.quote(os.environ.get("RMCP_GL_PROJECT", "nevzatcirak/rmcp-accept"), safe="")
TOKEN = os.environ["REVIEW_MCP_GITLAB_TOKEN"]
VARS = ["REVIEW_MCP_GITLAB_TOKEN", "REVIEW_MCP_LLM_BASE_URL", "REVIEW_MCP_LLM_MODEL", "REVIEW_MCP_LLM_API_KEY"]


def api(method, path, body=None, ok404=False):
    req = urllib.request.Request(f"{BASE}/api/v4/projects/{PROJECT}{path}", method=method,
                                 data=json.dumps(body).encode() if body is not None else None,
                                 headers={"PRIVATE-TOKEN": TOKEN, "Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req) as r:
            data = r.read()
            return json.loads(data) if data else {}
    except urllib.error.HTTPError as e:
        if ok404 and e.code == 404:
            return None
        sys.exit(f"{method} {path.split('?')[0]}: HTTP {e.code} {e.read()[:300]!r}")


STORE = """package acceptv26

// Order is one order of the shop.
type Order struct {
	ID     string
	Amount int
}

// Store keeps orders in memory.
type Store struct {
	orders map[string]*Order
}

// Find returns the order with the given ID, or nil when it is not known.
func (s *Store) Find(id string) *Order {
	return s.orders[id]
}
"""

CHANGE = """package acceptv26

// Average returns the mean amount of the given orders.
func Average(orders []*Order) int {
	sum := 0
	for _, o := range orders {
		sum += o.Amount
	}
	return sum / len(orders)
}
"""

CI = """# Acceptance fixture for review-mcp 2.6.0-rc.1 (V-4 CI mode, Q4).
review:
  image: node:22
  rules:
    - if: $CI_PIPELINE_SOURCE == "merge_request_event"
  variables:
    GIT_STRATEGY: none
    REVIEW_MCP_GITLAB_BASE_URL: $CI_SERVER_URL
    REVIEW_MCP_LLM_REQUESTS_PER_MINUTE: "10"
  script:
    - npx -y @nevzatcirak/review-mcp@2.6.0-rc.1 ci review --publish --incremental
"""

cmd = sys.argv[1] if len(sys.argv) > 1 else ""
if cmd == "vars":
    for k in VARS:
        v = os.environ.get(k, "")
        if not v:
            sys.exit(f"{k} is not set in your environment")
        api("DELETE", f"/variables/{k}", ok404=True)
        masked = k in ("REVIEW_MCP_GITLAB_TOKEN", "REVIEW_MCP_LLM_API_KEY")
        api("POST", "/variables", {"key": k, "value": v, "masked": masked, "protected": False})
        print("set", k, "(masked)" if masked else "")
elif cmd == "unvars":
    for k in VARS:
        api("DELETE", f"/variables/{k}", ok404=True)
        print("deleted", k)
elif cmd == "setup":
    default = api("GET", "")["default_branch"]
    api("POST", f"/repository/branches?branch=accept-v26-base&ref={urllib.parse.quote(default)}")
    api("POST", "/repository/commits", {"branch": "accept-v26-base", "commit_message": "Add the order store",
                                        "actions": [{"action": "create", "file_path": "ci-v26/store.go", "content": STORE}]})
    api("POST", "/repository/branches?branch=accept-v26-gl&ref=accept-v26-base")
    api("POST", "/repository/commits", {"branch": "accept-v26-gl", "commit_message": "Add the average order amount",
                                        "actions": [{"action": "create", "file_path": "ci-v26/average.go", "content": CHANGE},
                                                    {"action": "create", "file_path": ".gitlab-ci.yml", "content": CI}]})
    mr = api("POST", "/merge_requests", {"source_branch": "accept-v26-gl", "target_branch": "accept-v26-base",
                                         "title": "Add the average order amount",
                                         "description": "Acceptance fixture for review-mcp 2.6.0-rc.1; it will be closed without merging."})
    print("MR:", mr["web_url"], "iid", mr["iid"])
elif cmd == "status":
    iid = sys.argv[2]
    pls = api("GET", f"/merge_requests/{iid}/pipelines")
    if not pls:
        print("no pipeline yet")
    else:
        p = pls[0]
        print("pipeline", p["id"], p["status"], p.get("source"))
        for j in api("GET", f"/pipelines/{p['id']}/jobs"):
            print("  job", j["name"], j["status"], j.get("failure_reason", ""))
    notes = api("GET", f"/merge_requests/{iid}/notes?per_page=100")
    print("notes:", len(notes), "| with review-mcp marker:", sum("review-mcp:overview" in n.get("body", "") for n in notes))
elif cmd == "close":
    api("PUT", f"/merge_requests/{sys.argv[2]}", {"state_event": "close"})
    print("closed")
else:
    sys.exit(__doc__)
