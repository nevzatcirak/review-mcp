package gitlab

import (
	"context"
	"net/url"
	"strconv"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/httpx"
)

// GetFileAt implements provider.Provider through GetDiff's content request
// (GET /repository/files/{path}/raw?ref={rev}).
func (p *Provider) GetFileAt(ctx context.Context, ref provider.PRRef, path, rev string) ([]byte, error) {
	pp, err := projectPath(ref)
	if err != nil {
		return nil, err
	}
	return p.getRaw(ctx, pp, path, rev)
}

// apiTreeEntry is one entry of GET /repository/tree.
type apiTreeEntry struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// ListDir implements provider.Provider: GET /repository/tree?path={dir}
// &ref={rev} (no path for the root), with keyset pagination followed
// through the Link header, read up to provider.MaxDirEntries entries. A
// tree entry is a directory; a blob or a commit (a submodule) is not. A
// directory that does not exist is a 404 (not_found). GitLab lists a path
// that is a file as an empty tree; since git has no empty directories, an
// empty listing below the root is a protocol error.
func (p *Provider) ListDir(ctx context.Context, ref provider.PRRef, dir, rev string) (*provider.DirListing, error) {
	pp, err := projectPath(ref)
	if err != nil {
		return nil, err
	}
	if !provider.ValidRevision(rev) || (dir != "" && !validContentPath(dir)) {
		return nil, protocolErr("unusable path or revision")
	}
	q := "?ref=" + url.QueryEscape(rev) + "&per_page=" + strconv.Itoa(perPage) + "&pagination=keyset"
	if dir != "" {
		q += "&path=" + url.QueryEscape(dir)
	}
	in, truncated, err := httpx.PagesByLinkCapped(ctx, p.client, p.fetchPage, pp+"/repository/tree"+q,
		httpx.Cap{Pages: treePageCap, Items: provider.MaxDirEntries}, func(pg *[]apiTreeEntry) []apiTreeEntry { return *pg })
	if err != nil {
		return nil, err
	}
	if dir != "" && len(in) == 0 {
		return nil, protocolErr("the path is not a directory")
	}
	entries := make([]provider.DirEntry, 0, len(in))
	for _, e := range in {
		entries = append(entries, provider.DirEntry{Name: e.Name, Dir: e.Type == "tree"})
	}
	return provider.FinishDirListing(entries, truncated), nil
}
