package gitctx

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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
	// branchRefFmt is the prefix of a fetched branch's local ref (v2.5 spec
	// V-13b §1.2); branchRef names the rest.
	branchRefFmt = "refs/review-mcp/branch/"
)

// plan is everything Ensure and EnsureBranch derive from a Repo and a PR or
// a branch before any I/O.
type plan struct {
	cloneURL string
	// remoteRef is refs/pull/{n}/head, refs/pull-requests/{n}/from or
	// refs/merge-requests/{n}/head for a pull request; refs/heads/{branch}
	// or HEAD for a branch.
	remoteRef string
	localRef  string
	rel       string // <host>/<base>/<namespace dir>/<repo> under the cache directory
	// headSHA is the commit the fetch must bring (a pull request's head);
	// "" for a branch, whose fetched commit is the pin.
	headSHA string
	// branch is set for a branch fetch: a missing remote ref is then
	// branch_not_found rather than not_found.
	branch   bool
	net      netPolicy
	schemes  []scheme
	cacheKey string // the auth-scheme cache key
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

// nsDirSep joins the segments of a nested namespace ("group/sub/team") into
// one cache directory name. It is not a segmentRE character, so the mapping
// is injective, and it is safe in a directory name on every OS. A namespace
// of one segment is its own directory name, as before.
const nsDirSep = "+"

// safeNamespace reports whether every "/"-separated segment of ns is a
// safeSegment (so there is no empty, "." or ".." segment).
func safeNamespace(ns string) bool {
	for _, s := range strings.Split(ns, "/") {
		if !safeSegment(s) {
			return false
		}
	}
	return true
}

// namespaceDir is the single cache directory name of ns. The cache depth
// stays fixed, so that an entry can never lie inside another one's
// directory (see the layout in cache.go).
func namespaceDir(ns string) string {
	return strings.ReplaceAll(ns, "/", nsDirSep)
}

// maxBaseSegment is the longest escaped base path kept readable; a longer
// one is replaced by a hash.
const maxBaseSegment = 100

// baseSegment turns the escaped path of the base URL ("" or "/bitbucket",
// "/a/b", "/git%20x") into one cache directory name that is a safeSegment.
//
// The mapping is injective, so two different base paths never share an
// entry:
//   - no path at all is "_";
//   - otherwise ASCII letters and digits are kept, and so are "." and "-"
//     except as the first character (no ".", "..", hidden or option-like
//     names); every other byte, "_" and "/" included, is "_XX" with XX its
//     upper-case hex code. So "a/b" is "a_2Fb" and "a_b" is "a_5Fb";
//   - a result longer than maxBaseSegment is "_h" followed by 40 lower-case
//     hex digits of the path's SHA-256 ("h" is never the first character of
//     an escape, and a kept result never contains "_h").
//
// Case is kept: on a case-insensitive file system "/BB" and "/bb" share a
// directory, as they already do for namespaces; the SHA check (RC-5) keeps
// such a shared entry correct.
func baseSegment(escapedPath string) string {
	p := strings.TrimPrefix(escapedPath, "/")
	if p == "" {
		return "_"
	}
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			(c == '.' || c == '-') && i > 0:
			b.WriteByte(c)
		default:
			b.WriteString("_" + strings.ToUpper(hex.EncodeToString([]byte{c})))
		}
	}
	if b.Len() > maxBaseSegment {
		sum := sha256.Sum256([]byte(p))
		return "_h" + hex.EncodeToString(sum[:20])
	}
	return b.String()
}

// newPlan validates repo and pr and builds the clone URL (RC-4), the PR ref
// (RC-5) and the cache path.
func newPlan(repo Repo, pr PR) (*plan, error) {
	unsupported := fail(ReasonUnsupported)
	if pr.Number <= 0 {
		return nil, unsupported
	}
	sha := strings.ToLower(strings.TrimSpace(pr.HeadSHA))
	if !shaRE.MatchString(sha) {
		return nil, unsupported
	}
	p, err := newRepoPlan(repo)
	if err != nil {
		return nil, err
	}
	n := strconv.FormatInt(pr.Number, 10)
	p.localRef, p.headSHA = localRefFmt+n, sha
	switch repo.Kind {
	case provider.KindGitea, provider.KindGitHub:
		p.remoteRef = "refs/pull/" + n + "/head"
	case provider.KindBitbucketServer:
		p.remoteRef = "refs/pull-requests/" + n + "/from"
	case provider.KindGitLab:
		// Y-21: the merge request ref is fixed.
		p.remoteRef = "refs/merge-requests/" + n + "/head"
	}
	return p, nil
}

// newBranchPlan validates repo and branch and builds the plan of a branch
// fetch (v2.5 spec V-13b §1.2): the repository's clone URL, cache path and
// auth schemes, exactly as for its pull requests, with the remote ref
// refs/heads/{branch} (HEAD for "", the remote's default branch) and the
// local ref branchRef(branch). A branch that is not a
// provider.ValidBranchName is unsupported before any I/O; callers check it
// earlier (a related item's URL is url_malformed).
func newBranchPlan(repo Repo, branch string) (*plan, error) {
	if branch != "" && !provider.ValidBranchName(branch) {
		return nil, fail(ReasonUnsupported)
	}
	p, err := newRepoPlan(repo)
	if err != nil {
		return nil, err
	}
	p.remoteRef, p.localRef, p.branch = "HEAD", branchRef(branch), true
	if branch != "" {
		p.remoteRef = "refs/heads/" + branch
	}
	return p, nil
}

// branchRef is the local ref of a fetched branch:
// refs/review-mcp/branch/{the first 16 hex digits of the branch name's
// SHA-256}, or refs/review-mcp/branch/default for the default branch (""),
// so that a branch name never forms a path or a ref expression of its own.
func branchRef(branch string) string {
	if branch == "" {
		return branchRefFmt + "default"
	}
	sum := sha256.Sum256([]byte(branch))
	return branchRefFmt + hex.EncodeToString(sum[:])[:16]
}

// newRepoPlan validates repo and builds what every fetch of it shares: the
// clone URL (RC-4), the auth schemes, the network policy and the cache
// path. One bare repository serves its pull requests and its branches.
func newRepoPlan(repo Repo) (*plan, error) {
	unsupported := fail(ReasonUnsupported)
	if !safeNamespace(repo.Namespace) || !safeSegment(repo.Name) {
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
	p := &plan{
		net: netPolicy{
			allowHTTP:          urlScheme == "http",
			caCert:             repo.CACert,
			insecureSkipVerify: repo.InsecureSkipVerify,
		},
	}
	base := u.String()
	ns, name := provider.EscapeNamespace(repo.Namespace), url.PathEscape(repo.Name)
	switch repo.Kind {
	case provider.KindGitea:
		p.cloneURL = base + "/" + ns + "/" + name + ".git"
		p.schemes = []scheme{schemeToken, schemeBasic}
	case provider.KindBitbucketServer:
		p.cloneURL = base + "/scm/" + ns + "/" + name + ".git"
		p.schemes = []scheme{schemeBearer, schemeBasic}
	case provider.KindGitHub:
		// Y-16. The base is the web base, never the API base. GitHub does
		// not accept a bare Bearer token for git over HTTPS, so Basic with
		// the fixed user comes first; Bearer is only the second attempt.
		p.cloneURL = base + "/" + ns + "/" + name + ".git"
		p.schemes = []scheme{schemeAccessToken, schemeBearer}
	case provider.KindGitLab:
		// Y-21. The base is the web base (it may carry a context path) and
		// the namespace may be nested groups. GitLab takes a token as the
		// password of HTTP Basic with any user name: "oauth2", the fixed
		// one, as GitLab documents it.
		p.cloneURL = base + "/" + ns + "/" + name + ".git"
		p.schemes = []scheme{schemeOAuth2}
	default:
		return nil, unsupported
	}
	host := hostRE.ReplaceAllString(strings.ToLower(u.Hostname()), "_")
	if port := u.Port(); port != "" {
		host += "_" + port
	}
	// A host never starts with "." (the cache's .home lives beside the hosts).
	if !safeSegment(host) || strings.HasPrefix(host, ".") {
		return nil, unsupported
	}
	p.rel = filepath.Join(host, baseSegment(u.EscapedPath()), namespaceDir(repo.Namespace), repo.Name)
	p.cacheKey = string(repo.Kind) + "\x00" + base
	return p, nil
}

// ---- auth schemes (§3.0 items 1 and 2) ----

type scheme int

const (
	schemeToken       scheme = iota // Gitea: "Authorization: token <token>"
	schemeBearer                    // Bitbucket Server: "Authorization: Bearer <token>"
	schemeBasic                     // Gitea, Bitbucket Server: HTTP Basic, user = token identity, password = token
	schemeAccessToken               // GitHub: HTTP Basic, user = accessTokenUser, password = token
	schemeOAuth2                    // GitLab: HTTP Basic, user = oauth2User, password = token
)

// accessTokenUser is the fixed Basic user of GitHub's git over HTTPS.
const accessTokenUser = "x-access-token"

// oauth2User is the fixed Basic user of GitLab's git over HTTPS.
const oauth2User = "oauth2"

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
	case schemeAccessToken:
		return "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(accessTokenUser+":"+tok)), nil
	case schemeOAuth2:
		return "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(oauth2User+":"+tok)), nil
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
	return r.ensure(ctx, repo, p)
}

// EnsureBranch makes one branch of repo available in the cache, at the
// commit the fetch brings, and returns it (v2.5 spec V-13b §1.2): a related
// repository is read at that one commit for the whole run.
//
// It is Ensure with another ref, and every rule of Ensure holds unchanged:
// the cache entry (the same one as the repository's pull requests), the
// lock, the sweep, the redirect guard, the credential only in the fetch's
// environment, the auth scheme order, the partial-clone filter, --depth=1,
// no tags, no submodules, MaxRepoBytes and FetchTimeout. The remote ref is
// refs/heads/{branch}, or the remote's HEAD for "" (its default branch);
// the local ref is branchRef(branch), never the branch name itself. There
// is no SHA to compare with: the fetched commit is the pin, and
// Checkout.HeadSHA reports it.
//
// A remote without the branch is branch_not_found; every other failure has
// Ensure's reasons. branch must be "" or a provider.ValidBranchName
// (otherwise unsupported, before git runs).
func (r *Runner) EnsureBranch(ctx context.Context, repo Repo, branch string) (Checkout, error) {
	if err := ctx.Err(); err != nil {
		return Checkout{}, err
	}
	p, err := newBranchPlan(repo, branch)
	if err != nil {
		return Checkout{}, err
	}
	return r.ensure(ctx, repo, p)
}

// ensure fetches p.remoteRef into p.localRef of the repository's cache
// entry under the entry's lock, and returns the checkout. With p.headSHA
// set the fetched commit must equal it.
func (r *Runner) ensure(ctx context.Context, repo Repo, p *plan) (Checkout, error) {
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
	g := &gitRun{path: probe.path, gitDir: gitDir, dir: entry, home: homeDir(root)}
	if err := g.prepare(ctx, p); err != nil {
		return Checkout{}, err
	}
	if err := g.checkURL(ctx, p); err != nil {
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
	if p.headSHA != "" && got != p.headSHA {
		return Checkout{}, fail(ReasonSHAMismatch)
	}
	if !shaRE.MatchString(got) {
		return Checkout{}, fail(ReasonGitFailed)
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
	// home is the cache's empty home directory.
	home string
}

func (g *gitRun) run(ctx context.Context, c gitCmd) result {
	c.args = append([]string{"--git-dir=" + g.gitDir}, c.args...)
	c.dir, c.home = g.dir, g.home
	return run(ctx, g.path, c)
}

// checkURL is the redirect guard that runs before any network command: git
// expands the URL the fetch will use (remote origin, through every
// url.*.insteadOf of every configuration git reads, including a Windows
// system configuration) without any network access, and it must be exactly
// the pinned clone URL. Otherwise nothing is sent and the reason is
// redirect.
//
// It runs with the environment of the fetch minus the credential: the same
// HOME, system-configuration setting and extra entries, and no
// http.extraHeader. "origin" rather than the URL itself is expanded, so a
// second remote.origin.url from a configuration file (fetch uses the first)
// is caught as well.
func (g *gitRun) checkURL(ctx context.Context, p *plan) error {
	res := g.run(ctx, gitCmd{args: []string{"ls-remote", "--get-url", "origin"}})
	if res.err != nil {
		_, err := res.failure()
		return err
	}
	if strings.TrimRight(string(res.stdout), "\r\n") != p.cloneURL {
		return fail(ReasonRedirect)
	}
	return nil
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
		res := run(ctx, g.path, gitCmd{args: []string{"init", "--bare", "--quiet", g.gitDir}, dir: g.dir, home: g.home})
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

// fetch fetches the plan's remote ref (a PR ref or a branch) with the first auth scheme and, on HTTP 401, once
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
		if c.missingRef && p.branch {
			return fail(ReasonBranchNotFound)
		}
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
