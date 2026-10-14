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
		out.Items = append(out.Items, provider.NewCheck(s.Name, mapCheckState(s.Status, s.AllowFailure), s.TargetURL))
	}
	return out, nil
}
