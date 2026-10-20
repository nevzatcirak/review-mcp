// Package related reads the related pull requests of a review (X-41, v2.3
// spec V-13a): pull requests the caller names as read-only context of a
// primary pull request, on any configured provider. review-mcp never
// discovers them, never publishes to them, and reads only what the review
// needs: the pull request's title, state, draft flag, target branch, head
// revision and diff (no description, no comments).
//
// From v2.5 (spec V-13b) a related item may also be a repository, or a
// branch of it: it is fetched read-only at one commit, pinned when the run
// reads it, through the repository-context machinery (gitctx.EnsureBranch),
// and only with context.repo.enabled, and searched there for the changed
// contract surface of the primary diff (Surface, Search). It is fetched only
// when there is something to search for. Nothing in it is built or run
// (R-1).
//
// Every related URL goes through the run's resolver, the same one as the
// primary's: an URL that matches no configured provider is never sent
// anywhere, and in serve mode a provider without the caller's token is
// refused before any request. A related pull request that cannot be read
// never fails the run, and never means that it does not exist. Logs carry
// an item's index, provider kind, status and reason only: never its URL
// path, title or diff; for a repository item also its item kind and counts,
// never its branch, commit, identifiers, paths or hits.
package related

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/filter"
	"github.com/nevzatcirak/review-mcp/internal/gitctx"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/repocfg"
)

// Limits of the related argument (spec §1.1).
const (
	// MaxItems is the most related pull requests of one call.
	MaxItems = 4
	// MaxURLBytes is the longest related URL.
	MaxURLBytes = 2048
)

// Status values of a related item (coverage.related[].status).
const (
	// StatusFull: every changed file was shown.
	StatusFull = "full"
	// StatusPartial: some changed files were listed by path only.
	StatusPartial = "partial"
	// StatusNotPlaced: read, but not in the prompt.
	StatusNotPlaced = "not_placed"
	// StatusFailed: not read (Reason says why).
	StatusFailed = "failed"
	// StatusIgnored: the primary pull request itself, or a duplicate.
	StatusIgnored = "ignored"
	// StatusNoUseFound: a repository item was searched and no use of the
	// changed identifiers was found (v2.5 spec V-13b §1.3 item 2). It never
	// means that nothing is affected: text search does not see dynamic or
	// generated uses.
	StatusNoUseFound = "no_use_found"
)

// KindRepository is coverage.related[].kind of a repository item; a pull
// request item has no kind (V-13b §1.3 item 1).
const KindRepository = string(provider.ItemRepository)

// SourceRepoConfig is coverage.related[].source of an item the repository
// config declares (V-13b §3.2, ReadDeclared).
const SourceRepoConfig = "repo_config"

// Reason values (coverage.related[].reason).
const (
	ReasonURLNotConfigured   = "url_not_configured"
	ReasonURLMalformed       = "url_malformed"
	ReasonCredentialsMissing = "credentials_missing"
	ReasonNotFound           = "not_found"
	ReasonForbidden          = "forbidden"
	ReasonUnauthorized       = "unauthorized"
	ReasonUnreadable         = "unreadable"
	ReasonDuplicate          = "duplicate"
	ReasonPrimary            = "primary"
	ReasonDidNotFit          = "did_not_fit"
	ReasonReviewInParts      = "review_in_parts"
	// ReasonOff: related.max_tokens is 0, so nothing was read.
	//
	// DESIGN-QUESTION (WP-11a DQ-1): the spec turns related pull requests
	// off with related.max_tokens = 0 ("the argument is accepted, nothing is
	// read, one note") but names no status for the items, and every item
	// must be accounted for in coverage.related. They get not_placed with
	// this reason. Recommendation: keep; an item that is in the argument
	// but was not read or shown is not_placed, and the reason says why.
	ReasonOff = "off"

	// Reasons of repository items (v2.5 spec V-13b §1.3 item 2).

	// ReasonRepoContextOff: context.repo.enabled is not set (always so in
	// serve mode), so the repository was not fetched; no git command ran.
	ReasonRepoContextOff = "repo_context_off"
	// ReasonBranchNotFound: the repository has no such branch.
	ReasonBranchNotFound = gitctx.ReasonBranchNotFound
	// ReasonPrimaryRepository: the item names the reviewed pull request's own
	// repository, whose contents are already repository context.
	ReasonPrimaryRepository = "primary_repository"
	// ReasonNoContractChange: the primary diff gave no identifier, so the
	// repository was neither fetched nor searched (V-13b §2.1a).
	ReasonNoContractChange = "no_contract_change"
)

// GitReasons are the failed reasons a repository item takes from gitctx
// (besides branch_not_found and not_found, and auth, which is reported as
// unauthorized): the fetch's fixed reasons, passed on as they are.
var GitReasons = []string{gitctx.ReasonTimeout, gitctx.ReasonTooLarge, gitctx.ReasonRedirect, gitctx.ReasonGitFailed,
	gitctx.ReasonGitUnavailable, gitctx.ReasonBusy, gitctx.ReasonCache, gitctx.ReasonUnsupported}

// Fixed argument errors (spec §1.1 item 2). They never repeat a value.
const (
	errTooMany     = "related must have at most 4 items"
	errCombination = "related cannot be combined with focus or findings"
)

// ArgumentError is an invalid related argument; its text is fixed.
type ArgumentError struct{ msg string }

func (e *ArgumentError) Error() string { return e.msg }

// Normalize returns the URL as the run uses it: an absolute http(s) URL
// with a host and no credentials, at most MaxURLBytes long, without its
// query and fragment, as net/url writes it. ok is false for anything else.
func Normalize(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > MaxURLBytes {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Opaque != "" {
		return "", false
	}
	if s := strings.ToLower(u.Scheme); s != "http" && s != "https" {
		return "", false
	}
	u.RawQuery, u.ForceQuery, u.Fragment, u.RawFragment = "", false, "", ""
	return u.String(), true
}

// Validate checks the related argument before any network call, in the
// spec's order: at most MaxItems items, each a URL Normalize accepts (the
// error names its 0-based index), and no focus or findings with it
// (combined is true when the call has either). It returns the normalised
// URLs, each with the Bitbucket Server branch query kept (keepAt); an empty
// list is not given and returns nil. The error is an *ArgumentError.
func Validate(items []string, combined bool) ([]string, error) {
	if len(items) == 0 {
		return nil, nil
	}
	if len(items) > MaxItems {
		return nil, &ArgumentError{errTooMany}
	}
	out := make([]string, len(items))
	for i, raw := range items {
		u, ok := Normalize(raw)
		if !ok {
			return nil, &ArgumentError{fmt.Sprintf("related[%d] must be an http or https pull request or repository URL without credentials", i)}
		}
		out[i] = keepAt(raw, u)
	}
	if combined {
		return nil, &ArgumentError{errCombination}
	}
	return out, nil
}

// atKey is the one query key a related URL keeps: a Bitbucket Server
// repository URL names its branch with it ("…/browse?at=refs/heads/x",
// v2.5 spec V-13b §1.1 item 4).
const atKey = "at"

// keepAt returns the normalised URL norm of raw with raw's "at" query
// values put back (only that key, in their order), when raw's path has the
// shape of a Bitbucket Server repository URL: it ends in
// /repos/{slug} or /repos/{slug}/browse. Any other URL is norm unchanged, so
// a pull request URL stays exactly as V-13a normalised it. Whether the
// value names a branch is the provider's ParseRepoPath's decision.
//
// DESIGN-QUESTION (WP-13a DQ-2): the related argument is normalised before
// the URL is resolved, so the provider is not known yet. The query is kept
// by the path's shape rather than for every URL, so that no other URL's
// normalised form (the URL coverage.related reports) changes from 2.4.
// Recommendation: keep.
func keepAt(raw, norm string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return norm
	}
	vals, ok := u.Query()[atKey]
	if !ok || !bitbucketRepoPath(u.Path) {
		return norm
	}
	// "/" is legal in a query value; it is kept readable.
	return norm + "?" + strings.ReplaceAll(url.Values{atKey: vals}.Encode(), "%2F", "/")
}

// bitbucketRepoPath reports whether p ends in /repos/{slug} or
// /repos/{slug}/browse.
func bitbucketRepoPath(p string) bool {
	segs := strings.Split(strings.TrimSuffix(p, "/"), "/")
	n := len(segs)
	if n >= 1 && segs[n-1] == "browse" {
		n--
	}
	return n >= 3 && segs[n-2] == "repos" && segs[n-1] != ""
}

// Resolver maps a pull request URL to a reference and a provider: the run's
// resolver (provider.Resolver, wrapped in serve mode by the credential
// check). It performs no network I/O.
type Resolver interface {
	Resolve(rawURL string) (provider.PRRef, provider.Provider, error)
}

// ItemResolver is a Resolver that also resolves repository URLs
// (provider.Resolver.ResolveItem, kept by the tools' wrappers). Read uses it
// for repository items; a resolver without it resolves pull request URLs
// only.
type ItemResolver interface {
	ResolveItem(rawURL string) (provider.ItemKind, provider.PRRef, provider.RepoRef, provider.Provider, error)
}

// BranchFetcher fetches a branch of a repository at the commit the fetch
// brings (gitctx.Runner.EnsureBranch).
type BranchFetcher interface {
	EnsureBranch(ctx context.Context, repo gitctx.Repo, branch string) (gitctx.Checkout, error)
}

// ConsumerSearcher searches a fetched repository for fixed strings
// (gitctx.Runner.SearchTerms).
type ConsumerSearcher interface {
	SearchTerms(ctx context.Context, c gitctx.Checkout, q gitctx.TermQuery) (gitctx.TermResult, error)
}

// Repositories is what Read and Search need for repository items (v2.5
// spec V-13b). A nil *Repositories resolves every URL as a pull request
// URL, as in V-13a (pr_info).
type Repositories struct {
	// Enabled is context.repo.enabled (never set in serve mode). Without it
	// every repository item is failed with repo_context_off and no git
	// command runs.
	Enabled bool
	// Config is the run's configuration, for the provider base URL, token
	// and TLS settings of each repository (gitctx.RepoFor).
	Config *config.Config
	// Fetcher fetches the branches and Searcher searches them. Either nil
	// with Enabled set fails the items with unsupported (a test backend
	// without branches), before any git command.
	Fetcher  BranchFetcher
	Searcher ConsumerSearcher
	// Filter is the operator's filter (ignore.*, generated, lockfile,
	// minified and binary rules), applied to the search (spec §2.2 item 1):
	// the reviewed repository's own config does not reach another
	// repository.
	Filter *filter.Filter
	// ResolveOnly lists the repository items without reading them (pr_info,
	// V-13b spec §4 item 2): they are resolved and checked for the primary's
	// own repository and duplicates, then keep no status and nothing is
	// fetched.
	ResolveOnly bool
}

// RepositoriesFor returns the Repositories of a run with configuration cfg,
// the operator's configuration operator (before the repository config was
// merged; nil uses cfg) and repository-context backend (pipeline
// Deps.RepoContext): nil selects the real git runner, as repoctx.Open does;
// a backend that cannot fetch a branch or search it leaves Fetcher or
// Searcher nil. Nothing is fetched here.
func RepositoriesFor(cfg, operator *config.Config, backend any) *Repositories {
	r := &Repositories{Enabled: cfg.Context.Repo.Enabled, Config: cfg}
	if !r.Enabled {
		return r
	}
	if operator == nil {
		operator = cfg
	}
	if backend == nil {
		backend = gitctx.New(gitctx.OptionsFromConfig(cfg.Context.Repo))
	}
	if b, ok := backend.(BranchFetcher); ok {
		r.Fetcher = b
	}
	if b, ok := backend.(ConsumerSearcher); ok {
		r.Searcher = b
	}
	f, err := filter.New(operator)
	if err != nil {
		// The operator's configuration was validated at start; a filter that
		// cannot be built searches nothing rather than everything.
		r.Searcher = nil
	}
	r.Filter = f
	return r
}

// Item is one related item of a run, in argument order: a pull request or,
// with Kind ItemRepository, a repository.
type Item struct {
	// URL is the normalised URL the caller gave.
	URL string
	// Status and Reason are the item's outcome so far: StatusFailed and
	// StatusIgnored are final; a read item has Read set and gets its status
	// when the prompt is built (full, partial or not_placed); a fetched
	// repository item has Fetched set.
	Status, Reason string
	// Read is set when the pull request and its diff were read.
	Read     bool
	Ref      provider.PRRef
	Provider provider.Provider
	PR       *provider.PullRequest
	Diff     *provider.Diff

	// Kind is provider.ItemRepository for a repository item, "" for a pull
	// request item or an item that was not resolved (off).
	Kind provider.ItemKind
	// Repo is the repository item's reference.
	Repo provider.RepoRef
	// Fetched is set when the repository was fetched; Checkout holds it at
	// its pinned commit (Checkout.HeadSHA) for the whole run.
	Fetched  bool
	Checkout gitctx.Checkout
	// Identifiers, Matches and MatchesShown are the repository item's
	// search counts (V-13b §1.3 item 1): the identifiers searched, every
	// hit found, and the hits placed in the prompt.
	Identifiers, Matches, MatchesShown int
	// Source is SourceRepoConfig for an item from the repository config.
	Source string
	// Paths confines the search of an item from the repository config to
	// these directory prefixes (V-13b §2.2 item 2); nil searches the whole
	// tree.
	Paths []string
	// Searched is set when the repository was searched; Surface holds the
	// identifiers it was searched for, Hits the hits the caps kept (at most
	// MaxHitsPerRepository), and Cut how many hits the caps dropped.
	Searched bool
	Surface  []Identifier
	// StoppedRemovals are removed identifiers only the stop list kept out
	// of the search (StoppedRemovals; spec §4 item 1a).
	StoppedRemovals []string
	Hits            []gitctx.Hit
	Cut             int
	// toolSeen are the files of the repository a repository tool showed the
	// model (MarkSeen, V-13b §3.1).
	toolSeen []string

	// repos is the run's Repositories of a repository item that can be
	// fetched (Search fetches it); nil otherwise.
	repos *Repositories
	// unsearched and limit are the identifiers related.max_identifiers
	// left out of the search, and that limit (the note of spec §2.4 item 3).
	unsearched, limit int
}

// IsRepository reports whether it is a repository item.
func (it *Item) IsRepository() bool { return it.Kind == provider.ItemRepository }

// Name is a repository item's "{namespace}/{name}", one line.
func (it *Item) Name() string { return oneLine(it.Repo.Namespace) + "/" + oneLine(it.Repo.Repo) }

// Commit12 is the first 12 hexadecimal digits of a fetched repository
// item's pinned commit.
func (it *Item) Commit12() string {
	c := it.Checkout.HeadSHA
	if len(c) > 12 {
		c = c[:12]
	}
	return c
}

// Saw reports whether the run saw the file path in the repository item: a
// file of one of its kept hits, or a file a repository tool showed the
// model there (MarkSeen, V-13b §2.4 item 1). A finding located there gets a
// link at the pinned commit; any other path of the repository is
// unverified.
func (it *Item) Saw(path string) bool {
	if !it.Fetched || path == "" {
		return false
	}
	if it.Searched {
		for _, h := range it.Hits {
			if h.Path == path {
				return true
			}
		}
	}
	return slices.Contains(it.toolSeen, path)
}

// SeenFiles returns the files Saw reports, in order: those of the kept hits,
// then those a repository tool showed the model; nil for an item that was
// not fetched.
func (it *Item) SeenFiles() []string {
	if !it.Fetched {
		return nil
	}
	var out []string
	if it.Searched {
		for _, h := range it.Hits {
			if !slices.Contains(out, h.Path) {
				out = append(out, h.Path)
			}
		}
	}
	for _, p := range it.toolSeen {
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// MarkSeen records that a repository tool showed the model the files paths
// of the fetched repository item (V-13b §3.1): Saw reports them from now on.
func (it *Item) MarkSeen(paths ...string) {
	if !it.IsRepository() || !it.Fetched {
		return
	}
	for _, p := range paths {
		if p != "" && !slices.Contains(it.toolSeen, p) {
			it.toolSeen = append(it.toolSeen, p)
		}
	}
}

// SearchFilter is the filter a search of the repository item applies: the
// operator's (Repositories.Filter); nil for an item that is not readable.
func (it *Item) SearchFilter() *filter.Filter {
	if it.repos == nil {
		return nil
	}
	return it.repos.Filter
}

// Read resolves and reads the related items urls (normalised, in argument
// order) of the primary pull request primary (its normalised URL and its
// resolved reference). A URL equal to the primary's, or resolving to the
// same pull request, is ignored with ReasonPrimary; a repeat of an earlier
// item (by URL or by the pull request it resolves to) with
// ReasonDuplicate. Each other item is resolved through r, then read with
// GetPullRequest and GetDiff (include filters the files, as for the
// primary; nil keeps them all), sequentially, under ctx. A failure is the
// item's status and reason, never the run's error. With off set nothing is
// resolved or read: every item that is not ignored is not_placed with
// ReasonOff.
//
// With repos set and r an ItemResolver, an item may also be a repository
// (v2.5 spec V-13b): one naming the primary's own repository is ignored with
// ReasonPrimaryRepository, a repeat of the same repository and branch with
// ReasonDuplicate. Without repos.Enabled each other one is failed with
// ReasonRepoContextOff, and no git command runs. Otherwise it is readable
// and keeps no status: it is fetched and searched by Search, once the
// changed contract surface is known (spec §2.1a), or never.
//
// DESIGN-QUESTION (WP-11a DQ-2): the spec ignores the primary URL and
// duplicates "after normalisation". Two different URLs can name the same
// pull request (".../pulls/7" and ".../pulls/7/files"), so an item is also
// ignored when it resolves to the primary's reference or to an earlier
// item's, before anything is read. Recommendation: keep; the same pull
// request is never read twice nor shown as its own context.
func Read(ctx context.Context, r Resolver, primaryURL string, primary provider.PRRef, urls []string,
	include func(string) bool, off bool, repos *Repositories, log *slog.Logger) []*Item {
	return read(ctx, r, primaryURL, primary, requestsOf(urls), include, off, true, repos, log)
}

// ReadDeclared is Read for the related repositories the reviewed
// repository's config declares (v2.5 spec V-13b §3.2), which a run reads
// only when the call passes no related argument and the operator key
// related.from_repo_config is set: the caller decides. Each item has Source
// SourceRepoConfig, is a repository item whatever happens to it, and keeps
// the declaration's Paths, which confine its search and the tools' reads.
// Its URL goes through the run's resolver like any related URL, so a URL
// that matches no configured provider is failed with url_not_configured
// before any request or git command: a declaration can never add a host.
//
// DESIGN-QUESTION (WP-13c DQ-6): the spec declares repositories only. A
// declared URL that is malformed, or that resolves to a pull request URL,
// is failed with url_malformed; a declaration whose branch differs from the
// branch its URL names is failed with url_malformed too, since either
// choice would read a branch the file does not name unambiguously; the
// same branch twice, or a branch with a URL without one, is that branch.
// Recommendation: keep.
func ReadDeclared(ctx context.Context, r Resolver, primaryURL string, primary provider.PRRef, decls []repocfg.Declared,
	off bool, repos *Repositories, log *slog.Logger) []*Item {
	reqs := make([]request, len(decls))
	for i := range decls {
		reqs[i] = declaredRequest(decls[i])
	}
	return read(ctx, r, primaryURL, primary, reqs, nil, off, true, repos, log)
}

// ResolveDeclared resolves the related repositories the repository config
// declares without reading them (pr_info, V-13b §3.2 item 4): each is a
// repository item with Source SourceRepoConfig; one that resolves to a
// configured provider keeps no status (it would be read), the others are
// failed or ignored as ReadDeclared makes them. Nothing is fetched, and the
// resolver makes no request.
func ResolveDeclared(r Resolver, primary provider.PRRef, decls []repocfg.Declared, log *slog.Logger) []*Item {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	ir, _ := r.(ItemResolver)
	items := make([]*Item, 0, len(decls))
	var seen []provider.RepoRef
	for i, d := range decls {
		rq := declaredRequest(d)
		it := declaredItem(rq)
		items = append(items, it)
		switch {
		case rq.malformed, ir == nil:
			it.Status, it.Reason = StatusFailed, ReasonURLMalformed
		default:
			kind, _, repo, p, err := ir.ResolveItem(rq.url)
			if err != nil {
				it.Status, it.Reason = StatusFailed, reasonOf(err)
				break
			}
			repo, ok := declaredRef(rq.declared, kind, repo)
			if !ok {
				it.Status, it.Reason = StatusFailed, ReasonURLMalformed
				break
			}
			it.Repo, it.Provider = repo, p
			switch {
			case sameRepo(repo, primary):
				it.Status, it.Reason = StatusIgnored, ReasonPrimaryRepository
			case containsRepo(seen, repo):
				it.Status, it.Reason = StatusIgnored, ReasonDuplicate
			default:
				seen = append(seen, repo)
			}
		}
		logRepository(log, i, it)
	}
	return items
}

// declaredRequest is the request of a declaration: its URL as the related
// argument would normalise it, or as written when Normalize refuses it
// (malformed).
func declaredRequest(d repocfg.Declared) request {
	u, ok := Normalize(d.URL)
	if ok {
		u = keepAt(d.URL, u)
	} else {
		u = strings.TrimSpace(d.URL)
	}
	return request{url: u, declared: &d, malformed: !ok}
}

// declaredItem is the item of a declared request before it is resolved: a
// repository item whatever happens to it, with its source, paths and
// declared branch.
func declaredItem(rq request) *Item {
	d := rq.declared
	return &Item{URL: rq.url, Kind: provider.ItemRepository, Source: SourceRepoConfig, Paths: d.Paths,
		Repo: provider.RepoRef{Branch: d.Branch, URL: rq.url}}
}

// declaredRef applies the declaration d to what its URL resolved to: ok is
// false when the URL is not a repository's, or names another branch than
// d (WP-13c DQ-6); d's branch is the repository's otherwise.
func declaredRef(d *repocfg.Declared, kind provider.ItemKind, repo provider.RepoRef) (provider.RepoRef, bool) {
	if kind != provider.ItemRepository || (d.Branch != "" && repo.Branch != "" && d.Branch != repo.Branch) {
		return repo, false
	}
	if d.Branch != "" {
		repo.Branch = d.Branch
	}
	return repo, true
}

// request is one item to read: a URL of the related argument, or a
// declaration of the repository config (declared set; malformed when its URL
// is not one Normalize accepts).
type request struct {
	url       string
	declared  *repocfg.Declared
	malformed bool
}

func requestsOf(urls []string) []request {
	out := make([]request, len(urls))
	for i, u := range urls {
		out[i] = request{url: u}
	}
	return out
}

// ReadPullRequests is Read without the diffs (pr_info, spec V-13a §3.2):
// each related pull request is resolved and read with GetPullRequest only;
// related.max_tokens does not apply, since nothing goes into a prompt.
// A repository URL is resolved, not read (Repositories.ResolveOnly): it
// keeps no status, the caller lists it as not_read (V-13b §4 item 2).
//
// DESIGN-QUESTION (WP-11c DQ-2): related.max_tokens 0 turns related pull
// requests off for the prompts of pr_review and pr_ask. pr_info has no
// prompt and reads no diff, so it reads them whatever the budget.
// Recommendation: keep.
func ReadPullRequests(ctx context.Context, r Resolver, primaryURL string, primary provider.PRRef, urls []string,
	log *slog.Logger) []*Item {
	return read(ctx, r, primaryURL, primary, requestsOf(urls), nil, false, false, &Repositories{ResolveOnly: true}, log)
}

func read(ctx context.Context, r Resolver, primaryURL string, primary provider.PRRef, reqs []request,
	include func(string) bool, off, withDiff bool, repos *Repositories, log *slog.Logger) []*Item {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	resolve := func(u string) (provider.ItemKind, provider.PRRef, provider.RepoRef, provider.Provider, error) {
		ref, p, err := r.Resolve(u)
		return provider.ItemPullRequest, ref, provider.RepoRef{}, p, err
	}
	if ir, ok := r.(ItemResolver); ok && repos != nil {
		resolve = ir.ResolveItem
	}
	items := make([]*Item, 0, len(reqs))
	firstByURL := map[string]*Item{}
	var seenRefs []provider.PRRef
	var seenRepos []provider.RepoRef
	var pending []int // the repository items to fetch, by index
	for i, rq := range reqs {
		u := rq.url
		it := &Item{URL: u}
		if rq.declared != nil {
			it = declaredItem(rq)
		}
		items = append(items, it)
		switch first := firstByURL[u]; {
		case rq.malformed:
			it.Status, it.Reason = StatusFailed, ReasonURLMalformed
		case u == primaryURL:
			it.Status, it.Reason = StatusIgnored, ReasonPrimary
		case first != nil:
			it.Status, it.Reason = StatusIgnored, ReasonDuplicate
			it.Kind, it.Repo = first.Kind, first.Repo
		case off:
			it.Status, it.Reason = StatusNotPlaced, ReasonOff
		}
		if firstByURL[u] == nil {
			firstByURL[u] = it
		}
		if it.Status != "" {
			log.Debug("related pull request", "index", i, "status", it.Status, "reason", it.Reason)
			continue
		}
		kind, ref, repo, p, err := resolve(u)
		if err != nil {
			it.Status, it.Reason = StatusFailed, reasonOf(err)
			log.Debug("related pull request", "index", i, "status", it.Status, "reason", it.Reason)
			continue
		}
		if rq.declared != nil {
			// A declaration must name a repository, and its branch must agree
			// with its URL's (WP-13c DQ-6).
			var ok bool
			if repo, ok = declaredRef(rq.declared, kind, repo); !ok {
				it.Status, it.Reason = StatusFailed, ReasonURLMalformed
				logRepository(log, i, it)
				continue
			}
			kind = provider.ItemRepository
		}
		if kind == provider.ItemRepository {
			it.Kind, it.Repo, it.Provider = kind, repo, p
			switch {
			case sameRepo(repo, primary):
				it.Status, it.Reason = StatusIgnored, ReasonPrimaryRepository
			case containsRepo(seenRepos, repo):
				it.Status, it.Reason = StatusIgnored, ReasonDuplicate
			default:
				seenRepos = append(seenRepos, repo)
				pending = append(pending, i)
				continue
			}
			logRepository(log, i, it)
			continue
		}
		switch {
		case sameRef(ref, primary):
			it.Status, it.Reason = StatusIgnored, ReasonPrimary
		case containsRef(seenRefs, ref):
			it.Status, it.Reason = StatusIgnored, ReasonDuplicate
		}
		if it.Status != "" {
			log.Debug("related pull request", "index", i, "kind", string(ref.Kind), "status", it.Status, "reason", it.Reason)
			continue
		}
		seenRefs = append(seenRefs, ref)
		it.Ref, it.Provider = ref, p
		pr, err := p.GetPullRequest(ctx, ref)
		d := &provider.Diff{}
		if err == nil && withDiff {
			d, err = p.GetDiff(ctx, ref, pr, provider.DiffOptions{Include: include})
		}
		if err != nil {
			it.Status, it.Reason = StatusFailed, reasonOf(err)
			log.Debug("related pull request", "index", i, "kind", string(ref.Kind), "status", it.Status, "reason", it.Reason)
			continue
		}
		it.Read, it.PR, it.Diff = true, pr, d
		log.Debug("related pull request", "index", i, "kind", string(ref.Kind), "status", "read",
			"files", len(d.Files), "skipped", len(d.Skipped))
	}
	for _, i := range pending {
		readable(items[i], repos, log, i)
	}
	return items
}

// readable decides whether the repository item it (index i) can be read:
// only with repos.Enabled (otherwise repo_context_off) and a backend that
// can fetch and search a branch (otherwise unsupported). Nothing is fetched
// here and no git command runs; a readable item keeps no status until
// Search.
func readable(it *Item, repos *Repositories, log *slog.Logger, i int) {
	switch {
	case repos.ResolveOnly:
	case !repos.Enabled:
		it.Status, it.Reason = StatusFailed, ReasonRepoContextOff
	case repos.Fetcher == nil || repos.Searcher == nil:
		it.Status, it.Reason = StatusFailed, gitctx.ReasonUnsupported
	default:
		it.repos = repos
	}
	logRepository(log, i, it)
}

// logRepository logs a repository item's outcome: its index, item kind,
// provider kind, status (readable, fetched or searched when it has none
// yet), reason and counts; never its URL, branch, commit or anything found
// in it.
func logRepository(log *slog.Logger, i int, it *Item) {
	status := it.Status
	switch {
	case status != "":
	case it.Searched:
		status = "searched"
	case it.Fetched:
		status = "fetched"
	default:
		status = "readable"
	}
	log.Debug("related repository", "index", i, "item", KindRepository, "kind", string(it.Repo.Kind),
		"status", status, "reason", it.Reason, "identifiers", it.Identifiers, "matches", it.Matches,
		"matches_shown", it.MatchesShown)
}

// gitReason is the failed reason of a repository fetch: gitctx's fixed
// reason, with auth (git cannot tell 401 from 403 apart once both schemes
// were refused) reported as unauthorized.
func gitReason(err error) string {
	if r := gitctx.ReasonOf(err); r != gitctx.ReasonAuth {
		return r
	}
	return ReasonUnauthorized
}

// sameRepo reports whether repo names the primary pull request's own
// repository (any branch).
func sameRepo(repo provider.RepoRef, primary provider.PRRef) bool {
	return repo.Kind == primary.Kind && strings.EqualFold(repo.Namespace, primary.Namespace) &&
		strings.EqualFold(repo.Repo, primary.Repo) && repo.Kind != ""
}

// containsRepo reports whether repos holds the same repository and branch
// as repo (branch names are case-sensitive; "" is the default branch).
func containsRepo(repos []provider.RepoRef, repo provider.RepoRef) bool {
	for _, r := range repos {
		if r.Kind == repo.Kind && strings.EqualFold(r.Namespace, repo.Namespace) && strings.EqualFold(r.Repo, repo.Repo) &&
			r.Branch == repo.Branch {
			return true
		}
	}
	return false
}

// ReadItems are the items of items that were read, in order.
func ReadItems(items []*Item) []*Item {
	var out []*Item
	for _, it := range items {
		if it.Read {
			out = append(out, it)
		}
	}
	return out
}

func sameRef(a, b provider.PRRef) bool {
	return a.Kind == b.Kind && strings.EqualFold(a.Namespace, b.Namespace) && strings.EqualFold(a.Repo, b.Repo) &&
		a.Number == b.Number && a.Kind != ""
}

func containsRef(refs []provider.PRRef, ref provider.PRRef) bool {
	for _, r := range refs {
		if sameRef(r, ref) {
			return true
		}
	}
	return false
}

// classed is an error that names its X-6 class (tools.RequestError: the
// serve-mode credential check).
type classed interface{ ErrorClass() string }

// reasonOf maps a resolution or read error to a fixed reason (spec §1.2
// item 4): the URL classes, a missing serve-mode credential, not found, an
// authentication answer (403 forbidden, otherwise unauthorized), and
// unreadable for everything else.
func reasonOf(err error) string {
	var c classed
	if errors.As(err, &c) && c.ErrorClass() == ReasonCredentialsMissing {
		return ReasonCredentialsMissing
	}
	var pe *provider.Error
	if !errors.As(err, &pe) {
		return ReasonUnreadable
	}
	switch pe.Class {
	case provider.ClassURLNotConfigured:
		return ReasonURLNotConfigured
	case provider.ClassURLMalformed:
		return ReasonURLMalformed
	case provider.ClassNotFound:
		return ReasonNotFound
	case provider.ClassAuth:
		if pe.Status == 403 {
			return ReasonForbidden
		}
		return ReasonUnauthorized
	}
	return ReasonUnreadable
}

// Notes (spec §1.5 item 2, verbatim); url is the normalised URL, which the
// user gave.

// NoteFailed: the item could not be read.
func NoteFailed(url, reason string) string {
	return "Related pull request " + url + " could not be read (" + reason + ") and was not used."
}

// NoteIgnored: the item is the primary pull request or a duplicate.
func NoteIgnored(url string) string {
	return "Related pull request " + url + " was ignored: it is the reviewed pull request itself or a duplicate."
}

// NoteRepositoryFailed: the repository item could not be fetched.
//
// DESIGN-QUESTION (WP-13a DQ-4): the spec gives notes for searches
// (§2.4) but none for a repository item that is failed or ignored; the
// pull request notes would call it a pull request. These two follow their
// wording with "repository". Recommendation: keep, and list them in the
// troubleshooting docs (WP-13d).
func NoteRepositoryFailed(url, reason string) string {
	return "Related repository " + url + " could not be read (" + reason + ") and was not used."
}

// NoteRepositoryIgnored: the repository item is the reviewed pull request's
// own repository or a duplicate.
func NoteRepositoryIgnored(url string) string {
	return "Related repository " + url + " was ignored: it is the reviewed pull request's own repository or a duplicate."
}

// NotePartial: n of the item's files were listed by path only.
func NotePartial(url string, n int) string {
	return fmt.Sprintf("Related pull request %s did not fit in full; %d of its files are listed by path only.", url, n)
}

// Notes of the whole set.
const (
	NoteNotPlaced = "The related pull requests did not fit the context window next to the diff and were not used."
	NoteInParts   = "The pull request was reviewed in parts; related pull requests need the whole change at once and were not used."
	NoteOff       = "related.max_tokens is 0, so related pull requests were not read."
)
