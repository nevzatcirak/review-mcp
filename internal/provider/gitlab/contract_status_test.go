package gitlab

// The pr_info, description-edit and CI part of the contract fake (WP-6d):
// the Spec's reviewers as GitLab reviewers and approvals, the PUT of the
// merge request, and the Spec's checks as commit statuses.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/contract"
)

// ctReviewers are the Spec's reviewers GitLab shows: it has no dismissal
// and review-mcp casts no review, so neither is served.
func (g *ctGitLab) ctReviewers() []contract.Reviewer {
	var out []contract.Reviewer
	for _, r := range g.pr.Reviewers {
		if !r.Dismissed && !r.Own {
			out = append(out, r)
		}
	}
	return out
}

// ctApprovedOnly marks the reviewers served through approved_by alone, not
// as requested reviewers: the stale verdict (GitLab has no staleness, so it
// is a plain approval).
func ctApprovedOnly(r contract.Reviewer) bool {
	return r.State == provider.ReviewApproved && r.Stale
}

var ctReviewerStates = map[provider.ReviewState]string{
	provider.ReviewApproved: "approved", provider.ReviewChangesRequested: "requested_changes",
	provider.ReviewCommented: "reviewed", provider.ReviewPending: "unreviewed",
}

// reviewerUsers is reviewers[] of the merge request.
func (g *ctGitLab) reviewerUsers() []any {
	out := []any{}
	for _, r := range g.ctReviewers() {
		if !ctApprovedOnly(r) {
			out = append(out, ctUser(r.User))
		}
	}
	return out
}

func (g *ctGitLab) reviewersJSON() []any {
	out := []any{}
	for _, r := range g.ctReviewers() {
		if !ctApprovedOnly(r) {
			state := ctReviewerStates[r.State]
			if s, ok := g.fx.reviewerStates[r.User.Login]; ok {
				state = s
			}
			out = append(out, map[string]any{"user": ctUser(r.User), "state": state,
				"created_at": "2026-01-02T03:04:05Z"})
		}
	}
	return out
}

func (g *ctGitLab) approvalsJSON() map[string]any {
	by := []any{}
	for _, r := range g.ctReviewers() {
		if r.State == provider.ReviewApproved {
			by = append(by, map[string]any{"user": ctUser(r.User)})
		}
	}
	m := map[string]any{"iid": 7, "approved_by": by}
	switch {
	case g.fx.omitRequired:
	case g.fx.required != nil:
		m["approvals_required"] = *g.fx.required
	default:
		m["approvals_required"] = 2
	}
	return m
}

// ctStatusValues are the commit status values the fake serves for each
// state; neutral is a failed job that may fail.
var ctStatusValues = map[provider.CheckState]string{
	provider.CheckSuccess: "success", provider.CheckFailure: "failed", provider.CheckPending: "running",
	provider.CheckNeutral: "failed", provider.CheckSkipped: "skipped", provider.CheckError: "canceled",
}

func (g *ctGitLab) statusesJSON() []any {
	items := []any{}
	if g.fx.statuses != nil {
		for _, s := range g.fx.statuses {
			items = append(items, s)
		}
		return items
	}
	for i, c := range g.pr.Checks {
		value := ctStatusValues[c.State]
		if c.Unknown {
			value = "brand_new_state"
		}
		item := map[string]any{"name": c.Name, "status": value, "target_url": c.URL,
			"allow_failure": c.State == provider.CheckNeutral && !c.Unknown}
		// The shared contract expects no URL for a check without one, and
		// the provider links the job page of a status that has an id
		// (TestCheckJobLinks), so such a status is served without an id.
		if c.URL != "" {
			item["id"] = i + 1
		}
		items = append(items, item)
	}
	return items
}

// serveStatus answers the reviewers, approvals, PUT and statuses requests;
// it reports whether the request was one of them.
func (g *ctGitLab) serveStatus(w http.ResponseWriter, r *http.Request, path string) bool {
	get := r.Method == http.MethodGet
	switch {
	case get && path == ctMR+"/reviewers":
		if s := g.fx.reviewersStatus; s != 0 {
			contract.WriteError(w, s)
			return true
		}
		ctJSON(w, http.StatusOK, g.reviewersJSON())
	case get && path == ctMR+"/approvals":
		if s := g.fx.approvalsStatus; s != 0 {
			contract.WriteError(w, s)
			return true
		}
		ctJSON(w, http.StatusOK, g.approvalsJSON())
	case r.Method == http.MethodPut && path == ctMR:
		g.servePut(w, r)
	case get && (strings.HasPrefix(path, ctProject+"/repository/commits/") || strings.HasPrefix(path, ctProjectByID+"/repository/commits/")) &&
		strings.HasSuffix(path, "/statuses"):
		sha, err := url.PathUnescape(strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(path, ctProjectByID), ctProject), "/statuses")[len("/repository/commits/"):])
		if err != nil || sha != g.pr.HeadSHA {
			contract.WriteError(w, http.StatusNotFound)
			return true
		}
		size := 100
		if g.fx.statusPage > 0 {
			size = g.fx.statusPage
		}
		g.ctPage(w, r, g.statusesJSON(), ctProjectByID+"/repository/commits/"+sha+"/statuses", size, "per_page=100&")
	default:
		return false
	}
	return true
}

// servePut applies the PUT of the merge request. Only title and description
// are accepted: any other field fails the test, as the provider must never
// name one.
func (g *ctGitLab) servePut(w http.ResponseWriter, r *http.Request) {
	var in map[string]json.RawMessage
	if r.Header.Get("Content-Type") != "application/json" {
		g.t.Errorf("PUT %s: Content-Type %q", r.URL.EscapedPath(), r.Header.Get("Content-Type"))
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		contract.WriteError(w, http.StatusBadRequest)
		return
	}
	for k, raw := range in {
		var v string
		if (k != "title" && k != "description") || json.Unmarshal(raw, &v) != nil {
			g.t.Errorf("PUT %s sends the field %q: only title and description may be sent", r.URL.EscapedPath(), k)
			contract.WriteError(w, http.StatusBadRequest)
			return
		}
		if k == "title" {
			g.pr.Title = v
		} else {
			g.pr.Description = v
		}
	}
	g.puts = append(g.puts, in)
	ctJSON(w, http.StatusOK, g.mrBody())
}

// putFields are the field names of the PUT bodies, sorted, one string each.
func (g *ctGitLab) putFields() []string {
	var out []string
	for _, p := range g.puts {
		var keys []string
		for k := range p {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		out = append(out, strings.Join(keys, ","))
	}
	return out
}
