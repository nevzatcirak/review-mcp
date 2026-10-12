package bitbucketserver

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/httpx"
)

// buildStatusPrefix is the legacy build status API.
const buildStatusPrefix = "/rest/build-status/1.0"

// CheckStrategies returns the provider.Checks.Strategy values this provider
// can produce (a fresh slice).
func CheckStrategies() []string {
	return []string{provider.ChecksBBSBuilds, provider.ChecksBBSBuildStatus}
}

// apiBuild is one build status of either build endpoint.
type apiBuild struct {
	State string `json:"state"`
	Key   string `json:"key"`
	Name  string `json:"name"`
	URL   string `json:"url"`
}

// mapBuildState maps a Bitbucket build state; UNKNOWN, and any value
// Bitbucket does not document, is neutral.
func mapBuildState(s string) provider.CheckState {
	switch strings.ToUpper(s) {
	case "SUCCESSFUL":
		return provider.CheckSuccess
	case "FAILED":
		return provider.CheckFailure
	case "INPROGRESS":
		return provider.CheckPending
	default: // UNKNOWN and anything unknown
		return provider.CheckNeutral
	}
}

// GetChecks implements provider.Provider: the repository-scoped build
// status (GET /rest/api/latest/projects/{p}/repos/{r}/commits/{sha}/builds),
// or, when that endpoint answers 404 (a server that predates it), the
// legacy build status (GET /rest/build-status/1.0/commits/{sha}). Both are
// paged and bounded by provider.MaxChecks; Checks.Strategy says which one
// answered. Any other failure of the first endpoint is returned as it is,
// as for the merge-base endpoint (computeBase). A build's name is its name,
// else its key.
func (p *Provider) GetChecks(ctx context.Context, ref provider.PRRef, sha string) (*provider.Checks, error) {
	rp, err := repoPath(apiLatest, ref)
	if err != nil {
		return nil, err
	}
	if !provider.ValidRevision(sha) {
		return nil, protocolErr("unusable revision")
	}
	if err := p.ensureSupported(ctx); err != nil {
		return nil, err
	}
	commit := "/commits/" + url.PathEscape(sha)
	strategy := provider.ChecksBBSBuilds
	builds, truncated, err := p.builds(ctx, rp+commit+"/builds")
	if errors.Is(err, provider.ErrNotFound) {
		p.logger.Debug("repository build status endpoint not available; using the legacy build status")
		strategy = provider.ChecksBBSBuildStatus
		builds, truncated, err = p.builds(ctx, buildStatusPrefix+commit)
	}
	if err != nil {
		return nil, err
	}
	p.logger.Debug("bitbucket server checks read", "strategy", strategy)
	out := &provider.Checks{Truncated: truncated, Strategy: strategy}
	for _, b := range builds {
		name := b.Name
		if strings.TrimSpace(name) == "" {
			name = b.Key
		}
		out.Items = append(out.Items, provider.NewCheck(name, mapBuildState(b.State), b.URL))
	}
	return out, nil
}

// builds reads one paged build status list.
func (p *Provider) builds(ctx context.Context, path string) ([]apiBuild, bool, error) {
	return httpx.PagesStartLimitCapped(ctx, p.client, path, pageLimit, httpx.Cap{Items: provider.MaxChecks},
		func(pg *httpx.StartLimitPage[apiBuild]) *httpx.StartLimitPage[apiBuild] { return pg })
}

// GetFileAt implements provider.Provider through GetDiff's raw content
// request (GET .../raw/{path}?at={rev}).
func (p *Provider) GetFileAt(ctx context.Context, ref provider.PRRef, path, rev string) ([]byte, error) {
	rp, err := repoPath(apiV1, ref)
	if err != nil {
		return nil, err
	}
	if err := p.ensureSupported(ctx); err != nil {
		return nil, err
	}
	return p.getRaw(ctx, rp, path, rev)
}

// apiBrowseChild is one child of a browse response. Bitbucket may collapse
// a chain of single-child directories into one child with several path
// components; its first component is then a directory.
type apiBrowseChild struct {
	Path struct {
		Components []string `json:"components"`
		Name       string   `json:"name"`
	} `json:"path"`
	Type string `json:"type"`
}

// ListDir implements provider.Provider: GET .../browse/{dir}?at={rev}
// (/browse for the root), whose children are paged; the walk stops past
// provider.MaxDirEntries. A path that is a file answers without children,
// which is a protocol error.
func (p *Provider) ListDir(ctx context.Context, ref provider.PRRef, dir, rev string) (*provider.DirListing, error) {
	rp, err := repoPath(apiV1, ref)
	if err != nil {
		return nil, err
	}
	if !provider.ValidRevision(rev) || (dir != "" && !validRawPath(dir)) {
		return nil, protocolErr("unusable path or revision")
	}
	if err := p.ensureSupported(ctx); err != nil {
		return nil, err
	}
	path := rp + "/browse"
	if dir != "" {
		path += "/" + escapePath(dir)
	}
	type browse struct {
		Children *httpx.StartLimitPage[apiBrowseChild] `json:"children"`
	}
	children, truncated, err := httpx.PagesStartLimitCapped(ctx, p.client, path+"?at="+url.QueryEscape(rev), pageLimit,
		httpx.Cap{Items: provider.MaxDirEntries}, func(b *browse) *httpx.StartLimitPage[apiBrowseChild] { return b.Children })
	if err != nil {
		return nil, err
	}
	entries := make([]provider.DirEntry, 0, len(children))
	for _, c := range children {
		name, collapsed := c.Path.Name, false
		if comps := c.Path.Components; len(comps) > 0 {
			name, collapsed = comps[0], len(comps) > 1
		}
		entries = append(entries, provider.DirEntry{Name: name, Dir: collapsed || strings.EqualFold(c.Type, "DIRECTORY")})
	}
	return provider.FinishDirListing(entries, truncated), nil
}
