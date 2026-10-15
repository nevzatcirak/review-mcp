package gitlab

import (
	"context"
	"net/url"
	"strconv"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/httpx"
)

// statusesPageCap bounds the commit statuses read (perPage items per
// page); provider.MaxChecks cuts the list first.
const statusesPageCap = 10

// apiCommitStatus is an entry of GET .../repository/commits/{sha}/statuses.
type apiCommitStatus struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Status       string `json:"status"`
	AllowFailure bool   `json:"allow_failure"`
	TargetURL    string `json:"target_url"`
}

// mapCheckState maps a commit status (spec 2C §4 item 3): success is
// success; failed is failure, or neutral when the job may fail; canceled is
// error; skipped is skipped; manual is neutral when the job may fail, else
// pending (the pipeline waits for it); created, waiting_for_resource,
// preparing, pending, running and scheduled are pending; anything else is
// neutral.
func mapCheckState(status string, allowFailure bool) provider.CheckState {
	switch status {
	case "success":
		return provider.CheckSuccess
	case "failed":
		if allowFailure {
			return provider.CheckNeutral
		}
		return provider.CheckFailure
	case "canceled":
		return provider.CheckError
	case "skipped":
		return provider.CheckSkipped
	case "manual":
		if allowFailure {
			return provider.CheckNeutral
		}
		return provider.CheckPending
	case "created", "waiting_for_resource", "preparing", "pending", "running", "scheduled":
		return provider.CheckPending
	}
	return provider.CheckNeutral
}

// GetChecks implements provider.Provider: the commit statuses of sha in the
// merge request's project (GET /projects/{id}/repository/commits/{sha}/
// statuses), Link-paged and bounded by statusesPageCap and
// provider.MaxChecks. GitLab returns the latest status of each name. The
// pipelines of merged results and of a fork's project are not read.
func (p *Provider) GetChecks(ctx context.Context, ref provider.PRRef, sha string) (*provider.Checks, error) {
	pp, err := projectPath(ref)
	if err != nil {
		return nil, err
	}
	if !provider.ValidRevision(sha) {
		return nil, protocolErr("unusable revision")
	}
	sts, truncated, err := httpx.PagesByLinkCapped(ctx, p.client, p.fetchPage,
		pp+"/repository/commits/"+url.PathEscape(sha)+"/statuses?per_page="+strconv.Itoa(perPage),
		httpx.Cap{Pages: statusesPageCap, Items: provider.MaxChecks},
		func(pg *[]apiCommitStatus) []apiCommitStatus { return *pg })
	if err != nil {
		return nil, err
	}
	out := &provider.Checks{Truncated: truncated, Strategy: provider.ChecksGitLabCommitStatuses}
	for _, s := range sts {
		out.Items = append(out.Items, provider.NewCheck(s.Name, mapCheckState(s.Status, s.AllowFailure), p.statusURL(ref, s)))
	}
	return out, nil
}

// statusURL is the link of a commit status: its target_url, or, when it has
// none and an id, the job page {web base}/{project path}/-/jobs/{id}. A
// status without either has no link: no URL is guessed.
//
// DESIGN-QUESTION (WP-8a item 2): the statuses API does not say whether a
// status is a CI job or one an external service posted, and both have an id.
// Every status with an id and no target_url is linked as a job; an external
// status without a target_url would then link a job page that does not
// exist, or another job's. The spec says "a job status with an id"; no
// field of the response tells a job apart, so the id is the only test.
// Recommendation: keep; ask for a stricter test only if a real instance
// shows external statuses without a target_url.
func (p *Provider) statusURL(ref provider.PRRef, s apiCommitStatus) string {
	if s.TargetURL != "" || s.ID <= 0 {
		return s.TargetURL
	}
	return p.webURL + "/" + provider.EscapeNamespace(ref.Namespace) + "/" + url.PathEscape(ref.Repo) +
		"/-/jobs/" + strconv.FormatInt(s.ID, 10)
}
