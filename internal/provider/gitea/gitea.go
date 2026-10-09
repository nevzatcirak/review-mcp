// Package gitea implements the provider.Provider interface for Gitea (and
// API-compatible forges) over the REST API v1.
//
// All API calls go to {base_url}/api/v1. The optional web_url is used only
// for FileLineURL and for resolver matching; it never receives an API call.
package gitea

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/httpx"
	"github.com/nevzatcirak/review-mcp/internal/version"
)

const (
	apiPrefix = "/api/v1"
	pageLimit = 50
	// fetchConcurrency bounds parallel raw-content requests.
	fetchConcurrency = 4

	capKeyDiff = "diff.max_diff_bytes"
	capKeyFile = "diff.max_file_bytes"
)

// Factory builds Gitea providers and parses Gitea PR paths. It implements
// provider.Factory.
type Factory struct{}

// NewFactory returns the Gitea factory to hand to provider.NewResolver.
func NewFactory() Factory { return Factory{} }

// Kind implements provider.Factory.
func (Factory) Kind() provider.Kind { return provider.KindGitea }

// ParsePRPath implements provider.Factory. remainder is escaped and starts
// with "/"; it must be /{owner}/{repo}/pulls/{n}, optionally followed by more
// segments.
func (Factory) ParsePRPath(remainder string) (namespace, repo string, number int64, err error) {
	errShape := errors.New("not a Gitea pull request path")
	if !strings.HasPrefix(remainder, "/") {
		return "", "", 0, errShape
	}
	raw := strings.Split(remainder[1:], "/")
	if len(raw) < 4 {
		return "", "", 0, errShape
	}
	segs := make([]string, 4)
	for i := range segs {
		s, uerr := url.PathUnescape(raw[i])
		if uerr != nil {
			return "", "", 0, errShape
		}
		segs[i] = s
	}
	// The URL shape has exactly one namespace and one repository segment: an
	// escaped "/" in either ("%2F") would smuggle a nested path in.
	if strings.Contains(segs[0], "/") || strings.Contains(segs[1], "/") || segs[0] == "" || segs[1] == "" || segs[2] != "pulls" || isDots(segs[0]) || isDots(segs[1]) {
		return "", "", 0, errShape
	}
	n, ok := parsePositive(segs[3])
	if !ok {
		return "", "", 0, errShape
	}
	return segs[0], segs[1], n, nil
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
	if cfg == nil || cfg.Gitea.BaseURL == "" {
		return nil, &provider.Error{Class: provider.ClassURLNotConfigured, Hint: "gitea.base_url"}
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	// The Secret value type is captured, not its revealed string: Reveal is
	// called inside the closure for every request.
	token := cfg.Secrets.GiteaToken
	c, err := httpx.New(httpx.Options{
		BaseURL:            cfg.Gitea.BaseURL,
		Auth:               func(r *http.Request) { r.Header.Set("Authorization", "token "+token.Reveal()) },
		CACertPath:         cfg.Gitea.CACert,
		InsecureSkipVerify: cfg.Gitea.InsecureSkipVerify,
		UserAgent:          "review-mcp/" + version.Info().Version,
		Logger:             logger,
	})
	if err != nil {
		return nil, err
	}
	webURL := cfg.Gitea.WebURL
	if webURL == "" {
		webURL = cfg.Gitea.BaseURL
	}
	return &Provider{
		client:   c,
		logger:   logger,
		webURL:   strings.TrimRight(webURL, "/"),
		maxFiles: cfg.Diff.MaxFilesFullContent,
		maxFile:  int64(cfg.Diff.MaxFileBytes),
		maxDiff:  int64(cfg.Diff.MaxDiffBytes),
	}, nil
}

// Provider is a request-scoped Gitea provider. It holds no revealed secret.
type Provider struct {
	client   *httpx.Client
	logger   *slog.Logger
	webURL   string
	maxFiles int
	maxFile  int64
	maxDiff  int64
}

var _ provider.Provider = (*Provider)(nil)

// CloseIdleConnections closes the idle connections of the provider's
// per-call HTTP client (provider.IdleCloser). It is safe on a nil provider
// and may be called more than once.
func (p *Provider) CloseIdleConnections() {
	if p == nil {
		return
	}
	p.client.CloseIdleConnections()
}

// Kind implements provider.Provider.
func (*Provider) Kind() provider.Kind { return provider.KindGitea }

// Capabilities implements provider.Provider.
func (*Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		GFM: true, MarkdownTables: true, Labels: true, InlineComments: true,
		InlineThreadResolution: true, DescriptionEdit: true,
	}
}

// BaseStrategies returns the provider.PullRequest.BaseStrategy values this
// provider can produce (a fresh slice).
func BaseStrategies() []string {
	return []string{provider.BaseGiteaMergeBase, provider.BaseGiteaBaseSHA}
}

func protocolErr(hint string) *provider.Error {
	return &provider.Error{Class: provider.ClassProtocol, Hint: hint}
}

// repoPath returns "/api/v1/repos/{o}/{r}" built from escaped segments.
func repoPath(ref provider.PRRef) (string, error) {
	if ref.Namespace == "" || ref.Repo == "" || isDots(ref.Namespace) || isDots(ref.Repo) {
		return "", protocolErr("invalid repository reference")
	}
	if ref.Number <= 0 {
		return "", protocolErr("invalid pull request number")
	}
	return apiPrefix + "/repos/" + url.PathEscape(ref.Namespace) + "/" + url.PathEscape(ref.Repo), nil
}

func prPath(ref provider.PRRef) (string, error) {
	rp, err := repoPath(ref)
	if err != nil {
		return "", err
	}
	return rp + "/pulls/" + strconv.FormatInt(ref.Number, 10), nil
}

type apiPR struct {
	Title     string `json:"title"`
	Body      string `json:"body"`
	State     string `json:"state"`
	HTMLURL   string `json:"html_url"`
	MergeBase string `json:"merge_base"`
	Draft     *bool  `json:"draft"`
	Merged    bool   `json:"merged"`
	Mergeable *bool  `json:"mergeable"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
	RequestedReviewers []apiUser `json:"requested_reviewers"`
	Head               struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"base"`
}

// GetPullRequest implements provider.Provider. It also derives BaseSHA and
// BaseStrategy.
func (p *Provider) GetPullRequest(ctx context.Context, ref provider.PRRef) (*provider.PullRequest, error) {
	path, err := prPath(ref)
	if err != nil {
		return nil, err
	}
	var in apiPR
	if err := p.client.GetJSON(ctx, path, &in); err != nil {
		return nil, err
	}
	baseSHA, strategy := in.Base.SHA, provider.BaseGiteaBaseSHA
	if in.MergeBase != "" {
		baseSHA, strategy = in.MergeBase, provider.BaseGiteaMergeBase
	}
	p.logger.Debug("gitea base revision chosen", "strategy", strategy)
	return &provider.PullRequest{
		Title:        in.Title,
		Description:  in.Body,
		Author:       in.User.Login,
		SourceBranch: in.Head.Ref,
		TargetBranch: in.Base.Ref,
		HeadSHA:      in.Head.SHA,
		BaseSHA:      baseSHA,
		BaseStrategy: strategy,
		WebURL:       in.HTMLURL,
		State:        in.State,
		Draft:        in.Draft,
		Merged:       in.Merged,
		Mergeable:    in.Mergeable,
	}, nil
}

// GetCommitMessages implements provider.Provider. The commits endpoint is
// newest first; the result is oldest first.
func (p *Provider) GetCommitMessages(ctx context.Context, ref provider.PRRef) ([]string, error) {
	path, err := prPath(ref)
	if err != nil {
		return nil, err
	}
	type apiCommit struct {
		Commit struct {
			Message string `json:"message"`
		} `json:"commit"`
	}
	commits, err := httpx.PagesUntilEmpty[apiCommit](ctx, p.client, path+"/commits", pageLimit)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(commits))
	for i, c := range commits {
		out[len(commits)-1-i] = c.Commit.Message
	}
	return out, nil
}

// PostComment implements provider.Provider.
func (p *Provider) PostComment(ctx context.Context, ref provider.PRRef, body string) (*provider.Comment, error) {
	rp, err := repoPath(ref)
	if err != nil {
		return nil, err
	}
	var out struct {
		ID      int64  `json:"id"`
		HTMLURL string `json:"html_url"`
	}
	path := rp + "/issues/" + strconv.FormatInt(ref.Number, 10) + "/comments"
	if err := p.client.SendJSON(ctx, http.MethodPost, path, map[string]string{"body": body}, &out); err != nil {
		return nil, err
	}
	c := &provider.Comment{URL: out.HTMLURL}
	// The comment already exists at this point, so a response without an id
	// is not turned into an error (a retry would post a duplicate).
	if out.ID != 0 {
		c.ID = strconv.FormatInt(out.ID, 10)
	}
	return c, nil
}

// FileLineURL implements provider.Provider. It is pure.
//
// When pr is nil or has no head SHA it returns "": a link without a commit
// would point at a moving target, and the spec defines only the commit form.
func (p *Provider) FileLineURL(ref provider.PRRef, pr *provider.PullRequest, path string, line int) string {
	if pr == nil || pr.HeadSHA == "" {
		return ""
	}
	segs := strings.Split(path, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	u := p.webURL + "/" + url.PathEscape(ref.Namespace) + "/" + url.PathEscape(ref.Repo) +
		"/src/commit/" + url.PathEscape(pr.HeadSHA) + "/" + strings.Join(segs, "/")
	if line > 0 {
		u += "#L" + strconv.Itoa(line)
	}
	return u
}
