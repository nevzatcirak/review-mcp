package gitea

import (
	"context"
	"net/url"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/httpx"
)

// CheckStrategies returns the provider.Checks.Strategy values this provider
// can produce (a fresh slice).
func CheckStrategies() []string {
	return []string{provider.ChecksGiteaCombined}
}

// apiStatus is one entry of a combined status's statuses.
type apiStatus struct {
	Context   string `json:"context"`
	Status    string `json:"status"`
	TargetURL string `json:"target_url"`
}

// mapCheckState maps a Gitea commit status state; warning is neutral, and
// so is any value Gitea does not document.
func mapCheckState(s string) provider.CheckState {
	switch strings.ToLower(s) {
	case "success":
		return provider.CheckSuccess
	case "failure":
		return provider.CheckFailure
	case "error":
		return provider.CheckError
	case "pending":
		return provider.CheckPending
	default: // warning and anything unknown
		return provider.CheckNeutral
	}
}

// GetChecks implements provider.Provider: the combined status of sha (GET
// /repos/{o}/{r}/commits/{sha}/status), paged until an empty page and
// bounded by provider.MaxChecks.
func (p *Provider) GetChecks(ctx context.Context, ref provider.PRRef, sha string) (*provider.Checks, error) {
	rp, err := repoPath(ref)
	if err != nil {
		return nil, err
	}
	if !provider.ValidRevision(sha) {
		return nil, protocolErr("unusable revision")
	}
	type combined struct {
		Statuses []apiStatus `json:"statuses"`
	}
	sts, truncated, err := httpx.PagesUntilEmptyCapped(ctx, p.client, rp+"/commits/"+url.PathEscape(sha)+"/status", pageLimit,
		httpx.Cap{Items: provider.MaxChecks}, func(c *combined) []apiStatus { return c.Statuses })
	if err != nil {
		return nil, err
	}
	out := &provider.Checks{Truncated: truncated, Strategy: provider.ChecksGiteaCombined}
	for _, s := range sts {
		out.Items = append(out.Items, provider.NewCheck(s.Context, mapCheckState(s.Status), s.TargetURL))
	}
	return out, nil
}

// GetFileAt implements provider.Provider through GetDiff's raw content
// request (GET /repos/{o}/{r}/raw/{path}?ref={rev}).
func (p *Provider) GetFileAt(ctx context.Context, ref provider.PRRef, path, rev string) ([]byte, error) {
	rp, err := repoPath(ref)
	if err != nil {
		return nil, err
	}
	return p.getRaw(ctx, rp, path, rev)
}

// ListDir implements provider.Provider: GET /repos/{o}/{r}/contents/{dir}
// ?ref={rev} (/contents for the root). Gitea lists a directory in one
// response; a path that is a file answers with an object, which is a
// protocol error.
func (p *Provider) ListDir(ctx context.Context, ref provider.PRRef, dir, rev string) (*provider.DirListing, error) {
	rp, err := repoPath(ref)
	if err != nil {
		return nil, err
	}
	if !provider.ValidRevision(rev) || (dir != "" && !validRawPath(dir)) {
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
	if err := p.client.GetJSON(ctx, path+"?ref="+url.QueryEscape(rev), &in); err != nil {
		return nil, err
	}
	entries := make([]provider.DirEntry, 0, len(in))
	for _, e := range in {
		entries = append(entries, provider.DirEntry{Name: e.Name, Dir: e.Type == "dir"})
	}
	return provider.FinishDirListing(entries, false), nil
}
