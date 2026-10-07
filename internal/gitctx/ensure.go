package gitctx

import (
	"context"
	"encoding/base64"
	"errors"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// Fetch settings (RC-5).
const (
	fetchFilter = "blob:limit=1m"
	localRefFmt = "refs/review-mcp/pr/"
)

// plan is everything Ensure derives from a Repo and a PR before any I/O.
type plan struct {
	cloneURL  string
	remoteRef string // refs/pull/{n}/head or refs/pull-requests/{n}/from
	localRef  string
	rel       string // <host>/<namespace>/<repo> under the cache directory
	headSHA   string
	net       netPolicy
	schemes   []scheme
	cacheKey  string // the auth-scheme cache key
}

var (
	// segmentRE is a namespace or repository name that is also a safe
	// directory name on every OS. Gitea and Bitbucket names stay inside it.
	segmentRE = regexp.MustCompile(`^[A-Za-z0-9._~-]{1,100}$`)
	shaRE     = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	hostRE    = regexp.MustCompile(`[^a-z0-9.-]`)
)

func safeSegment(s string) bool {
	return segmentRE.MatchString(s) && s != "." && s != ".."
}

// newPlan validates repo and pr and builds the clone URL (RC-4), the PR ref
// (RC-5) and the cache path.
func newPlan(repo Repo, pr PR) (*plan, error) {
	unsupported := fail(ReasonUnsupported)
	if !safeSegment(repo.Namespace) || !safeSegment(repo.Name) || pr.Number <= 0 {
		return nil, unsupported
	}
	sha := strings.ToLower(strings.TrimSpace(pr.HeadSHA))
	if !shaRE.MatchString(sha) {
		return nil, unsupported
	}
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(repo.BaseURL), "/"))
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Hostname() == "" {
		return nil, unsupported
	}
	urlScheme := strings.ToLower(u.Scheme)
	if urlScheme != "https" && urlScheme != "http" {
		return nil, unsupported
	}
	n := strconv.FormatInt(pr.Number, 10)
	p := &plan{
		localRef: localRefFmt + n,
		headSHA:  sha,
		net: netPolicy{
			allowHTTP:          urlScheme == "http",
			caCert:             repo.CACert,
			insecureSkipVerify: repo.InsecureSkipVerify,
		},
	}
	base := u.String()
	ns, name := url.PathEscape(repo.Namespace), url.PathEscape(repo.Name)
	switch repo.Kind {
	case provider.KindGitea:
		p.cloneURL = base + "/" + ns + "/" + name + ".git"
		p.remoteRef = "refs/pull/" + n + "/head"
		p.schemes = []scheme{schemeToken, schemeBasic}
	case provider.KindBitbucketServer:
		p.cloneURL = base + "/scm/" + ns + "/" + name + ".git"
		p.remoteRef = "refs/pull-requests/" + n + "/from"
		p.schemes = []scheme{schemeBearer, schemeBasic}
	default:
		return nil, unsupported
	}
	host := hostRE.ReplaceAllString(strings.ToLower(u.Hostname()), "_")
	if port := u.Port(); port != "" {
		host += "_" + port
	}
	if !safeSegment(host) {
		return nil, unsupported
	}
	p.rel = filepath.Join(host, repo.Namespace, repo.Name)
	p.cacheKey = string(repo.Kind) + "\x00" + base
	return p, nil
}

// ---- auth schemes (§3.0 items 1 and 2) ----

type scheme int

const (
	schemeToken  scheme = iota // Gitea: "Authorization: token <token>"
	schemeBearer               // Bitbucket Server: "Authorization: Bearer <token>"
	schemeBasic                // both: HTTP Basic, user = token identity, password = token
)

// workingScheme remembers, per provider kind and base URL, the scheme that
// fetched successfully. It lives for the process.
var workingScheme sync.Map // cacheKey -> scheme

// order returns the schemes to try: the one that worked before first.
func (p *plan) order() []scheme {
	v, ok := workingScheme.Load(p.cacheKey)
	if !ok {
		return p.schemes
	}
	first := v.(scheme)
	out := []scheme{first}
	for _, s := range p.schemes {
		if s != first {
			out = append(out, s)
		}
	}
	return out
}

// authHeader builds the http.extraHeader value for s. The value goes into
// the fetch process's environment only.
func authHeader(ctx context.Context, repo Repo, s scheme) (string, error) {
	tok := repo.Token.Reveal()
	if tok == "" || strings.ContainsAny(tok, "\r\n\x00") {
		return "", fail(ReasonAuth)
	}
	switch s {
	case schemeToken:
		return "Authorization: token " + tok, nil
	case schemeBearer:
		return "Authorization: Bearer " + tok, nil
	}
	if repo.Identity == nil {
		return "", fail(ReasonAuth)
	}
	user, err := repo.Identity(ctx)
	if err != nil || user == "" || strings.ContainsAny(user, ":\r\n\x00") {
		if errors.Is(err, context.Canceled) {
			return "", err
		}
		return "", fail(ReasonAuth)
	}
	return "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+tok)), nil
}

// ---- Ensure ----

// Ensure makes the head of pr available in the cache and returns it.
//
// At the start of every use the cache is swept (RC-6). The pull request ref
// is fetched with --depth=1 --filter=blob:limit=1m --no-tags into the
// repository's bare cache entry, and the fetched commit must equal
// pr.HeadSHA. The wait for the entry's lock plus the fetch is limited by
// Options.FetchTimeout, and the repository by Options.MaxRepoBytes (measured
// after the fetch; a larger repository is removed).
//
// Every failure is an *Error with a fixed reason (a canceled ctx returns
// ctx.Err()); none is fatal to a review, which then runs without repository
// context.
func (r *Runner) Ensure(ctx context.Context, repo Repo, pr PR) (Checkout, error) {
	if err := ctx.Err(); err != nil {
		return Checkout{}, err
	}
	p, err := newPlan(repo, pr)
	if err != nil {
		return Checkout{}, err
	}
	probe := gitProbe(r.opts.GitPath)
	if !probe.ok() {
		return Checkout{}, fail(ReasonGitUnavailable)
	}
	root, err := r.openRoot(true)
	if err != nil {
		return Checkout{}, err
	}
	entry := filepath.Join(root, p.rel)
	if !within(root, entry) {
		return Checkout{}, fail(ReasonUnsupported)
	}
	if err := os.MkdirAll(entry, 0o700); err != nil || !realDirs(root, p.rel) {
		return Checkout{}, fail(ReasonCache)
	}

	ctx, cancel := context.WithTimeout(ctx, r.opts.FetchTimeout)
	defer cancel()
	lock, err := acquireLock(ctx, filepath.Join(entry, lockName))
	if err != nil {
		return Checkout{}, err
	}
	defer lock.release()

	r.sweep(root, entry)
	if err := touch(filepath.Join(entry, markerName)); err != nil {
		return Checkout{}, fail(ReasonCache)
	}

	gitDir := filepath.Join(entry, gitDirName)
	g := &gitRun{path: probe.path, gitDir: gitDir, dir: entry}
	if err := g.prepare(ctx, p); err != nil {
		return Checkout{}, err
	}
	if err := g.fetch(ctx, repo, p); err != nil {
		if ReasonOf(err) == ReasonGitFailed {
			// A broken repository heals on the next use.
			_ = removeEntryLocked(root, entry)
		}
		return Checkout{}, err
	}
	got, err := g.resolve(ctx, p.localRef)
	if err != nil {
		return Checkout{}, err
	}
	if got != p.headSHA {
		return Checkout{}, fail(ReasonSHAMismatch)
	}
	if treeSize(gitDir) > r.opts.MaxRepoBytes {
		_ = removeEntryLocked(root, entry)
		return Checkout{}, fail(ReasonTooLarge)
	}
	if err := touch(filepath.Join(entry, markerName)); err != nil {
		return Checkout{}, fail(ReasonCache)
	}
	return Checkout{GitDir: gitDir, HeadSHA: got, Ref: p.localRef}, nil
}

// gitRun runs git against one cache entry's bare repository.
type gitRun struct {
	path, gitDir, dir string
}

func (g *gitRun) run(ctx context.Context, c gitCmd) result {
	c.args = append([]string{"--git-dir=" + g.gitDir}, c.args...)
	c.dir = g.dir
	return run(ctx, g.path, c)
}

// prepare creates the bare repository on first use and pins its remote to
// the clone URL. The remote is a promisor so that --filter is allowed. No
// credential is ever written here.
func (g *gitRun) prepare(ctx context.Context, p *plan) error {
	fi, err := os.Lstat(g.gitDir)
	if err == nil && (fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir()) {
		// Never let git follow a planted link: drop it and start over.
		if os.Remove(g.gitDir) != nil {
			return fail(ReasonCache)
		}
		err = fs.ErrNotExist
	}
	if errors.Is(err, fs.ErrNotExist) {
		res := run(ctx, g.path, gitCmd{args: []string{"init", "--bare", "--quiet", g.gitDir}, dir: g.dir})
		if res.err != nil {
			_, err := res.failure()
			return err
		}
	}
	for _, kv := range [][2]string{
		{"core.repositoryformatversion", "1"},
		{"extensions.partialClone", "origin"},
		{"remote.origin.url", p.cloneURL},
		{"remote.origin.promisor", "true"},
		{"remote.origin.partialclonefilter", fetchFilter},
	} {
		if res := g.run(ctx, gitCmd{args: []string{"config", kv[0], kv[1]}}); res.err != nil {
			_, err := res.failure()
			return err
		}
	}
	return nil
}

// fetch fetches the PR ref with the first auth scheme and, on HTTP 401, once
// more with the next (§3.0). The scheme that worked is remembered for the
// process. The header reaches git only through the environment.
func (g *gitRun) fetch(ctx context.Context, repo Repo, p *plan) error {
	args := []string{
		"fetch", "--depth=1", "--filter=" + fetchFilter, "--no-tags",
		"--no-write-fetch-head", "--no-recurse-submodules", "--quiet",
		"origin", "+" + p.remoteRef + ":" + p.localRef,
	}
	order := p.order()
	var lastErr error
	for i, s := range order {
		if i > 1 {
			break
		}
		header, err := authHeader(ctx, repo, s)
		if err != nil {
			if lastErr != nil {
				return lastErr
			}
			return err
		}
		res := g.run(ctx, gitCmd{
			args: args,
			// The empty value first resets any extraHeader from the user's
			// own git configuration.
			config: [][2]string{{"http.extraHeader", ""}, {"http.extraHeader", header}},
			net:    &p.net,
		})
		if res.err == nil {
			workingScheme.Store(p.cacheKey, s)
			return nil
		}
		c, err := res.failure()
		if !c.unauthorized {
			return err
		}
		lastErr = err
	}
	return lastErr
}

// resolve returns the commit a local ref points to.
func (g *gitRun) resolve(ctx context.Context, ref string) (string, error) {
	res := g.run(ctx, gitCmd{args: []string{"rev-parse", "--verify", "--quiet", ref + "^{commit}"}})
	if res.err != nil {
		_, err := res.failure()
		return "", err
	}
	return strings.ToLower(strings.TrimSpace(string(res.stdout))), nil
}
