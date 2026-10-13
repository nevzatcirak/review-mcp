package github

import (
	"context"
	"errors"
	"net/url"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/httpx"
)

// checksPageCap bounds each of the two check lists (100 per page); the walk
// stops earlier, past provider.MaxChecks items.
const checksPageCap = 10

// CheckStrategies returns the provider.Checks.Strategy values this provider
// can produce (a fresh slice).
func CheckStrategies() []string {
	return []string{provider.ChecksGitHubRunsAndStatuses}
}

// apiCheckRun is one entry of GET /commits/{sha}/check-runs.
type apiCheckRun struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	HTMLURL    string `json:"html_url"`
	DetailsURL string `json:"details_url"`
}

// apiCommitStatus is one entry of the combined status's statuses.
type apiCommitStatus struct {
	Context   string `json:"context"`
	State     string `json:"state"`
	TargetURL string `json:"target_url"`
}

// mapCheckRun maps a check run: a run that has not completed is pending;
// a completed one maps its conclusion (cancelled is error, timed_out
// failure, action_required pending, stale neutral). Any value GitHub does
// not document is neutral.
func mapCheckRun(status, conclusion string) provider.CheckState {
	switch status {
	case "queued", "in_progress", "waiting", "requested", "pending":
		return provider.CheckPending
	case "completed":
	default:
		return provider.CheckNeutral
	}
	switch conclusion {
	case "success":
		return provider.CheckSuccess
	case "failure", "timed_out":
		return provider.CheckFailure
	case "cancelled":
		return provider.CheckError
	case "skipped":
		return provider.CheckSkipped
	case "action_required":
		return provider.CheckPending
	default: // neutral, stale and anything unknown
		return provider.CheckNeutral
	}
}

// mapCommitStatus maps a legacy commit status state; any value GitHub does
// not document is neutral.
func mapCommitStatus(s string) provider.CheckState {
	switch s {
	case "success":
		return provider.CheckSuccess
	case "failure":
		return provider.CheckFailure
	case "error":
		return provider.CheckError
	case "pending":
		return provider.CheckPending
	default:
		return provider.CheckNeutral
	}
}

// GetChecks implements provider.Provider: the check runs of sha (GET
// /commits/{sha}/check-runs) and its legacy statuses (GET
// /commits/{sha}/status), both Link-paged, merged by name; a check run wins
// over a status of the same name. Each list is bounded by checksPageCap
// pages and provider.MaxChecks items, and so is the merged result. The two
// lists need different permissions of a fine-grained token (Checks and
// Commit statuses), so when exactly one of them fails with auth or
// not_found the other is returned with Checks.Partial set. Any other
// failure, or both lists failing, fails the call. GitHub cannot say whether
// a check is required without more requests, so Required stays nil.
func (p *Provider) GetChecks(ctx context.Context, ref provider.PRRef, sha string) (*provider.Checks, error) {
	rp, err := repoPath(ref)
	if err != nil {
		return nil, err
	}
	if !provider.ValidRevision(sha) {
		return nil, protocolErr("unusable revision")
	}
	commit := rp + "/commits/" + url.PathEscape(sha)
	cp := httpx.Cap{Pages: checksPageCap, Items: provider.MaxChecks}
	type runsPage struct {
		CheckRuns []apiCheckRun `json:"check_runs"`
	}
	runs, runsCut, runsErr := httpx.PagesByLinkCapped(ctx, p.client, p.fetchPage, listPath(commit+"/check-runs"), cp,
		func(pg *runsPage) []apiCheckRun { return pg.CheckRuns })
	if runsErr != nil && !permissionLike(runsErr) {
		return nil, runsErr
	}
	type statusPage struct {
		Statuses []apiCommitStatus `json:"statuses"`
	}
	sts, stsCut, stsErr := httpx.PagesByLinkCapped(ctx, p.client, p.fetchPage, listPath(commit+"/status"), cp,
		func(pg *statusPage) []apiCommitStatus { return pg.Statuses })
	switch {
	case runsErr != nil && stsErr != nil:
		return nil, runsErr
	case stsErr != nil && !permissionLike(stsErr):
		return nil, stsErr
	case runsErr != nil:
		runs, runsCut = nil, false
		p.logger.Debug("github checks: check runs not readable; using the commit statuses only", "class", errClass(runsErr))
	case stsErr != nil:
		sts, stsCut = nil, false
		p.logger.Debug("github checks: commit statuses not readable; using the check runs only", "class", errClass(stsErr))
	}

	out := &provider.Checks{Truncated: runsCut || stsCut, Partial: runsErr != nil || stsErr != nil,
		Strategy: provider.ChecksGitHubRunsAndStatuses}
	seen := make(map[string]bool, len(runs))
	for _, r := range runs {
		link := r.HTMLURL
		if provider.CleanCheckURL(link) == "" {
			link = r.DetailsURL
		}
		c := provider.NewCheck(r.Name, mapCheckRun(r.Status, r.Conclusion), link)
		seen[c.Name] = true
		out.Items = append(out.Items, c)
	}
	for _, s := range sts {
		c := provider.NewCheck(s.Context, mapCommitStatus(s.State), s.TargetURL)
		if seen[c.Name] {
			continue
		}
		seen[c.Name] = true
		out.Items = append(out.Items, c)
	}
	if len(out.Items) > provider.MaxChecks {
		out.Items, out.Truncated = out.Items[:provider.MaxChecks], true
	}
	p.logger.Debug("github checks read", "check_runs", len(runs), "statuses", len(sts), "truncated", out.Truncated,
		"partial", out.Partial)
	return out, nil
}

// permissionLike reports a failure of one check list that a token without
// that list's permission produces (auth or not_found), as opposed to a
// failure of the host or the transport.
func permissionLike(err error) bool {
	return errors.Is(err, provider.ErrAuth) || errors.Is(err, provider.ErrNotFound)
}

// GetFileAt implements provider.Provider through GetDiff's content request
// (GET /contents/{path}?ref={rev} with the raw media type).
func (p *Provider) GetFileAt(ctx context.Context, ref provider.PRRef, path, rev string) ([]byte, error) {
	rp, err := repoPath(ref)
	if err != nil {
		return nil, err
	}
	return p.getRaw(ctx, rp, path, rev)
}

// ListDir implements provider.Provider: GET /contents/{dir}?ref={rev}
// (/contents for the root) with the JSON media type. GitHub lists a
// directory in one response (up to 1000 entries); a path that is a file
// answers with an object, which is a protocol error.
func (p *Provider) ListDir(ctx context.Context, ref provider.PRRef, dir, rev string) (*provider.DirListing, error) {
	rp, err := repoPath(ref)
	if err != nil {
		return nil, err
	}
	if !provider.ValidRevision(rev) || (dir != "" && !validContentPath(dir)) {
		return nil, protocolErr("unusable path or revision")
	}
	path := rp + "/contents"
	if dir != "" {
		path += "/" + escapePath(dir)
	}
	var in []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	if err := p.getJSON(ctx, path+"?ref="+url.QueryEscape(rev), &in); err != nil {
		return nil, err
	}
	entries := make([]provider.DirEntry, 0, len(in))
	for _, e := range in {
		entries = append(entries, provider.DirEntry{Name: e.Name, Dir: e.Type == "dir"})
	}
	return provider.FinishDirListing(entries, false), nil
}
