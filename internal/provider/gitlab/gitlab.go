// Package gitlab implements the provider.Provider interface for GitLab
// (gitlab.com and self-managed GitLab 16.0 or newer) over the REST API v4
// (X-34, design note Y-17 to Y-21, spec 2C).
//
// API calls go to {gitlab.base_url}/api/v4 (config.GitLab.APIURL). The web
// base, with any context path, is used only for FileLineURL and for
// resolver matching; it never receives an API call. A project is addressed
// by its URL-encoded full path ("group%2Fsub%2Fproject"); the escaped
// slashes reach the server as sent.
//
// This package holds the read path (WP-6a): merge request metadata, the base
// revision from diff_refs, the diff with contents, the commit messages, the
// token's user, files and directory listings at a revision; and the
// discussions (WP-6b, comments.go): threads, replies, edits and general
// comments; the inline discussions (WP-6c, inline.go); and pr_info
// (status.go), description edits (update.go) and CI (checks.go) (WP-6d).
package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/httpx"
	"github.com/nevzatcirak/review-mcp/internal/version"
)

const (
	// mediaJSON is the Accept value of every request but the raw file
	// request, which sends acceptRaw.
	mediaJSON = "application/json"
	acceptRaw = "*/*"

	// perPage is the page size asked for; GitLab's maximum.
	perPage = 100
	// Page caps per list (perPage items each). GitLab lists at most 3000
	// changed files of a merge request (the highest diff limit an
	// administrator can set), so diffsPageCap is reached only past that; a
	// merge request with more than commitsPageCap*perPage commits is read
	// up to the cap. treePageCap leaves room above provider.MaxDirEntries.
	diffsPageCap   = 40
	commitsPageCap = 10
	treePageCap    = 10

	// diffRefsRetry is the one wait before the merge request is read again
	// when its diff_refs are still empty (spec 2C §1 item 6).
	diffRefsRetry = 2 * time.Second

	// fetchConcurrency bounds parallel content requests.
	fetchConcurrency = provider.ContentFetchConcurrency

	capKeyFile = "diff.max_file_bytes"
)

// Fixed hints.
const (
	// HintDiffNotReady is the Hint of the upstream error of a merge request
	// whose diff_refs are still empty after the one retry.
	HintDiffNotReady = "the merge request's diff is not ready yet; try again in a moment"
	// HintMinVersion is the Hint of the unsupported_version error of an
	// endpoint that answers 404 although the merge request itself can be
	// read.
	HintMinVersion = "GitLab 16.0 or newer is required"
)

// Factory builds GitLab providers and parses GitLab merge request paths. It
// implements provider.Factory.
type Factory struct{}

// NewFactory returns the GitLab factory to hand to provider.NewResolver.
func NewFactory() Factory { return Factory{} }

// Kind implements provider.Factory.
func (Factory) Kind() provider.Kind { return provider.KindGitLab }

// Capabilities implements provider.CapabilityReporter: the capabilities of
// every Provider New builds, without building one.
func (Factory) Capabilities() provider.Capabilities { return (*Provider)(nil).Capabilities() }

// mrTails are the segments that may follow /-/merge_requests/{iid} (a
// "#..." fragment never reaches ParsePRPath).
var mrTails = []string{"diffs", "commits", "pipelines"}

// ParsePRPath implements provider.Factory. remainder is escaped and starts
// with "/"; it must be /{namespace...}/{project}/-/merge_requests/{iid},
// optionally followed by exactly one of /diffs, /commits or /pipelines.
// The namespace is every segment before the project, at least one (nested
// groups). Only the "/-/" form is accepted. An escaped "/" ("%2F"), an
// empty segment and a "." or ".." segment are refused.
func (Factory) ParsePRPath(remainder string) (namespace, repo string, number int64, err error) {
	errShape := errors.New("not a GitLab merge request path")
	if !strings.HasPrefix(remainder, "/") {
		return "", "", 0, errShape
	}
	raw := strings.Split(remainder[1:], "/")
	segs := make([]string, len(raw))
	for i, r := range raw {
		s, uerr := url.PathUnescape(r)
		if uerr != nil || s == "" || strings.Contains(s, "/") || isDots(s) {
			return "", "", 0, errShape
		}
		segs[i] = s
	}
	dash := slices.Index(segs, "-")
	if dash < 2 || len(segs) < dash+3 || segs[dash+1] != "merge_requests" {
		return "", "", 0, errShape
	}
	n, ok := parsePositive(segs[dash+2])
	if !ok {
		return "", "", 0, errShape
	}
	switch tail := segs[dash+3:]; {
	case len(tail) == 0:
	case len(tail) == 1 && slices.Contains(mrTails, tail[0]):
	default:
		return "", "", 0, errShape
	}
	return strings.Join(segs[:dash-1], "/"), segs[dash-1], n, nil
}

// ParseRepoPath implements provider.Factory (v2.5 spec V-13b §1.1).
// remainder is escaped and starts with "/"; it must be
// /{namespace...}/{project} or /{namespace...}/{project}/-/tree/{branch...},
// the namespace being every segment before the project, at least one
// (nested groups), and the branch every segment after /-/tree/ joined with
// "/". A trailing ".git" on the project is dropped. Each segment is
// unescaped on its own; an escaped "/" ("%2F"), an empty segment and a "."
// or ".." segment are refused, and so is a branch that is not a
// provider.ValidBranchName. The query is not read.
func (Factory) ParseRepoPath(remainder string, _ url.Values) (namespace, repo, branch string, err error) {
	errShape := errors.New("not a GitLab repository path")
	if !strings.HasPrefix(remainder, "/") {
		return "", "", "", errShape
	}
	raw := strings.Split(remainder[1:], "/")
	segs := make([]string, len(raw))
	for i, r := range raw {
		s, uerr := url.PathUnescape(r)
		if uerr != nil || s == "" || strings.Contains(s, "/") || isDots(s) {
			return "", "", "", errShape
		}
		segs[i] = s
	}
	path := segs
	if dash := slices.Index(segs, "-"); dash >= 0 {
		if len(segs) < dash+3 || segs[dash+1] != "tree" {
			return "", "", "", errShape
		}
		path, branch = segs[:dash], strings.Join(segs[dash+2:], "/")
		if !provider.ValidBranchName(branch) {
			return "", "", "", errShape
		}
	}
	if len(path) < 2 {
		return "", "", "", errShape
	}
	repo = provider.TrimGitSuffix(path[len(path)-1])
	if repo == "" {
		return "", "", "", errShape
	}
	return strings.Join(path[:len(path)-1], "/"), repo, branch, nil
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
	if cfg == nil || cfg.GitLab.BaseURL == "" {
		return nil, &provider.Error{Class: provider.ClassURLNotConfigured, Hint: "gitlab.base_url"}
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	p := &Provider{
		logger:   logger,
		webURL:   strings.TrimRight(cfg.GitLab.BaseURL, "/"),
		maxFiles: cfg.Diff.MaxFilesFullContent,
		maxFile:  int64(cfg.Diff.MaxFileBytes),
		now:      time.Now,
		sleep:    sleepCtx,
	}
	// The Secret value type is captured, not its revealed string: Reveal is
	// called inside the closure for every request.
	token := cfg.Secrets.GitLabToken
	c, err := httpx.New(httpx.Options{
		BaseURL:            cfg.GitLab.APIURL(),
		Auth:               func(r *http.Request) { r.Header.Set("PRIVATE-TOKEN", token.Reveal()) },
		CACertPath:         cfg.GitLab.CACert,
		InsecureSkipVerify: cfg.GitLab.InsecureSkipVerify,
		UserAgent:          "review-mcp/" + version.Info().Version,
		Accept:             mediaJSON,
		Classify:           func(status int, h http.Header) *provider.Error { return classify(status, h, p.now()) },
		Logger:             logger,
	})
	if err != nil {
		return nil, err
	}
	p.client = c
	return p, nil
}

// Provider is a request-scoped GitLab provider. It holds no revealed secret.
type Provider struct {
	client   *httpx.Client
	logger   *slog.Logger
	webURL   string
	maxFiles int
	maxFile  int64

	// now and sleep are the clock of the rate-limit handling and of the
	// diff_refs retry; tests replace them.
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
func (*Provider) Kind() provider.Kind { return provider.KindGitLab }

// Capabilities implements provider.Provider. GitLab renders GFM with tables
// and has labels, and a published line that starts with "/" can run a
// quick action, so every published body is slash-sanitised (X-35). Both
// kinds of discussion can be resolved. Inline comments are discussions on
// one line (inline.go), and a native suggestion block sits on that line
// and says how many lines below it it replaces ("suggestion:-0+N",
// SuggestionStyleOffset). A description (and title) can be edited
// (update.go); the draft state of a merge request follows its title.
func (*Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		GFM: true, MarkdownTables: true, Labels: true, InlineComments: true,
		SuggestionBlocks: true, SuggestionStyle: provider.SuggestionStyleOffset, QuickActions: true,
		InlineThreadResolution: true, GeneralThreadResolution: true, DescriptionEdit: true, Issues: true,
	}
}

// BaseStrategies returns the provider.PullRequest.BaseStrategy values this
// provider can produce (a fresh slice).
func BaseStrategies() []string {
	return []string{provider.BaseGitLabDiffRefs}
}

// CheckStrategies returns the provider.Checks.Strategy values this provider
// will produce (a fresh slice).
func CheckStrategies() []string {
	return []string{provider.ChecksGitLabCommitStatuses}
}

func protocolErr(hint string) *provider.Error {
	return &provider.Error{Class: provider.ClassProtocol, Hint: hint}
}

// validName reports whether s is usable as one segment of a project path.
func validName(s string) bool {
	return s != "" && !isDots(s) && !strings.Contains(s, "/")
}

// projectPath returns "/projects/{id}" where id is the URL-encoded full
// path of the project: every "/" of namespace/repo is sent as "%2F".
func projectPath(ref provider.PRRef) (string, error) {
	if !validName(ref.Repo) || ref.Namespace == "" {
		return "", protocolErr("invalid project reference")
	}
	for _, s := range strings.Split(ref.Namespace, "/") {
		if !validName(s) {
			return "", protocolErr("invalid project reference")
		}
	}
	return "/projects/" + url.PathEscape(ref.Namespace+"/"+ref.Repo), nil
}

func mrPath(ref provider.PRRef) (string, error) {
	pp, err := projectPath(ref)
	if err != nil {
		return "", err
	}
	if ref.Number <= 0 {
		return "", protocolErr("invalid merge request number")
	}
	return pp + "/merge_requests/" + strconv.FormatInt(ref.Number, 10), nil
}

type apiUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
}

type apiDiffRefs struct {
	BaseSHA  string `json:"base_sha"`
	HeadSHA  string `json:"head_sha"`
	StartSHA string `json:"start_sha"`
}

// changesCount is GitLab's changes_count: a decimal string such as "12",
// or "1000+" when GitLab stopped at its diff limit. A number or null is
// accepted too.
type changesCount struct {
	n       int
	atLeast bool
}

// UnmarshalJSON implements json.Unmarshaler. A value that is not a count
// is unknown (0), never an error: the field only feeds a note.
func (c *changesCount) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) != nil {
		var n json.Number
		if json.Unmarshal(b, &n) != nil {
			return nil
		}
		s = n.String()
	}
	*c = parseChangesCount(s)
	return nil
}

// parseChangesCount parses "12" or "1000+".
func parseChangesCount(s string) changesCount {
	s = strings.TrimSpace(s)
	num, plus := strings.CutSuffix(s, "+")
	n, ok := digits(num)
	if !ok || n > 1<<31 {
		return changesCount{}
	}
	return changesCount{n: int(n), atLeast: plus}
}

type apiMR struct {
	Title               string       `json:"title"`
	Description         *string      `json:"description"`
	State               string       `json:"state"`
	Draft               bool         `json:"draft"`
	DetailedMergeStatus string       `json:"detailed_merge_status"`
	WebURL              string       `json:"web_url"`
	ChangesCount        changesCount `json:"changes_count"`
	SourceBranch        string       `json:"source_branch"`
	TargetBranch        string       `json:"target_branch"`
	SHA                 string       `json:"sha"`
	DiffRefs            *apiDiffRefs `json:"diff_refs"`
	Author              apiUser      `json:"author"`
	SourceProjectID     int64        `json:"source_project_id"`
	TargetProjectID     int64        `json:"target_project_id"`
}

// diffReady reports whether the merge request's diff can be read: GitLab
// has filled its diff_refs (it does so asynchronously after the merge
// request is created), and sha, when set, is diff_refs' head (otherwise a
// push is still being processed). The diff, the contents and the inline
// positions then all use diff_refs' head.
func (m *apiMR) diffReady() bool {
	return m.DiffRefs != nil && m.DiffRefs.BaseSHA != "" && m.DiffRefs.HeadSHA != "" &&
		(m.SHA == "" || m.SHA == m.DiffRefs.HeadSHA)
}

// mapState maps a merge request state to the provider-neutral words:
// opened is open, merged is merged, closed and locked are closed. Anything
// else is passed on, and pr_info reports it as unknown.
func mapState(s string) (state string, merged bool) {
	switch s {
	case "opened":
		return "open", false
	case "merged":
		return "merged", true
	case "closed", "locked":
		return "closed", false
	}
	return s, false
}

// GetPullRequest implements provider.Provider: GET
// /projects/{id}/merge_requests/{iid}. The base revision is diff_refs'
// base_sha (gitlab:diff_refs) and the head revision is diff_refs' head_sha.
// GitLab fills diff_refs asynchronously after a merge request is created or
// pushed to: when they are still empty, or sha differs from their head,
// the merge request is read once more after diffRefsRetry, if that wait
// ends before ctx's deadline; still not ready, the call fails with class
// upstream and HintDiffNotReady.
func (p *Provider) GetPullRequest(ctx context.Context, ref provider.PRRef) (*provider.PullRequest, error) {
	path, err := mrPath(ref)
	if err != nil {
		return nil, err
	}
	var in apiMR
	if err := p.getJSON(ctx, path, &in); err != nil {
		return nil, err
	}
	if !in.diffReady() {
		notReady := &provider.Error{Class: provider.ClassUpstream, Hint: HintDiffNotReady}
		if !p.fitsDeadline(ctx, diffRefsRetry) {
			return nil, notReady
		}
		p.logger.Debug("gitlab merge request diff_refs are not ready: reading it again once", "wait_ms", diffRefsRetry.Milliseconds())
		if serr := p.sleep(ctx, diffRefsRetry); serr != nil {
			return nil, provider.TransportError(serr)
		}
		in = apiMR{}
		if err := p.getJSON(ctx, path, &in); err != nil {
			return nil, err
		}
		if !in.diffReady() {
			return nil, notReady
		}
	}
	state, merged := mapState(in.State)
	draft := in.Draft
	pr := &provider.PullRequest{
		Title:               in.Title,
		Author:              in.Author.Username,
		SourceBranch:        in.SourceBranch,
		TargetBranch:        in.TargetBranch,
		HeadSHA:             in.DiffRefs.HeadSHA,
		BaseSHA:             in.DiffRefs.BaseSHA,
		StartSHA:            in.DiffRefs.StartSHA,
		BaseStrategy:        provider.BaseGitLabDiffRefs,
		WebURL:              in.WebURL,
		State:               state,
		Draft:               &draft,
		Merged:              merged,
		MergeableState:      in.DetailedMergeStatus,
		ChangedFiles:        in.ChangesCount.n,
		ChangedFilesAtLeast: in.ChangesCount.atLeast,
	}
	if in.Description != nil {
		pr.Description = *in.Description
	}
	if in.SourceProjectID != 0 && in.SourceProjectID != in.TargetProjectID {
		p.logger.Debug("gitlab merge request comes from a fork")
	}
	p.logger.Debug("gitlab base revision chosen", "strategy", provider.BaseGitLabDiffRefs)
	return pr, nil
}

// GetCommitMessages implements provider.Provider: GET
// /projects/{id}/merge_requests/{iid}/commits, Link-paged. GitLab lists
// the newest commit first; the messages are returned oldest first. A merge
// request with more commits than commitsPageCap pages hold is read up to
// the cap (its newest commits).
func (p *Provider) GetCommitMessages(ctx context.Context, ref provider.PRRef) ([]string, error) {
	path, err := mrPath(ref)
	if err != nil {
		return nil, err
	}
	type apiCommit struct {
		Message string `json:"message"`
	}
	commits, truncated, err := httpx.PagesByLinkCapped(ctx, p.client, p.fetchPage,
		path+"/commits?per_page="+strconv.Itoa(perPage), httpx.Cap{Pages: commitsPageCap},
		func(pg *[]apiCommit) []apiCommit { return *pg })
	if err != nil {
		return nil, err
	}
	if truncated {
		p.logger.Debug("gitlab commit list cut at the page cap", "pages", commitsPageCap)
	}
	out := make([]string, len(commits))
	for i, c := range commits {
		out[len(commits)-1-i] = c.Message
	}
	return out, nil
}

// CurrentUser implements provider.Provider: GET /user. Project and group
// access tokens authenticate as a bot user, which /user reports as well.
func (p *Provider) CurrentUser(ctx context.Context) (provider.User, error) {
	var u apiUser
	if err := p.getJSON(ctx, "/user", &u); err != nil {
		return provider.User{}, err
	}
	if u.Username == "" {
		return provider.User{}, protocolErr("the user response has no username")
	}
	out := provider.User{Name: u.Username}
	if u.ID > 0 {
		out.ID = strconv.FormatInt(u.ID, 10)
	}
	return out, nil
}

// FileLineURL implements provider.Provider. It is pure:
// {base}/{full_path}/-/blob/{head_sha}/{path}#L{line}, every segment
// escaped.
//
// When pr is nil or has no head SHA it returns "": a link without a commit
// would point at a moving target.
func (p *Provider) FileLineURL(ref provider.PRRef, pr *provider.PullRequest, path string, line int) string {
	if pr == nil || pr.HeadSHA == "" {
		return ""
	}
	u := p.webURL + "/" + provider.EscapeNamespace(ref.Namespace) + "/" + url.PathEscape(ref.Repo) +
		"/-/blob/" + url.PathEscape(pr.HeadSHA) + "/" + escapePath(path)
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
