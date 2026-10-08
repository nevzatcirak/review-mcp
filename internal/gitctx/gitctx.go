// Package gitctx fetches the head of a pull request into a local, cached,
// bare git repository so that code outside the pull request can be searched
// (repository context, X-22 / RC-1 to RC-10 of
// docs/design/v1.1-repo-context.md).
//
// It runs the system git binary through os/exec, with arguments only and
// never a shell (RC-2). The provider token reaches git only through the
// environment of the one child process that needs it, as GIT_CONFIG_COUNT /
// GIT_CONFIG_KEY_n / GIT_CONFIG_VALUE_n (http.extraHeader); it never appears
// in an argument, on disk, in a log or in an error (RC-3).
//
// Every git process gets a fresh environment from an allowlist, never the
// parent's: PATH; SYSTEMROOT on Windows; the proxy variables (http_proxy,
// https_proxy, no_proxy, all_proxy, in both cases) when set; HOME and
// XDG_CONFIG_HOME (and USERPROFILE on Windows) pointing at the cache's empty
// <cache_dir>/.home, never the user's real home, so the user's global git
// configuration is not read; GIT_CONFIG_NOSYSTEM=1 except on Windows (Git
// for Windows keeps its TLS backend and CA settings in the system
// configuration); LANG=C; GIT_TERMINAL_PROMPT=0 and empty GIT_ASKPASS and
// SSH_ASKPASS; and the GIT_CONFIG_* entries. The provider's ca_cert and
// insecure_skip_verify are mirrored as http.sslCAInfo (with
// http.sslBackend=openssl on Windows) and http.sslVerify=false.
//
// The clone URL is built from the configured provider base URL and the
// resolved repository only (RC-4). Before any network command, git expands
// the remote's URL through every url.*.insteadOf it would apply (ls-remote
// --get-url, no network, no credential); anything but the pinned URL stops
// the fetch with reason redirect. The fetched commit must equal the pull
// request's head SHA (RC-5). The cache, one entry per
// host/base path/namespace/repository, sweeps idle and least-recently-used
// repositories at the start of every use (RC-6).
//
// Every failure is an *Error that carries a fixed reason and nothing else:
// git's stderr is classified and dropped, never passed on.
//
// Search (WP-11b): ExtractSymbols reads the changed symbols from the diff
// (RC-7) and Grep finds their uses with git grep on the fetched head SHA. A
// search never touches the network: its git processes carry no credential,
// forbid every protocol and set GIT_NO_LAZY_FETCH=1, so a blob the partial
// clone lacks is skipped and counted, never fetched.
//
// The package does not depend on internal/review.
package gitctx

import (
	"context"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// Defaults of the context.repo.* keys this package uses. They match
// config.Defaults; Options with a zero value fall back to them.
const (
	DefaultIdleDays     = 7
	DefaultMaxCacheMB   = 2048
	DefaultMaxRepoMB    = 500
	DefaultFetchTimeout = 60 * time.Second
)

// Repo identifies the repository of a pull request and how to reach it.
//
// Everything that forms the clone URL comes from the configuration and the
// already-resolved PR reference (RC-4): Kind and BaseURL from the provider
// configuration, Namespace and Name from provider.PRRef. Nothing from a PR
// body or comment may be put here.
type Repo struct {
	// Kind selects the provider's clone URL layout, PR ref and auth schemes.
	Kind provider.Kind
	// BaseURL is the configured provider base URL (gitea.base_url or
	// bitbucket_server.base_url, including any context path).
	BaseURL string
	// Namespace is the Gitea owner or the Bitbucket project key. It may hold
	// "/" (nested groups); the clone URL keeps the "/" and escapes each
	// segment, and the cache directory is the segments joined with "+".
	Namespace string
	// Name is the repository name (Gitea) or slug (Bitbucket Server).
	Name string
	// Token is the provider token. It is revealed only to build the
	// environment of the fetch process.
	Token config.Secret
	// Identity returns the user name of the token's user (provider
	// CurrentUser: X-AUSERNAME on Bitbucket Server, GET /api/v1/user on
	// Gitea). It is called only when the first auth scheme is answered with
	// HTTP 401 and HTTP Basic is tried (§3.0); nil disables that fallback.
	Identity func(ctx context.Context) (string, error)
	// CACert is the provider's ca_cert path ("" for the system roots).
	CACert string
	// InsecureSkipVerify mirrors the provider's insecure_skip_verify.
	InsecureSkipVerify bool
}

// PR identifies the pull request whose head is fetched.
type PR struct {
	// Number is the pull request number (Gitea index, Bitbucket id).
	Number int64
	// HeadSHA is the head commit reported by the provider API. The fetched
	// commit must equal it (RC-5).
	HeadSHA string
}

// Checkout is a fetched pull request head in the cache. The directory is a
// bare repository; nothing is checked out. It stays valid until the cache
// sweeps it: the LRU sweep never removes a repository used within the last
// lockStaleAfter, so a Grep shortly after Ensure finds it.
type Checkout struct {
	// GitDir is the bare repository.
	GitDir string
	// HeadSHA is the verified head commit (lower case). Searches use it as a
	// tree-ish, never a working tree.
	HeadSHA string
	// Ref is the local ref that holds HeadSHA, refs/review-mcp/pr/<n>.
	Ref string
}

// Options configure a Runner. Zero values select the defaults.
type Options struct {
	// CacheDir is context.repo.cache_dir; "" selects DefaultCacheDir.
	CacheDir string
	// IdleDays is context.repo.idle_days.
	IdleDays int
	// MaxCacheBytes is context.repo.max_cache_mb in bytes.
	MaxCacheBytes int64
	// MaxRepoBytes is context.repo.max_repo_mb in bytes.
	MaxRepoBytes int64
	// FetchTimeout is context.repo.fetch_timeout_seconds. It bounds the wait
	// for the repository lock plus the fetch, including an auth retry.
	FetchTimeout time.Duration
	// GitPath is the git binary; "" looks up "git" in PATH. The version is
	// checked once per process and binary.
	GitPath string
}

// OptionsFromConfig maps the context.repo.* configuration to Options.
func OptionsFromConfig(c config.ContextRepo) Options {
	return Options{
		CacheDir:      c.CacheDir,
		IdleDays:      c.IdleDays,
		MaxCacheBytes: int64(c.MaxCacheMB) << 20,
		MaxRepoBytes:  int64(c.MaxRepoMB) << 20,
		FetchTimeout:  time.Duration(c.FetchTimeoutSeconds) * time.Second,
	}
}

// Runner fetches pull request heads into the cache. It is safe for
// concurrent use; several processes may share one cache directory.
type Runner struct {
	opts Options
}

// New returns a Runner for opts.
func New(opts Options) *Runner {
	if opts.IdleDays <= 0 {
		opts.IdleDays = DefaultIdleDays
	}
	if opts.MaxCacheBytes <= 0 {
		opts.MaxCacheBytes = DefaultMaxCacheMB << 20
	}
	if opts.MaxRepoBytes <= 0 {
		opts.MaxRepoBytes = DefaultMaxRepoMB << 20
	}
	if opts.FetchTimeout <= 0 {
		opts.FetchTimeout = DefaultFetchTimeout
	}
	return &Runner{opts: opts}
}

// RepoFor builds the Repo of ref from the configuration. p supplies the
// token user's name for the HTTP Basic fallback (provider.CurrentUser). ok
// is false when ref's provider is not enabled.
func RepoFor(cfg *config.Config, ref provider.PRRef, p provider.Provider) (repo Repo, ok bool) {
	repo = Repo{Kind: ref.Kind, Namespace: ref.Namespace, Name: ref.Repo}
	switch ref.Kind {
	case provider.KindGitea:
		repo.BaseURL, repo.Token = cfg.Gitea.BaseURL, cfg.Secrets.GiteaToken
		repo.CACert, repo.InsecureSkipVerify = cfg.Gitea.CACert, cfg.Gitea.InsecureSkipVerify
	case provider.KindBitbucketServer:
		repo.BaseURL, repo.Token = cfg.BitbucketServer.BaseURL, cfg.Secrets.BitbucketServerToken
		repo.CACert, repo.InsecureSkipVerify = cfg.BitbucketServer.CACert, cfg.BitbucketServer.InsecureSkipVerify
	default:
		return Repo{}, false
	}
	if repo.BaseURL == "" {
		return Repo{}, false
	}
	if p != nil {
		repo.Identity = func(ctx context.Context) (string, error) {
			u, err := p.CurrentUser(ctx)
			return u.Name, err
		}
	}
	return repo, true
}
