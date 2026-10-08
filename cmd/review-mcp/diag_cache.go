package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/nevzatcirak/review-mcp/internal/gitctx"
)

// cacheReport is the diag cache JSON document (RC-6).
type cacheReport struct {
	CacheDir string `json:"cache_dir"`
	// Git is "git <version>" or "unavailable: <fixed reason>".
	Git        string `json:"git"`
	Enabled    bool   `json:"enabled"`
	IdleDays   int    `json:"idle_days"`
	MaxCacheMB int    `json:"max_cache_mb"`
	TotalBytes int64  `json:"total_bytes"`
	// Pruned lists the repositories --prune removed; absent without it.
	Pruned *[]gitctx.Entry `json:"pruned,omitempty"`
	Repos  []gitctx.Entry  `json:"repos"`
}

// diagCacheFailed is printed when the cache directory cannot be read. The
// error itself carries only a fixed reason.
const diagCacheFailed = "diag cache: the cache directory cannot be used: it is not a review-mcp cache (it has files but no CACHEDIR.TAG of review-mcp) or it cannot be read"

// runDiagCache implements "review-mcp diag cache [--prune]": it lists the
// repository-context cache and, with --prune, first runs the idle and LRU
// sweeps that every use runs. It works whether or not
// context.repo.enabled is set, so a disabled cache can still be cleared.
func runDiagCache(args []string, stdout, stderr io.Writer, load configLoader) int {
	fs := flag.NewFlagSet("diag cache", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { diagUsage(stderr) }
	prune := fs.Bool("prune", false, "delete repositories idle longer than context.repo.idle_days, then least-recently-used ones above context.repo.max_cache_mb")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "diag cache: takes no arguments besides --prune")
		diagUsage(stderr)
		return 2
	}
	cfg, _, code := diagSetup(load, stderr)
	if cfg == nil {
		return code
	}
	rc := cfg.Context.Repo
	r := gitctx.New(gitctx.OptionsFromConfig(rc))
	dir, err := r.CacheDir()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, diagCacheFailed)
		return 1
	}
	rep := cacheReport{
		CacheDir: dir, Git: gitctx.GitStatus(""), Enabled: rc.Enabled,
		IdleDays: rc.IdleDays, MaxCacheMB: rc.MaxCacheMB,
	}
	if *prune {
		pruned, err := r.Prune()
		if err != nil {
			_, _ = fmt.Fprintln(stderr, diagCacheFailed)
			return 1
		}
		rep.Pruned = &pruned
	}
	if rep.Repos, err = r.List(); err != nil {
		_, _ = fmt.Fprintln(stderr, diagCacheFailed)
		return 1
	}
	for _, e := range rep.Repos {
		rep.TotalBytes += e.SizeBytes
	}
	if err := writeJSON(stdout, rep); err != nil {
		_, _ = fmt.Fprintln(stderr, "could not write the report to stdout")
		return 1
	}
	return 0
}
