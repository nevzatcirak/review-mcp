// Package bitbucketserver implements the provider.Provider interface for
// Bitbucket Server / Data Center (7.0 and later) over the REST API 1.0.
//
// All API calls go to {base_url}/rest/api/1.0 (the merge-base endpoint uses
// /rest/api/latest). The base URL includes the context path and comes only
// from configuration. Bitbucket Server cannot produce a patch without file
// contents, so GetDiff fetches the base and head content of every changed
// file and generates the unified diff locally.
package bitbucketserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/httpx"
	"github.com/nevzatcirak/review-mcp/internal/version"
)

const (
	apiV1     = "/rest/api/1.0"
	apiLatest = "/rest/api/latest"

	// pageLimit is the page size for paged resources: it is below the
	// server-side maximum (1000) and keeps page counts low.
	pageLimit = 100

	// fetchConcurrency bounds parallel file processing; each worker issues
	// its raw-content requests sequentially, so at most this many requests
	// are in flight.
	fetchConcurrency = 4

	// minMajorVersion is the oldest supported server major version (DQ-20).
	minMajorVersion = 7

	capKeyFile = "diff.max_file_bytes"
)

// Factory builds Bitbucket Server providers and parses Bitbucket Server PR
// paths. It implements provider.Factory.
type Factory struct{}

// NewFactory returns the Bitbucket Server factory to hand to
// provider.NewResolver.
func NewFactory() Factory { return Factory{} }

// Kind implements provider.Factory.
func (Factory) Kind() provider.Kind { return provider.KindBitbucketServer }

// ParsePRPath implements provider.Factory. remainder is escaped and starts
// with "/"; it must be /projects/{KEY}/repos/{slug}/pull-requests/{id} or
// /users/{user}/repos/{slug}/pull-requests/{id} (namespace "~{user}"),
// optionally followed by more segments.
func (Factory) ParsePRPath(remainder string) (namespace, repo string, number int64, err error) {
	errShape := errors.New("not a Bitbucket Server pull request path")
	if !strings.HasPrefix(remainder, "/") {
		return "", "", 0, errShape
	}
	raw := strings.Split(remainder[1:], "/")
	if len(raw) < 6 {
		return "", "", 0, errShape
	}
	segs := make([]string, 6)
	for i := range segs {
		s, uerr := url.PathUnescape(raw[i])
		if uerr != nil || s == "" || isDots(s) {
			return "", "", 0, errShape
		}
		segs[i] = s
	}
	if segs[2] != "repos" || segs[4] != "pull-requests" {
		return "", "", 0, errShape
	}
	switch segs[0] {
	case "projects":
		namespace = segs[1]
	case "users":
		namespace = "~" + segs[1]
	default:
		return "", "", 0, errShape
	}
	n, ok := parsePositive(segs[5])
	if !ok {
		return "", "", 0, errShape
	}
	return namespace, segs[3], n, nil
}

func isDots(s string) bool { return s == "." || s == ".." }

// parsePositive parses a positive base-10 integer made of ASCII digits only
// (no sign, no "+").
func parsePositive(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// New implements provider.Factory.
func (Factory) New(cfg *config.Config, logger *slog.Logger) (provider.Provider, error) {
	if cfg == nil || cfg.BitbucketServer.BaseURL == "" {
		return nil, &provider.Error{Class: provider.ClassURLNotConfigured, Hint: "bitbucket_server.base_url"}
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	// The Secret value type is captured, not its revealed string: Reveal is
	// called inside the closure for every request.
	token := cfg.Secrets.BitbucketServerToken
	c, err := httpx.New(httpx.Options{
		BaseURL:            cfg.BitbucketServer.BaseURL,
		Auth:               func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token.Reveal()) },
		CACertPath:         cfg.BitbucketServer.CACert,
		InsecureSkipVerify: cfg.BitbucketServer.InsecureSkipVerify,
		UserAgent:          "review-mcp/" + version.Info().Version,
		Logger:             logger,
	})
	if err != nil {
		return nil, err
	}
	return &Provider{
		client:   c,
		logger:   logger,
		baseURL:  strings.TrimRight(cfg.BitbucketServer.BaseURL, "/"),
		maxFiles: cfg.Diff.MaxFilesFullContent,
		maxFile:  int64(cfg.Diff.MaxFileBytes),
	}, nil
}

// Provider is a request-scoped Bitbucket Server provider. It holds no
// revealed secret.
type Provider struct {
	client   *httpx.Client
	logger   *slog.Logger
	baseURL  string
	maxFiles int
	maxFile  int64

	probeMu  sync.Mutex
	probed   bool
	probeErr error
}

var _ provider.Provider = (*Provider)(nil)

// Kind implements provider.Provider.
func (*Provider) Kind() provider.Kind { return provider.KindBitbucketServer }

// Capabilities implements provider.Provider.
func (*Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{GFM: false, MarkdownTables: true, Labels: false, InlineComments: true}
}

func protocolErr(hint string) *provider.Error {
	return &provider.Error{Class: provider.ClassProtocol, Hint: hint}
}

// repoPath returns "{prefix}/projects/{K}/repos/{s}" built from escaped
// segments. A personal project key "~user" keeps its "~" under projects/.
func repoPath(prefix string, ref provider.PRRef) (string, error) {
	if ref.Namespace == "" || ref.Namespace == "~" || ref.Repo == "" || isDots(ref.Namespace) || isDots(ref.Repo) {
		return "", protocolErr("invalid repository reference")
	}
	if ref.Number <= 0 {
		return "", protocolErr("invalid pull request number")
	}
	return prefix + "/projects/" + url.PathEscape(ref.Namespace) + "/repos/" + url.PathEscape(ref.Repo), nil
}

func prPath(ref provider.PRRef) (string, error) {
	rp, err := repoPath(apiV1, ref)
	if err != nil {
		return "", err
	}
	return rp + "/pull-requests/" + strconv.FormatInt(ref.Number, 10), nil
}

type apiRef struct {
	DisplayID    string `json:"displayId"`
	LatestCommit string `json:"latestCommit"`
}

type apiPR struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	State       string `json:"state"`
	Author      struct {
		User struct {
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
		} `json:"user"`
	} `json:"author"`
	FromRef apiRef `json:"fromRef"`
	ToRef   apiRef `json:"toRef"`
	Links   struct {
		Self []struct {
			Href string `json:"href"`
		} `json:"self"`
	} `json:"links"`
}

type apiCommit struct {
	ID      string `json:"id"`
	Message string `json:"message"`
	Parents []struct {
		ID string `json:"id"`
	} `json:"parents"`
}

// GetPullRequest implements provider.Provider. It also computes BaseSHA and
// BaseStrategy.
func (p *Provider) GetPullRequest(ctx context.Context, ref provider.PRRef) (*provider.PullRequest, error) {
	path, err := prPath(ref)
	if err != nil {
		return nil, err
	}
	if err := p.ensureSupported(ctx); err != nil {
		return nil, err
	}
	var in apiPR
	if err := p.client.GetJSON(ctx, path, &in); err != nil {
		return nil, err
	}
	baseSHA, strategy, err := p.computeBase(ctx, ref, in.ToRef.LatestCommit)
	if err != nil {
		return nil, err
	}
	author := in.Author.User.Name
	if author == "" {
		author = in.Author.User.DisplayName
	}
	webURL := ""
	if len(in.Links.Self) > 0 {
		webURL = in.Links.Self[0].Href
	}
	p.logger.Debug("bitbucket server base revision chosen", "strategy", strategy)
	return &provider.PullRequest{
		Title:        in.Title,
		Description:  in.Description,
		Author:       author,
		SourceBranch: in.FromRef.DisplayID,
		TargetBranch: in.ToRef.DisplayID,
		HeadSHA:      in.FromRef.LatestCommit,
		BaseSHA:      baseSHA,
		BaseStrategy: strategy,
		WebURL:       webURL,
		State:        in.State,
	}, nil
}

// GetCommitMessages implements provider.Provider. The commits endpoint is
// newest first; the result is oldest first.
func (p *Provider) GetCommitMessages(ctx context.Context, ref provider.PRRef) ([]string, error) {
	path, err := prPath(ref)
	if err != nil {
		return nil, err
	}
	if err := p.ensureSupported(ctx); err != nil {
		return nil, err
	}
	commits, err := httpx.PagesStartLimit[apiCommit](ctx, p.client, path+"/commits", pageLimit)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(commits))
	for i, c := range commits {
		out[len(commits)-1-i] = c.Message
	}
	return out, nil
}

// PostComment implements provider.Provider.
//
// Bitbucket Server returns no HTML link for a comment, so Comment.URL is
// built from the configured base URL and the ref only:
// {base_url}/projects/{K}/repos/{s}/pull-requests/{id}/overview?commentId={id}
// (/users/{u}/... for a "~u" namespace). Without a comment id the URL has no
// commentId parameter. The link format is a live-verification item.
func (p *Provider) PostComment(ctx context.Context, ref provider.PRRef, body string) (*provider.Comment, error) {
	path, err := prPath(ref)
	if err != nil {
		return nil, err
	}
	if err := p.ensureSupported(ctx); err != nil {
		return nil, err
	}
	var out struct {
		ID int64 `json:"id"`
	}
	if err := p.client.SendJSON(ctx, http.MethodPost, path+"/comments", map[string]string{"text": body}, &out); err != nil {
		return nil, err
	}
	// The comment already exists at this point, so a response without an id
	// is not turned into an error (a retry would post a duplicate).
	c := &provider.Comment{URL: p.prWebURL(ref) + "/overview"}
	if out.ID != 0 {
		c.ID = strconv.FormatInt(out.ID, 10)
		c.URL += "?commentId=" + c.ID
	}
	return c, nil
}

// prWebURL returns {base_url}/projects/{K}/repos/{s}/pull-requests/{id}
// (/users/{u}/... for a personal repository) from escaped segments.
func (p *Provider) prWebURL(ref provider.PRRef) string {
	owner := "/projects/" + url.PathEscape(ref.Namespace)
	if user, ok := strings.CutPrefix(ref.Namespace, "~"); ok {
		owner = "/users/" + url.PathEscape(user)
	}
	return p.baseURL + owner + "/repos/" + url.PathEscape(ref.Repo) +
		"/pull-requests/" + strconv.FormatInt(ref.Number, 10)
}

// FileLineURL implements provider.Provider. It is pure.
//
// Format: {base_url}/projects/{K}/repos/{s}/pull-requests/{id}/diff#{path}?t={line}
// (/users/{user}/... for a personal repository). It returns "" when pr is
// nil, mirroring the Gitea provider. The anchor format is a live-verification
// item.
func (p *Provider) FileLineURL(ref provider.PRRef, pr *provider.PullRequest, path string, line int) string {
	if pr == nil || ref.Namespace == "" || ref.Namespace == "~" || ref.Repo == "" || ref.Number <= 0 {
		return ""
	}
	segs := strings.Split(path, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	u := p.prWebURL(ref) + "/diff#" + strings.Join(segs, "/")
	if line > 0 {
		u += "?t=" + strconv.Itoa(line)
	}
	return u
}
