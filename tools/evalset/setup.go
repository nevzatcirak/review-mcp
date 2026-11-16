package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/nevzatcirak/review-mcp/tools/evalset/cases"
)

// Fixed identity and dates of every commit setup makes (§2.1), so that the
// same case always gives the same trees and commit ids.
const (
	commitName  = "evalset"
	commitEmail = "evalset@example.com"
	baseDate    = "2026-01-01T00:00:00+00:00"
	headDate    = "2026-01-01T01:00:00+00:00"
	// baseMessage is the message of every base commit; the head commit's
	// message is the case title. Neither names the case or its category.
	baseMessage = "base"
	baseBranch  = "main"
	headBranch  = "change"
)

// prsFile is DIR/prs.json, the only place that maps a case id to its
// repository (WP-21a DQ-3).
type prsFile struct {
	Version int `json:"version"`
	// Mode is "gitea" (pull requests on a Gitea instance) or "local"
	// (clones made by setup -local).
	Mode     string    `json:"mode"`
	GiteaURL string    `json:"gitea_url,omitempty"`
	Owner    string    `json:"owner,omitempty"`
	Prefix   string    `json:"prefix"`
	Cases    []prEntry `json:"cases"`
}

// prEntry is one case of prs.json.
type prEntry struct {
	ID   string `json:"id"`
	Repo string `json:"repo"`
	// URL is the pull request (mode gitea); Path the clone (mode local).
	URL      string `json:"url,omitempty"`
	Path     string `json:"path,omitempty"`
	Base     string `json:"base"`
	Head     string `json:"head"`
	BaseTree string `json:"base_tree"`
	HeadTree string `json:"head_tree"`
}

var prefixRE = regexp.MustCompile(`^[A-Za-z0-9._-]{0,40}$`)

// repoName is the opaque repository name of a case: the prefix and the
// first 8 hex digits of SHA-256(id). The case id names the category and
// sometimes the topic, and the pull request URL reaches the model, so the
// id never appears in anything setup pushes (WP-21a DQ-3).
func repoName(prefix, id string) string {
	sum := sha256.Sum256([]byte(id))
	return prefix + hex.EncodeToString(sum[:])[:8]
}

func cmdSetup(ctx context.Context, args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("evalset setup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	giteaURL := fs.String("gitea-url", "", "base `URL` of the Gitea instance")
	owner := fs.String("owner", "", "the user or organization that owns the repositories")
	prefix := fs.String("prefix", "evalset-", "repository name `prefix`")
	corpus := fs.String("corpus", filepath.Join("tools", "evalset", "corpus"), "the corpus `directory`")
	out := fs.String("out", "", "`directory` for prs.json (required; new or empty)")
	local := fs.String("local", "", "make local clones under this `directory` instead of using Gitea")
	bin := fs.String("bin", "review-mcp", "the review-mcp `binary`, asked where its cache is")
	cacheFlag := fs.String("cache-dir", "", "the repository-context cache `directory` (default: the one `review-mcp diag cache` reports)")
	only := fs.String("cases", "", "comma-separated case `ids` to set up (default: all)")
	private := fs.Bool("private", true, "create the Gitea repositories as private")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	fail := func(code int, format string, a ...any) int {
		_, _ = fmt.Fprintf(stderr, "evalset setup: "+format+"\n", a...)
		return code
	}
	if fs.NArg() > 0 {
		return fail(2, "unexpected argument")
	}
	if !prefixRE.MatchString(*prefix) {
		return fail(2, "-prefix may hold letters, digits, '.', '_' and '-' only (at most 40)")
	}
	token := ""
	if *local != "" {
		if *giteaURL != "" || *owner != "" {
			return fail(2, "-local cannot be combined with -gitea-url or -owner")
		}
	} else {
		if *giteaURL == "" || *owner == "" {
			return fail(2, "-gitea-url and -owner are required (or -local)")
		}
		if err := checkGiteaURL(*giteaURL); err != nil {
			return fail(2, "%s", err.Error())
		}
		if token = getenv(envGiteaToken); token == "" {
			return fail(2, "%s is not set; the token is read from the environment only", envGiteaToken)
		}
	}

	all, err := cases.Load(os.DirFS(*corpus))
	if err != nil || len(all) == 0 {
		return fail(2, "cannot read the corpus in -corpus")
	}
	if errs := cases.CheckCorpus(all); len(errs) > 0 {
		return fail(2, "the corpus fails its self-check (%d errors); run go test ./tools/evalset/...", len(errs))
	}
	selected, err := selectCases(all, *only)
	if err != nil {
		return fail(2, "%s", err.Error())
	}
	names := map[string]string{}
	for _, c := range all {
		n := repoName(*prefix, c.ID)
		if other, dup := names[n]; dup {
			return fail(2, "cases %s and %s get the same repository name; choose another -prefix", other, c.ID)
		}
		names[n] = c.ID
	}

	cache, err := resolveCache(ctx, *cacheFlag, *bin)
	if err != nil {
		return fail(1, "%s", err.Error())
	}
	if err := checkOut("-out", *out, cache); err != nil {
		return fail(2, "%s", err.Error())
	}
	if *local != "" {
		if err := checkOut("-local", *local, cache); err != nil {
			return fail(2, "%s", err.Error())
		}
		if inside(*local, *out) || inside(*out, *local) {
			return fail(2, "-local and -out must be separate directories")
		}
	}

	file := prsFile{Version: 1, Prefix: *prefix}
	if *local != "" {
		file.Mode = "local"
		root, err := filepath.Abs(*local)
		if err != nil {
			return fail(1, "cannot resolve -local")
		}
		for _, c := range selected {
			dir := filepath.Join(root, repoName(*prefix, c.ID))
			e, err := buildCase(ctx, c, dir)
			if err != nil {
				return fail(1, "case %s: %s", c.ID, err.Error())
			}
			e.Repo, e.Path = repoName(*prefix, c.ID), dir
			file.Cases = append(file.Cases, e)
		}
		if err := writeJSON(filepath.Join(*out, "prs.json"), file); err != nil {
			return fail(1, "cannot write prs.json")
		}
		_, _ = fmt.Fprintf(stdout, "evalset setup: %d cases cloned; prs.json written\n", len(file.Cases))
		return 0
	}

	file.Mode, file.GiteaURL, file.Owner = "gitea", strings.TrimRight(*giteaURL, "/"), *owner
	g := newGitea(file.GiteaURL, token)
	login, err := g.user(ctx)
	if err != nil {
		return fail(1, "the Gitea API refused the token or cannot be reached")
	}
	work, err := os.MkdirTemp("", "evalset-setup-")
	if err != nil {
		return fail(1, "cannot create a work directory")
	}
	defer func() { _ = os.RemoveAll(work) }()

	var created, reused, opened, prReused int
	for i, c := range selected {
		name := repoName(*prefix, c.ID)
		e, err := buildCase(ctx, c, filepath.Join(work, fmt.Sprintf("c%02d", i)))
		if err != nil {
			return fail(1, "case %s: %s", c.ID, err.Error())
		}
		e.Repo = name
		st, err := g.ensureRepo(ctx, *owner, name, login, *private, e.Base)
		if err != nil {
			return fail(1, "case %s: %s", c.ID, err.Error())
		}
		if st == repoCreated {
			created++
		} else {
			reused++
		}
		remote := file.GiteaURL + "/" + url.PathEscape(*owner) + "/" + url.PathEscape(name) + ".git"
		if err := pushCase(ctx, filepath.Join(work, fmt.Sprintf("c%02d", i)), remote, token); err != nil {
			return fail(1, "case %s: %s", c.ID, err.Error())
		}
		if err := g.waitBranch(ctx, *owner, name, headBranch, e.Head); err != nil {
			return fail(1, "case %s: %s", c.ID, err.Error())
		}
		pr, isNew, err := g.ensurePR(ctx, *owner, name, c.Title, c.Description, e.Head)
		if err != nil {
			return fail(1, "case %s: %s", c.ID, err.Error())
		}
		if isNew {
			opened++
		} else {
			prReused++
		}
		e.URL = pr
		file.Cases = append(file.Cases, e)
	}
	if err := writeJSON(filepath.Join(*out, "prs.json"), file); err != nil {
		return fail(1, "cannot write prs.json")
	}
	_, _ = fmt.Fprintf(stdout, "evalset setup: %d cases; repositories %d created, %d reused; pull requests %d opened, %d reused; prs.json written\n",
		len(file.Cases), created, reused, opened, prReused)
	return 0
}

// checkGiteaURL accepts an http or https URL without credentials, query or
// fragment: a token never goes into a flag.
func checkGiteaURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("-gitea-url must be an http or https URL")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("-gitea-url must not carry credentials, a query or a fragment; the token is read from " + envGiteaToken)
	}
	return nil
}

// selectCases returns the cases named in only (all when empty), in corpus
// order.
func selectCases(all []*cases.Case, only string) ([]*cases.Case, error) {
	if strings.TrimSpace(only) == "" {
		return all, nil
	}
	want := map[string]bool{}
	for _, id := range strings.Split(only, ",") {
		if id = strings.TrimSpace(id); id != "" {
			want[id] = true
		}
	}
	var out []*cases.Case
	for _, c := range all {
		if want[c.ID] {
			out = append(out, c)
			delete(want, c.ID)
		}
	}
	for id := range want {
		return nil, fmt.Errorf("-cases names %s, which is not in the corpus", id)
	}
	return out, nil
}

// buildCase makes the repository of a case in dir (which must not exist):
// the base commit on main, the head commit on change, change checked out.
// Only base/ and head/ (and the deletions of deleted.txt) go into the
// commits; case.toml and the corpus README never do.
func buildCase(ctx context.Context, c *cases.Case, dir string) (prEntry, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return prEntry{}, errors.New("cannot create the repository directory")
	}
	g := gitIn{dir: dir}
	if err := g.run(ctx, nil, "init", "-q"); err != nil {
		return prEntry{}, err
	}
	if err := g.run(ctx, nil, "symbolic-ref", "HEAD", "refs/heads/"+baseBranch); err != nil {
		return prEntry{}, err
	}
	for p, b := range c.Base {
		if err := writeFile(dir, p, b); err != nil {
			return prEntry{}, err
		}
	}
	if err := g.commit(ctx, baseMessage, baseDate); err != nil {
		return prEntry{}, err
	}
	if err := g.run(ctx, nil, "checkout", "-q", "-b", headBranch); err != nil {
		return prEntry{}, err
	}
	for p, b := range c.Head {
		if err := writeFile(dir, p, b); err != nil {
			return prEntry{}, err
		}
	}
	for _, p := range c.Deleted {
		if err := os.Remove(filepath.Join(dir, filepath.FromSlash(p))); err != nil {
			return prEntry{}, errors.New("cannot delete a path of deleted.txt")
		}
	}
	if err := g.commit(ctx, c.Title, headDate); err != nil {
		return prEntry{}, err
	}
	var e prEntry
	e.ID = c.ID
	for _, r := range []struct {
		dst *string
		rev string
	}{{&e.Base, baseBranch}, {&e.Head, headBranch}, {&e.BaseTree, baseBranch + "^{tree}"}, {&e.HeadTree, headBranch + "^{tree}"}} {
		out, err := g.output(ctx, "rev-parse", "--verify", "-q", r.rev)
		if err != nil {
			return prEntry{}, err
		}
		*r.dst = out
	}
	return e, nil
}

// writeFile writes a corpus file (slash-separated path p) under dir with a
// fixed mode, so the tree is the same on every OS.
func writeFile(dir, p string, b []byte) error {
	full := filepath.Join(dir, filepath.FromSlash(p))
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		return errors.New("cannot create a directory of the case")
	}
	if err := os.WriteFile(full, b, 0o600); err != nil {
		return errors.New("cannot write a file of the case")
	}
	return nil
}

// pushCase pushes main and change to remote without force. The token goes
// in an HTTP header passed through git's environment (GIT_CONFIG_COUNT),
// never on the command line, in the URL or in a config file.
func pushCase(ctx context.Context, dir, remote, token string) error {
	header := "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(commitName+":"+token))
	env := []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.extraHeader", "GIT_CONFIG_VALUE_0=" + header}
	err := gitIn{dir: dir}.run(ctx, env, "push", "-q", remote,
		"refs/heads/"+baseBranch+":refs/heads/"+baseBranch, "refs/heads/"+headBranch+":refs/heads/"+headBranch)
	if err != nil {
		return errors.New("git push failed: the repository's branches differ from the case, or the server refused the push")
	}
	return nil
}

// gitIn runs git in one directory with a fixed, isolated configuration:
// no global config (and no system config except on Windows), hooks off, no signing, no line-ending
// conversion, a fixed author and committer, and no inherited GIT_ or
// EVALSET_ variable.
type gitIn struct{ dir string }

func (g gitIn) cmd(ctx context.Context, extra []string, args ...string) *exec.Cmd {
	full := append([]string{
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "commit.gpgsign=false",
		"-c", "core.autocrlf=false",
		"-c", "core.safecrlf=false",
		"-c", "core.fileMode=false",
		"-c", "credential.helper=",
	}, args...)
	cmd := exec.CommandContext(ctx, "git", full...) //nolint:gosec // G204: git with arguments built here
	cmd.Dir = g.dir
	env := []string{}
	for _, kv := range childEnv(os.Environ()) {
		if !strings.HasPrefix(strings.ToUpper(kv), "GIT_") {
			env = append(env, kv)
		}
	}
	// Git for Windows keeps its TLS settings in the system configuration,
	// so it is read there (as in internal/gitctx); the -c values above
	// override what matters for the commits (core.autocrlf among them).
	if runtime.GOOS != "windows" {
		env = append(env, "GIT_CONFIG_NOSYSTEM=1")
	}
	env = append(env,
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME="+commitName, "GIT_AUTHOR_EMAIL="+commitEmail,
		"GIT_COMMITTER_NAME="+commitName, "GIT_COMMITTER_EMAIL="+commitEmail,
	)
	cmd.Env = append(env, extra...)
	return cmd
}

// run runs git; its output is dropped (it may name a remote), and the error
// is one fixed sentence naming the git command.
func (g gitIn) run(ctx context.Context, extra []string, args ...string) error {
	if err := g.cmd(ctx, extra, args...).Run(); err != nil {
		return fmt.Errorf("git %s failed", args[0])
	}
	return nil
}

func (g gitIn) output(ctx context.Context, args ...string) (string, error) {
	b, err := g.cmd(ctx, nil, args...).Output()
	if err != nil {
		return "", fmt.Errorf("git %s failed", args[0])
	}
	return strings.TrimSpace(string(b)), nil
}

// commit stages everything and commits with the fixed identity and date.
func (g gitIn) commit(ctx context.Context, message, date string) error {
	if err := g.run(ctx, nil, "add", "-A"); err != nil {
		return err
	}
	return g.run(ctx, []string{"GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date},
		"commit", "-q", "--no-verify", "-m", message)
}
