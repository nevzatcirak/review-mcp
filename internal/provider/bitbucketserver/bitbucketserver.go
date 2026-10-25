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
	fetchConcurrency = provider.ContentFetchConcurrency

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

// Capabilities implements provider.CapabilityReporter: the capabilities of
// every Provider New builds, without building one.
func (Factory) Capabilities() provider.Capabilities { return (*Provider)(nil).Capabilities() }

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
	// The URL shape has exactly one namespace and one slug segment: an
	// escaped "/" in either ("%2F") would smuggle a nested path in.
	if strings.Contains(segs[1], "/") || strings.Contains(segs[3], "/") {
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

// atBranchPrefix is the only form of the "at" query value a Bitbucket
// Server repository URL may name its branch with.
const atBranchPrefix = "refs/heads/"

// ParseRepoPath implements provider.Factory (v2.5 spec V-13b §1.1).
// remainder is escaped and starts with "/"; it must be
// /projects/{KEY}/repos/{slug} or /users/{user}/repos/{slug} (namespace
// "~{user}", as for pull requests), optionally followed by /browse. Only
// the /browse form may name a branch, and only with the query key "at"
// holding refs/heads/{branch} (one value; any other ref, such as a tag or a
// commit, is refused). Other query keys are not read. A trailing ".git" on
// the slug is dropped. Each segment is unescaped on its own; an escaped "/"
// ("%2F"), an empty segment and a "." or ".." segment are refused, and so
// is a branch that is not a provider.ValidBranchName.
//
// DESIGN-QUESTION (WP-13a DQ-1): the spec's table names
// /projects/{KEY}/repos/{slug} only; personal repositories
// (/users/{user}/repos/{slug}) are accepted too, with the namespace
// ParsePRPath gives them. Recommendation: keep; a personal repository's
// pull request is accepted, so its repository is too.
func (Factory) ParseRepoPath(remainder string, query url.Values) (namespace, repo, branch string, err error) {
	errShape := errors.New("not a Bitbucket Server repository path")
	if !strings.HasPrefix(remainder, "/") {
		return "", "", "", errShape
	}
	raw := strings.Split(remainder[1:], "/")
	if len(raw) != 4 && len(raw) != 5 {
		return "", "", "", errShape
	}
	segs := make([]string, len(raw))
	for i, r := range raw {
		s, uerr := url.PathUnescape(r)
		if uerr != nil || s == "" || strings.Contains(s, "/") || isDots(s) {
			return "", "", "", errShape
		}
		segs[i] = s
	}
	if segs[2] != "repos" || (len(segs) == 5 && segs[4] != "browse") {
		return "", "", "", errShape
	}
	switch segs[0] {
	case "projects":
		namespace = segs[1]
	case "users":
		namespace = "~" + segs[1]
	default:
		return "", "", "", errShape
	}
	repo = provider.TrimGitSuffix(segs[3])
	if repo == "" {
		return "", "", "", errShape
	}
	if at, ok := query["at"]; ok {
		if len(segs) != 5 || len(at) != 1 || !strings.HasPrefix(at[0], atBranchPrefix) {
			return "", "", "", errShape
		}
		branch = strings.TrimPrefix(at[0], atBranchPrefix)
		if !provider.ValidBranchName(branch) {
			return "", "", "", errShape
		}
	}
	return namespace, repo, branch, nil
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
func (*Provider) Kind() provider.Kind { return provider.KindBitbucketServer }

// Capabilities implements provider.Provider. Bitbucket Server has no issue
// tracker: Issues is false and the provider does not implement
// provider.IssueReader. The participant status takes every decision
// (decision.go).
func (*Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		GFM: false, MarkdownTables: true, Labels: false, InlineComments: true,
		InlineThreadResolution: true, GeneralThreadResolution: true, DescriptionEdit: true,
		Decisions: decisionCaps,
	}
}

// BaseStrategies returns the provider.PullRequest.BaseStrategy values this
// provider can produce (a fresh slice).
func BaseStrategies() []string {
	return []string{provider.BaseBBSMergeBaseEP, provider.BaseBBSAncestorWalk}
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

// apiPerson is a user object of a PR payload.
type apiPerson struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
}

type apiPR struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	State       string `json:"state"`
	Author      struct {
		User struct {
			ID          int64  `json:"id"`
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
		} `json:"user"`
	} `json:"author"`
	// Version is the PR's optimistic-locking counter; UpdatePullRequest
	// sends it back.
	Version   *int  `json:"version"`
	Draft     *bool `json:"draft"`
	Reviewers []struct {
		User               apiPerson `json:"user"`
		Status             string    `json:"status"`
		LastReviewedCommit string    `json:"lastReviewedCommit"`
	} `json:"reviewers"`
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
	authorID := ""
	if in.Author.User.ID > 0 {
		authorID = strconv.FormatInt(in.Author.User.ID, 10)
	}
	version := ""
	if in.Version != nil {
		version = strconv.Itoa(*in.Version)
	}
	p.logger.Debug("bitbucket server base revision chosen", "strategy", strategy)
	return &provider.PullRequest{
		Title:        in.Title,
		Description:  in.Description,
		Author:       author,
		AuthorID:     authorID,
		SourceBranch: in.FromRef.DisplayID,
		TargetBranch: in.ToRef.DisplayID,
		HeadSHA:      in.FromRef.LatestCommit,
		BaseSHA:      baseSHA,
		BaseStrategy: strategy,
		WebURL:       webURL,
		State:        in.State,
		Draft:        in.Draft,
		Merged:       strings.EqualFold(in.State, "MERGED"),
		Version:      version,
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
	return p.newComment(ref, out.ID), nil
}

// newComment builds the Comment for id; without an id the URL has no
// commentId parameter.
func (p *Provider) newComment(ref provider.PRRef, id int64) *provider.Comment {
	c := &provider.Comment{URL: p.prWebURL(ref) + "/overview"}
	if id != 0 {
		c.ID = strconv.FormatInt(id, 10)
		c.URL += "?commentId=" + c.ID
	}
	return c
}

// prWebURL returns {base_url}/projects/{K}/repos/{s}/pull-requests/{id}
// (/users/{u}/... for a personal repository) from escaped segments.
func (p *Provider) prWebURL(ref provider.PRRef) string {
	return p.repoWebURL(ref) + "/pull-requests/" + strconv.FormatInt(ref.Number, 10)
}

// repoWebURL is the repository's web URL: {base_url}/projects/{K}/repos/{s},
// or /users/{user}/repos/{s} for a personal repository.
func (p *Provider) repoWebURL(ref provider.PRRef) string {
	owner := "/projects/" + url.PathEscape(ref.Namespace)
	if user, ok := strings.CutPrefix(ref.Namespace, "~"); ok {
		owner = "/users/" + url.PathEscape(user)
	}
	return p.baseURL + owner + "/repos/" + url.PathEscape(ref.Repo)
}

// FileLineURL implements provider.Provider. It is pure.
//
// Format: {base_url}/projects/{K}/repos/{s}/pull-requests/{id}/diff#{path}?t={line}
// (/users/{user}/... for a personal repository). It returns "" when pr is
// nil, mirroring the Gitea provider. The anchor format is a live-verification
// item.
//
// A reference without a pull request number (a related repository at its
// pinned commit, v2.5 spec V-13b §2.4) links the file at pr.HeadSHA:
// {base_url}/projects/{K}/repos/{s}/browse/{path}?at={sha}#{line}; "" without
// a head.
func (p *Provider) FileLineURL(ref provider.PRRef, pr *provider.PullRequest, path string, line int) string {
	if pr == nil || ref.Namespace == "" || ref.Namespace == "~" || ref.Repo == "" || ref.Number < 0 {
		return ""
	}
	segs := strings.Split(path, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	if ref.Number == 0 {
		if pr.HeadSHA == "" {
			return ""
		}
		u := p.repoWebURL(ref) + "/browse/" + strings.Join(segs, "/") + "?at=" + url.QueryEscape(pr.HeadSHA)
		if line > 0 {
			u += "#" + strconv.Itoa(line)
		}
		return u
	}
	u := p.prWebURL(ref) + "/diff#" + strings.Join(segs, "/")
	if line > 0 {
		u += "?t=" + strconv.Itoa(line)
	}
	return u
}
