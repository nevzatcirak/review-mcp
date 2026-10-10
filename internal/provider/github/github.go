// Package github implements the provider.Provider interface for GitHub
// (github.com and GitHub Enterprise Server) over the REST API v3 (X-28,
// design note Y-12 to Y-16).
//
// API calls go to the API base (config.GitHub.EffectiveAPIURL: configured,
// or derived from the web base). The web base (github.base_url) is used only
// for FileLineURL and for resolver matching; it never receives an API call.
//
// This package holds the read path (WP-2j): pull request metadata, the base
// revision, the diff with contents, the commit messages and the token's
// user; comments and threads (WP-2k); and inline comments posted in one
// review, on single lines or ranges (WP-2l); and the review status and
// description edits (WP-2m).
package github

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/httpx"
	"github.com/nevzatcirak/review-mcp/internal/version"
)

const (
	// apiVersion is the REST API version every request asks for.
	apiVersion = "2022-11-28"
	// mediaJSON and mediaRaw are the Accept values of JSON and raw-content
	// requests.
	mediaJSON = "application/vnd.github+json"
	mediaRaw  = "application/vnd.github.raw"

	// perPage is the page size asked for; GitHub's maximum.
	perPage = 100
	// Page caps per list. GitHub itself stops listing a pull request's files
	// at 3000 (30 pages) and its commits at 250 (3 pages); the caps leave
	// room above that, so a cap is reached only when the server keeps
	// sending next links beyond its own limits, which is a protocol error
	// (as for the other providers' page ceiling).
	filesPageCap   = 40
	commitsPageCap = 5

	// maxListedFiles is the most files GitHub lists for a pull request.
	maxListedFiles = 3000

	// fetchConcurrency bounds parallel content requests.
	fetchConcurrency = 4

	capKeyFile = "diff.max_file_bytes"
)

// Factory builds GitHub providers and parses GitHub PR paths. It implements
// provider.Factory.
type Factory struct{}

// NewFactory returns the GitHub factory to hand to provider.NewResolver.
func NewFactory() Factory { return Factory{} }

// Kind implements provider.Factory.
func (Factory) Kind() provider.Kind { return provider.KindGitHub }

// ParsePRPath implements provider.Factory. remainder is escaped and starts
// with "/"; it must be /{owner}/{repo}/pull/{n}, optionally followed by more
// segments (such as /files or /commits; a "#..." fragment never reaches
// it). The owner is exactly one segment, and an escaped "/" ("%2F") in the
// owner or the repository is refused.
func (Factory) ParsePRPath(remainder string) (namespace, repo string, number int64, err error) {
	errShape := errors.New("not a GitHub pull request path")
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
	if strings.Contains(segs[0], "/") || strings.Contains(segs[1], "/") || segs[0] == "" || segs[1] == "" ||
		segs[2] != "pull" || isDots(segs[0]) || isDots(segs[1]) {
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
	if cfg == nil || cfg.GitHub.BaseURL == "" {
		return nil, &provider.Error{Class: provider.ClassURLNotConfigured, Hint: "github.base_url"}
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	p := &Provider{
		logger:   logger,
		webURL:   strings.TrimRight(cfg.GitHub.BaseURL, "/"),
		maxFiles: cfg.Diff.MaxFilesFullContent,
		maxFile:  int64(cfg.Diff.MaxFileBytes),
		now:      time.Now,
		sleep:    sleepCtx,
	}
	// The Secret value type is captured, not its revealed string: Reveal is
	// called inside the closure for every request.
	token := cfg.Secrets.GitHubToken
	c, err := httpx.New(httpx.Options{
		BaseURL:            cfg.GitHub.EffectiveAPIURL(),
		Auth:               func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token.Reveal()) },
		CACertPath:         cfg.GitHub.CACert,
		InsecureSkipVerify: cfg.GitHub.InsecureSkipVerify,
		UserAgent:          "review-mcp/" + version.Info().Version,
		Accept:             mediaJSON,
		Headers:            map[string]string{"X-GitHub-Api-Version": apiVersion},
		Classify:           func(status int, h http.Header) *provider.Error { return classify(status, h, p.now()) },
		Logger:             logger,
	})
	if err != nil {
		return nil, err
	}
	p.client = c
	return p, nil
}

// Provider is a request-scoped GitHub provider. It holds no revealed secret.
type Provider struct {
	client   *httpx.Client
	logger   *slog.Logger
	webURL   string
	maxFiles int
	maxFile  int64

	// now and sleep are the clock of the rate-limit handling; tests replace
	// them.
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error
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
func (*Provider) Kind() provider.Kind { return provider.KindGitHub }

// Capabilities implements provider.Provider. GitHub renders GFM with tables
// and has labels. Thread resolution is not readable through REST (Y-14), so
// both resolution flags stay false. Inline comments can carry a native
// suggestion block that replaces the comment's whole range (start_line to
// line, SuggestionStyleRange). The title and the description can be edited
// (a partial PATCH, no version).
func (*Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{GFM: true, MarkdownTables: true, Labels: true, InlineComments: true,
		SuggestionBlocks: true, SuggestionStyle: provider.SuggestionStyleRange, DescriptionEdit: true}
}

// BaseStrategies returns the provider.PullRequest.BaseStrategy values this
// provider can produce (a fresh slice).
func BaseStrategies() []string {
	return []string{provider.BaseGitHubMergeBase, provider.BaseGitHubBaseSHA}
}

func protocolErr(hint string) *provider.Error {
	return &provider.Error{Class: provider.ClassProtocol, Hint: hint}
}

// repoPath returns "/repos/{o}/{r}" built from escaped segments.
func repoPath(ref provider.PRRef) (string, error) {
	if ref.Namespace == "" || ref.Repo == "" || isDots(ref.Namespace) || isDots(ref.Repo) ||
		strings.Contains(ref.Namespace, "/") || strings.Contains(ref.Repo, "/") {
		return "", protocolErr("invalid repository reference")
	}
	if ref.Number <= 0 {
		return "", protocolErr("invalid pull request number")
	}
	return "/repos/" + url.PathEscape(ref.Namespace) + "/" + url.PathEscape(ref.Repo), nil
}

func prPath(ref provider.PRRef) (string, error) {
	rp, err := repoPath(ref)
	if err != nil {
		return "", err
	}
	return rp + "/pulls/" + strconv.FormatInt(ref.Number, 10), nil
}

type apiUser struct {
	Login string `json:"login"`
	ID    int64  `json:"id"`
}

type apiRef struct {
	Ref string `json:"ref"`
	SHA string `json:"sha"`
}

type apiPR struct {
	Title          string  `json:"title"`
	Body           *string `json:"body"`
	State          string  `json:"state"`
	HTMLURL        string  `json:"html_url"`
	Draft          *bool   `json:"draft"`
	Merged         bool    `json:"merged"`
	MergedAt       *string `json:"merged_at"`
	Mergeable      *bool   `json:"mergeable"`
	MergeableState string  `json:"mergeable_state"`
	ChangedFiles   int     `json:"changed_files"`
	User           apiUser `json:"user"`
	Head           apiRef  `json:"head"`
	Base           apiRef  `json:"base"`
}

// GetPullRequest implements provider.Provider. It also derives BaseSHA and
// BaseStrategy: the merge base from the compare endpoint, or, when that
// cannot be read, the target branch revision recorded on the pull request.
func (p *Provider) GetPullRequest(ctx context.Context, ref provider.PRRef) (*provider.PullRequest, error) {
	path, err := prPath(ref)
	if err != nil {
		return nil, err
	}
	var in apiPR
	if err := p.getJSON(ctx, path, &in); err != nil {
		return nil, err
	}
	pr := &provider.PullRequest{
		Title:          in.Title,
		Author:         in.User.Login,
		SourceBranch:   in.Head.Ref,
		TargetBranch:   in.Base.Ref,
		HeadSHA:        in.Head.SHA,
		WebURL:         in.HTMLURL,
		State:          in.State,
		Draft:          in.Draft,
		Merged:         in.Merged || (in.MergedAt != nil && *in.MergedAt != ""),
		Mergeable:      in.Mergeable,
		MergeableState: in.MergeableState,
		ChangedFiles:   in.ChangedFiles,
	}
	if in.Body != nil {
		pr.Description = *in.Body
	}
	pr.BaseSHA, pr.BaseStrategy = p.baseRevision(ctx, ref, in.Base.SHA, in.Head.SHA)
	return pr, nil
}

// baseRevision returns the merge base of base and head from the compare
// endpoint (github:merge_base), or base itself (github:base_sha) when the
// comparison cannot be read. The fallback is recorded in the strategy and
// logged; it never fails the call.
func (p *Provider) baseRevision(ctx context.Context, ref provider.PRRef, base, head string) (string, string) {
	if base == "" || head == "" {
		p.logger.Debug("github base revision chosen", "strategy", provider.BaseGitHubBaseSHA, "reason", "missing revision")
		return base, provider.BaseGitHubBaseSHA
	}
	rp, err := repoPath(ref)
	if err != nil {
		return base, provider.BaseGitHubBaseSHA
	}
	var cmp struct {
		MergeBaseCommit struct {
			SHA string `json:"sha"`
		} `json:"merge_base_commit"`
	}
	// per_page=1 keeps the commit list of the comparison short; only the
	// merge base is read.
	path := rp + "/compare/" + url.PathEscape(base) + "..." + url.PathEscape(head) + "?per_page=1"
	if err := p.getJSON(ctx, path, &cmp); err != nil || cmp.MergeBaseCommit.SHA == "" {
		p.logger.Debug("github base revision chosen", "strategy", provider.BaseGitHubBaseSHA, "reason", "compare unavailable",
			"error", errClass(err))
		return base, provider.BaseGitHubBaseSHA
	}
	p.logger.Debug("github base revision chosen", "strategy", provider.BaseGitHubMergeBase)
	return cmp.MergeBaseCommit.SHA, provider.BaseGitHubMergeBase
}

// GetCommitMessages implements provider.Provider. GitHub lists a pull
// request's commits oldest first, and at most 250 of them.
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
	commits, err := httpx.PagesByLink[apiCommit](ctx, p.client, p.fetchPage,
		path+"/commits?per_page="+strconv.Itoa(perPage), commitsPageCap)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(commits))
	for i, c := range commits {
		out[i] = c.Commit.Message
	}
	return out, nil
}

// CurrentUser implements provider.Provider: GET /user.
func (p *Provider) CurrentUser(ctx context.Context) (provider.User, error) {
	var u apiUser
	if err := p.getJSON(ctx, "/user", &u); err != nil {
		return provider.User{}, err
	}
	if u.Login == "" {
		return provider.User{}, protocolErr("the user response has no login")
	}
	out := provider.User{Name: u.Login}
	if u.ID > 0 {
		out.ID = strconv.FormatInt(u.ID, 10)
	}
	return out, nil
}

// FileLineURL implements provider.Provider. It is pure.
//
// When pr is nil or has no head SHA it returns "": a link without a commit
// would point at a moving target.
func (p *Provider) FileLineURL(ref provider.PRRef, pr *provider.PullRequest, path string, line int) string {
	if pr == nil || pr.HeadSHA == "" {
		return ""
	}
	segs := strings.Split(path, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	u := p.webURL + "/" + url.PathEscape(ref.Namespace) + "/" + url.PathEscape(ref.Repo) +
		"/blob/" + url.PathEscape(pr.HeadSHA) + "/" + strings.Join(segs, "/")
	if line > 0 {
		u += "#L" + strconv.Itoa(line)
	}
	return u
}

// errClass returns only the class (and status) of a provider error, for
// debug logs.
func errClass(err error) string {
	if err == nil {
		return "none"
	}
	var perr *provider.Error
	if errors.As(err, &perr) {
		s := string(perr.Class)
		if perr.Status != 0 {
			s += " " + strconv.Itoa(perr.Status)
		}
		return s
	}
	return "unknown"
}
