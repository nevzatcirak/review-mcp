// Command evalset is the evaluation-set tool of V-9 (X-49). It puts the
// synthetic corpus under tools/evalset/corpus on a Gitea instance (or into
// local clones), runs the built review-mcp binary on every case in named
// variants, and scores the findings against the ground truth of each case.
//
// Usage:
//
//	go run ./tools/evalset setup -gitea-url URL -owner NAME [-prefix evalset-] -out DIR
//	go run ./tools/evalset setup -local CLONES -out DIR
//
// Secrets come from the environment only, never from flags:
// EVALSET_GITEA_TOKEN (setup). It is never printed, logged or written under -out, and the review-mcp
// binary that evalset starts does not inherit any EVALSET_ variable.
//
// Every output goes under -out, which must be a new or empty directory and
// must not lie inside the repository-context cache (the rule of
// tools/evalrepo). evalset prints counts and fixed sentences only; review
// text goes to files under -out.
//
// The corpus is never built or run (R-1): setup copies its files into git
// commits, and the reviewer reads them. evalset uses the standard library
// only, calls the binary as a process, and is not shipped.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Environment variables that hold secrets. Their values are never printed
// or written, and secretPrefix keeps every EVALSET_ variable out of the
// environment of the review-mcp binary.
const (
	envGiteaToken = "EVALSET_GITEA_TOKEN" //nolint:gosec // G101: the name of a variable, not a credential
	secretPrefix  = "EVALSET_"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, os.Getenv))
}

const usage = `usage:
  evalset setup -gitea-url URL -owner NAME [-prefix evalset-] [-corpus DIR] -out DIR
  evalset setup -local CLONES [-prefix evalset-] [-corpus DIR] -out DIR

The Gitea token is read from EVALSET_GITEA_TOKEN.
`

// run is main with injectable output and environment.
func run(ctx context.Context, args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "setup":
		return cmdSetup(ctx, args[1:], stdout, stderr, getenv)
	case "-h", "-help", "--help", "help":
		_, _ = fmt.Fprint(stdout, usage)
		return 0
	}
	_, _ = fmt.Fprint(stderr, usage)
	return 2
}

// execBin runs the review-mcp binary with the given extra environment and
// returns its standard output and standard error. The child environment is
// the current one without any EVALSET_ variable, plus extra.
func execBin(ctx context.Context, bin string, extra []string, args ...string) (stdout, stderr []byte, err error) {
	cmd := exec.CommandContext(ctx, bin, args...) //nolint:gosec // G204: the operator's own binary, arguments built here
	cmd.Env = append(childEnv(os.Environ()), extra...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
	return out.Bytes(), errb.Bytes(), err
}

// childEnv drops every EVALSET_ variable: the binary needs none of them,
// and the secrets must not reach it.
func childEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if !strings.HasPrefix(strings.ToUpper(kv), secretPrefix) {
			out = append(out, kv)
		}
	}
	return out
}

// cacheDirOf asks the binary where its repository-context cache is
// (`review-mcp diag cache`).
func cacheDirOf(ctx context.Context, bin string) (string, error) {
	raw, _, err := execBin(ctx, bin, nil, "diag", "cache")
	if err != nil {
		return "", err
	}
	var rep struct {
		CacheDir string `json:"cache_dir"`
	}
	if err := json.Unmarshal(raw, &rep); err != nil || rep.CacheDir == "" {
		return "", errors.New("no cache_dir")
	}
	return rep.CacheDir, nil
}

// checkOut applies the rule of tools/evalrepo to an output directory: it
// must not lie inside the repository-context cache, and it must be new or
// empty. It creates the directory (private).
func checkOut(name, out, cache string) error {
	if out == "" {
		return fmt.Errorf("%s is required", name)
	}
	if cache != "" && inside(out, cache) {
		return fmt.Errorf("%s is inside the repository-context cache directory; choose another directory", name)
	}
	if entries, err := os.ReadDir(out); err == nil && len(entries) > 0 {
		return fmt.Errorf("%s is not empty; give a new or empty directory", name)
	}
	if err := os.MkdirAll(out, 0o700); err != nil {
		return fmt.Errorf("cannot create %s", name)
	}
	return nil
}

// resolveCache returns the cache directory: the flag when given, else the
// one the binary reports.
func resolveCache(ctx context.Context, flagValue, bin string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	dir, err := cacheDirOf(ctx, bin)
	if err != nil {
		return "", errors.New("cannot tell where the repository-context cache is (`review-mcp diag cache` failed); pass -cache-dir")
	}
	return dir, nil
}

// inside reports whether path is dir or lies under it, after making both
// absolute and resolving symbolic links as far as the paths exist.
func inside(path, dir string) bool {
	p, d := resolve(path), resolve(dir)
	if p == "" || d == "" {
		return true // cannot tell: refuse
	}
	rel, err := filepath.Rel(d, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// resolve returns the absolute path with symbolic links resolved for the
// longest existing prefix.
func resolve(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return ""
	}
	rest := ""
	for cur := abs; ; {
		if real, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(real, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// writeJSON writes v indented, private.
func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}
