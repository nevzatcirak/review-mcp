// Command evalrepo is the evaluation harness of repository context (RC-10,
// v1.1 spec WP-11d). For each pull request URL it runs the review twice, with
// repository context off and on, by calling the built binary
//
//	review-mcp diag review --json --repo-context=off|on <PR_URL>
//
// (the same code path as a real review; nothing is published), stores both
// results side by side, and writes a rating sheet with one row per finding
// for the owner to fill in: is the finding cross-file, is it correct, is it
// new with context. The sheet is blind by default (see below). Stage 2 (a real code graph) starts only if the ratings
// show a clear gain on at least 20 pull requests (RC-10).
//
// Usage:
//
//	go run ./tools/evalrepo -out DIR [-bin review-mcp] [-cache-dir DIR] [-urls FILE] [-blind=false] [-seed N] [PR_URL ...]
//
// Everything is written under -out, which is required, and nowhere else:
// never into the repository-context cache. evalrepo refuses to run when -out
// lies inside the cache directory (the one `review-mcp diag cache` reports,
// or -cache-dir) or is a non-empty directory. The model's text goes only to
// the files under -out; evalrepo prints counts and fixed sentences. It needs
// the same environment as the binary (provider and LLM settings; tokens are
// read by the binary, never by evalrepo, and never logged). It uses the
// standard library only and is not shipped.
//
// Layout of -out:
//
//	ratings.csv         one row per finding of both runs, shuffled, blind
//	key.csv             sample id -> pull request, mode, finding (not for the rater)
//	README.txt          the seed and how to read the files
//	pr-01/off.json      the pr_review structured result without context
//	pr-01/on.json       the same with context
//	pr-01/compare.md    both reviews' findings and coverage, one after another
//	pr-02/...
//
// The rating is blind by default so that knowing which review had context
// cannot colour it: the rows of ratings.csv are shuffled with a seed (-seed;
// the default is time-based; the seed is printed and written to README.txt, so
// a run is reproducible), carry an opaque sample id instead of the mode and
// no coverage status, and the mapping is in key.csv, which the sheet does not
// reference. Opening a file under pr-NN reveals the mode. With -blind=false the
// sheet has the mode, the finding number and the coverage status in plain
// columns and there is no key.csv or README.txt.
//
// CSV was chosen for the sheet because every spreadsheet opens it and the
// owner only types y or n into three columns.
package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// modes are the two runs of each pull request, in the order they are stored.
var modes = []string{"off", "on"}

// runner runs the review-mcp binary and returns its standard output.
type runner interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
}

// execRunner runs bin. The binary's stderr (fixed sentences and counts, never
// review text) goes to stderr of evalrepo.
type execRunner struct {
	bin    string
	stderr io.Writer
}

func (r execRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, r.bin, args...) //nolint:gosec // G204: the operator's own binary, arguments built here
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, r.stderr
	err := cmd.Run()
	return out.Bytes(), err
}

type issue struct {
	File    string `json:"relevant_file"`
	Header  string `json:"issue_header"`
	Content string `json:"issue_content"`
	Start   int    `json:"start_line"`
	End     int    `json:"end_line"`
}

// result is the part of the pr_review structured result evalrepo reads.
type result struct {
	Review *struct {
		Issues []issue `json:"key_issues_to_review"`
	} `json:"review"`
	Coverage struct {
		RepoContext struct {
			Status     string `json:"status"`
			Reason     string `json:"reason"`
			Symbols    int    `json:"symbols"`
			References int    `json:"references"`
			Files      int    `json:"files"`
		} `json:"repo_context"`
	} `json:"coverage"`
}

func (r *result) issues() []issue {
	if r == nil || r.Review == nil {
		return nil
	}
	return r.Review.Issues
}

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, nil))
}

// run is main with injectable output and runner (nil selects the binary named
// by -bin).
func run(ctx context.Context, args []string, stdout, stderr io.Writer, r runner) int {
	fs := flag.NewFlagSet("evalrepo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", "", "`directory` for every output file (required; must not be inside the cache directory)")
	bin := fs.String("bin", "review-mcp", "the review-mcp `binary`")
	cacheFlag := fs.String("cache-dir", "", "the repository-context cache `directory` (default: the one `review-mcp diag cache` reports)")
	blind := fs.Bool("blind", true, "shuffle the rating sheet and hide which review had context (the mapping goes to key.csv)")
	seedFlag := fs.Int64("seed", 0, "`seed` of the shuffle (default: time-based; printed and written to README.txt)")
	urls := fs.String("urls", "", "`file` with one PR URL per line (blank lines and lines starting with # are ignored)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *out == "" {
		_, _ = fmt.Fprintln(stderr, "evalrepo: -out is required: evalrepo writes only under the directory you give it")
		return 2
	}
	list, err := collectURLs(*urls, fs.Args())
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "evalrepo: cannot read the URL list")
		return 2
	}
	if len(list) == 0 {
		_, _ = fmt.Fprintln(stderr, "evalrepo: no pull request URL given")
		return 2
	}
	if r == nil {
		r = execRunner{bin: *bin, stderr: stderr}
	}

	cache := *cacheFlag
	if cache == "" {
		if cache, err = cacheDirOf(ctx, r); err != nil {
			_, _ = fmt.Fprintln(stderr, "evalrepo: cannot tell where the cache directory is (`review-mcp diag cache` failed); pass -cache-dir")
			return 1
		}
	}
	if inside(*out, cache) {
		_, _ = fmt.Fprintln(stderr, "evalrepo: -out is inside the repository-context cache directory; choose another directory")
		return 2
	}
	if err := prepareOut(*out); err != nil {
		_, _ = fmt.Fprintln(stderr, "evalrepo: "+err.Error())
		return 2
	}

	var items []item
	failed := 0
	for i, url := range list {
		n := i + 1
		dir := filepath.Join(*out, fmt.Sprintf("pr-%02d", n))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			_, _ = fmt.Fprintln(stderr, "evalrepo: cannot create the output directory of pull request "+strconv.Itoa(n))
			return 1
		}
		results := map[string]*result{}
		for _, mode := range modes {
			raw, err := r.Run(ctx, "diag", "review", "--json", "--repo-context="+mode, url)
			var res result
			if err == nil {
				err = json.Unmarshal(raw, &res)
			}
			if err != nil {
				failed++
				_, _ = fmt.Fprintf(stderr, "evalrepo: pull request %d: the review with context %s failed\n", n, mode)
				continue
			}
			results[mode] = &res
			if err := os.WriteFile(filepath.Join(dir, mode+".json"), raw, 0o600); err != nil {
				_, _ = fmt.Fprintln(stderr, "evalrepo: cannot write the result of pull request "+strconv.Itoa(n))
				return 1
			}
			for j, is := range res.issues() {
				items = append(items, item{pr: n, url: url, mode: mode, finding: j + 1, is: is, rc: res.Coverage.RepoContext.Status})
			}
		}
		if err := os.WriteFile(filepath.Join(dir, "compare.md"), []byte(compare(url, results)), 0o600); err != nil {
			_, _ = fmt.Fprintln(stderr, "evalrepo: cannot write the comparison of pull request "+strconv.Itoa(n))
			return 1
		}
	}
	seed := *seedFlag
	if seed == 0 {
		seed = time.Now().UnixNano()
	}
	if err := writeSheets(*out, items, *blind, seed); err != nil {
		_, _ = fmt.Fprintln(stderr, "evalrepo: cannot write the rating sheet")
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "evalrepo: %d pull requests, %d findings to rate, %d reviews failed; see %s\n",
		len(list), len(items), failed, filepath.Join(*out, "ratings.csv"))
	if *blind {
		_, _ = fmt.Fprintf(stdout, "evalrepo: blind sheet, seed %d (also in README.txt)\n", seed)
	}
	if failed > 0 {
		return 1
	}
	return 0
}

// collectURLs reads the list file (if any) and the arguments.
func collectURLs(file string, args []string) ([]string, error) {
	var out []string
	if file != "" {
		b, err := os.ReadFile(file) //nolint:gosec // G304: the operator's own list
		if err != nil {
			return nil, err
		}
		for _, l := range strings.Split(string(b), "\n") {
			l = strings.TrimSpace(l)
			if l != "" && !strings.HasPrefix(l, "#") {
				out = append(out, l)
			}
		}
	}
	for _, a := range args {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return out, nil
}

// cacheDirOf asks the binary where its cache is.
func cacheDirOf(ctx context.Context, r runner) (string, error) {
	raw, err := r.Run(ctx, "diag", "cache")
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

// prepareOut creates the output directory (private) and refuses a directory
// that already has files: results of two runs must not mix.
func prepareOut(dir string) error {
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		return errors.New("-out is not empty; give a new or empty directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return errors.New("cannot create -out")
	}
	return nil
}

// item is one finding of one run.
type item struct {
	pr      int
	url     string
	mode    string
	finding int
	is      issue
	rc      string
}

// ratingColumns are the empty columns the owner fills in.
var ratingColumns = []string{"cross_file", "correct", "new"}

// writeSheets writes ratings.csv (and, when blind, key.csv and README.txt).
//
// Blind: the items are shuffled with seed, numbered S001, S002, ... in that
// order, and the sheet holds the sample id, the pull request, the finding's
// file, lines and header and the empty rating columns, never the mode, the
// finding number or the coverage status. key.csv maps each sample id to them.
func writeSheets(out string, items []item, blind bool, seed int64) error {
	itoa := strconv.Itoa
	if !blind {
		rows := [][]string{append([]string{"pr", "url", "mode", "finding", "file", "start_line", "end_line", "header", "repo_context"}, ratingColumns...)}
		for _, it := range items {
			rows = append(rows, append([]string{itoa(it.pr), it.url, it.mode, itoa(it.finding), it.is.File, itoa(it.is.Start),
				itoa(it.is.End), it.is.Header, it.rc}, "", "", ""))
		}
		return writeSheet(filepath.Join(out, "ratings.csv"), rows)
	}
	shuffled := shuffle(items, seed)
	sheet := [][]string{append([]string{"sample", "pr", "url", "file", "start_line", "end_line", "header"}, ratingColumns...)}
	key := [][]string{{"sample", "pr", "mode", "finding", "repo_context"}}
	for i, it := range shuffled {
		id := fmt.Sprintf("S%03d", i+1)
		sheet = append(sheet, append([]string{id, itoa(it.pr), it.url, it.is.File, itoa(it.is.Start), itoa(it.is.End), it.is.Header}, "", "", ""))
		key = append(key, []string{id, itoa(it.pr), it.mode, itoa(it.finding), it.rc})
	}
	if err := writeSheet(filepath.Join(out, "ratings.csv"), sheet); err != nil {
		return err
	}
	if err := writeSheet(filepath.Join(out, "key.csv"), key); err != nil {
		return err
	}
	readme := fmt.Sprintf(`Blind rating sheet (evalrepo)

ratings.csv  one row per finding of both runs (without and with repository
             context), in random order. Fill cross_file, correct and new
             (y or n) for each row; "sample" identifies the row.
key.csv      which sample is which run. Do not open it until you have rated.
seed         %d (evalrepo -seed %d reproduces this order)

Opening a file under pr-NN/ (off.json, on.json, compare.md) reveals which
review had context. Rate from ratings.csv alone.

new = the finding appears only in the review with context; to fill it in
after rating, join the sheet with key.csv.
`, seed, seed)
	return os.WriteFile(filepath.Join(out, "README.txt"), []byte(readme), 0o600)
}

// shuffle returns a copy of items in the order seed gives; the same seed and
// items always give the same order.
func shuffle(items []item, seed int64) []item {
	out := append([]item(nil), items...)
	rng := rand.New(rand.NewPCG(uint64(seed), uint64(seed)^0x9e3779b97f4a7c15)) //nolint:gosec // G404: a reproducible shuffle, not a secret
	rng.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

func writeSheet(path string, rows [][]string) error {
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	if err := w.WriteAll(rows); err != nil {
		return err
	}
	return os.WriteFile(path, b.Bytes(), 0o600)
}

// compare renders both reviews' findings and coverage one after another.
func compare(url string, results map[string]*result) string {
	var b strings.Builder
	b.WriteString("# " + url + "\n")
	for _, mode := range modes {
		title := map[string]string{"off": "Without repository context", "on": "With repository context"}[mode]
		b.WriteString("\n## " + title + "\n\n")
		res := results[mode]
		if res == nil {
			b.WriteString("The review failed; see the standard error of evalrepo.\n")
			continue
		}
		rc := res.Coverage.RepoContext
		b.WriteString("Repository context: " + rc.Status)
		switch rc.Status {
		case "used":
			fmt.Fprintf(&b, " (%d symbols, %d references from %d files)", rc.Symbols, rc.References, rc.Files)
		case "skipped":
			b.WriteString(" (" + rc.Reason + ")")
		}
		b.WriteString("\n")
		issues := res.issues()
		if len(issues) == 0 {
			b.WriteString("\nNo findings.\n")
		}
		for j, is := range issues {
			fmt.Fprintf(&b, "\n### %d. %s\n\n`%s:%d-%d`\n\n", j+1, oneLine(is.Header), oneLine(is.File), is.Start, is.End)
			for _, l := range strings.Split(strings.TrimSpace(is.Content), "\n") {
				b.WriteString("> " + l + "\n")
			}
		}
	}
	return b.String()
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
