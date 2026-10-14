package gitctx

import (
	"bytes"
	"context"
	"errors"
	"strings"
)

// maxChangedOutput caps what the comparison of two commits, and the listing
// of one commit's files, may print. With -z every path costs its length
// plus one byte, so 16 MiB holds well over a hundred thousand paths. A list
// cut by the cap could leave a file out, so a cut list is an error, never a
// short answer.
var maxChangedOutput = 16 << 20

// ChangedPaths returns the paths whose entry differs between the commits
// from and to (full SHAs) of c's repository (X-36): the output of
//
//	git diff --name-only -z --no-renames --no-ext-diff --no-textconv --end-of-options <from> <to> --
//
// run offline against the bare cache like every other read of this package:
// the allowlisted environment with the cache's empty home, no credential,
// protocol.allow=never with no https or http allowance, GIT_NO_LAZY_FETCH=1,
// core.hooksPath the null device, and git's stderr classified and dropped.
//
// Nothing from the repository is run or loaded as code (R-1):
// --no-ext-diff leaves out any diff.external or GIT_EXTERNAL_DIFF program and
// --no-textconv any textconv filter a .gitattributes could name; --name-only
// compares tree entries and reads no blob. --no-renames reports a rename as
// its old and its new path, so a caller matching the paths of a pull request
// sees both.
//
// DESIGN-QUESTION (WP-7c DQ-1): the spec's argv gains --end-of-options before
// the revisions and "--" after them. Both revisions are already checked to
// be full lower-case SHAs, so neither can be read as an option or a path;
// the two markers keep it that way should the check ever change.
// Recommendation: keep them.
//
// DESIGN-QUESTION (WP-7c DQ-2): the spec names the method ChangedPaths,
// which this package already uses for the paths of a diff
// (ChangedPaths(files)). A method and a function do not clash in Go, so the
// spec's name is kept. Recommendation: keep it.
//
// Paths come back as git stores them (with -z nothing is quoted), in git's
// order, each once. Either commit or one of their trees missing from the
// shallow or partial cache is an *Error (git fails offline and fetches
// nothing), and so is output past the cap; callers then compare another
// way. A canceled ctx returns the context's own error.
func (r *Runner) ChangedPaths(ctx context.Context, c Checkout, from, to string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	from, to = strings.ToLower(from), strings.ToLower(to)
	if !shaRE.MatchString(from) || !shaRE.MatchString(to) {
		return nil, fail(ReasonUnsupported)
	}
	g, err := r.openCheckout(c)
	if err != nil {
		return nil, err
	}
	return g.nameListOf(ctx, changedPathsArgs(from, to))
}

// nameList reads the NUL-terminated paths of --name-only -z. A path is kept
// byte for byte, whatever it holds (spaces, newlines, bytes that are not
// UTF-8); a duplicate is listed once.
func nameList(b []byte) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range bytes.Split(b, []byte{0}) {
		if len(p) == 0 || seen[string(p)] {
			continue // the empty field after the last NUL
		}
		seen[string(p)] = true
		out = append(out, string(p))
	}
	return out
}

// ListPaths returns the path of every file of the tree of commit rev (a full
// SHA) of c's repository (X-36), from
//
//	git ls-tree -r -z --name-only --end-of-options <rev>
//
// with the rules of ChangedPaths: offline, the same environment, timeout
// and output cap; the tree objects only, never a blob. Paths come back as
// git stores them. A commit or tree the cache lacks, a failure and output
// past the cap are an *Error, never a short list; a canceled ctx returns
// the context's own error.
//
// The change detection uses it so that a file is called unchanged only when
// git knows its path: a path git did not list as changed could also be one
// git never matched (DQ-7 on WP-7c).
//
// DESIGN-QUESTION (WP-7c fix DQ-10): unlike git diff, ls-tree takes
// --end-of-options as a synonym of "--", so a "--" after the commit would be
// read as a path and the list would come back empty without an error. The
// argv therefore ends with the commit. Recommendation: keep; the real-git
// test pins a non-empty list.
func (r *Runner) ListPaths(ctx context.Context, c Checkout, rev string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rev = strings.ToLower(rev)
	if !shaRE.MatchString(rev) {
		return nil, fail(ReasonUnsupported)
	}
	g, err := r.openCheckout(c)
	if err != nil {
		return nil, err
	}
	return g.nameListOf(ctx, listPathsArgs(rev))
}

// nameListOf runs one offline git command that prints NUL-terminated paths,
// with the timeout and the output cap of ChangedPaths.
func (g *gitRun) nameListOf(ctx context.Context, args []string) ([]string, error) {
	dctx, cancel := context.WithTimeout(ctx, grepTimeout)
	defer cancel()
	res := g.run(dctx, gitCmd{args: args, offline: true, maxStdout: maxChangedOutput})
	if res.err != nil {
		if err := ctx.Err(); err != nil && !errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		_, ferr := res.failure()
		return nil, ferr
	}
	if res.truncated {
		return nil, fail(ReasonTooLarge)
	}
	return nameList(res.stdout), nil
}

// listPathsArgs is the git command of ListPaths after the global options;
// rev is a validated SHA.
func listPathsArgs(rev string) []string {
	return []string{"ls-tree", "-r", "-z", "--name-only", "--end-of-options", rev}
}

// changedPathsArgs is the git command of ChangedPaths after the global
// options; from and to are validated SHAs.
func changedPathsArgs(from, to string) []string {
	return []string{"diff", "--name-only", "-z", "--no-renames", "--no-ext-diff", "--no-textconv", "--end-of-options", from, to, "--"}
}
