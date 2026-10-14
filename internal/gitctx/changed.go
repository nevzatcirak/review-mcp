package gitctx

import (
	"bytes"
	"context"
	"errors"
	"strings"
)

// maxChangedOutput caps what the comparison of two commits may print. With
// -z every path costs its length plus one byte, so 16 MiB holds well over a
// hundred thousand changed paths. A list cut by the cap could leave a
// changed file out, so a cut list is an error, never a short answer.
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
	dctx, cancel := context.WithTimeout(ctx, grepTimeout)
	defer cancel()
	res := g.run(dctx, gitCmd{args: changedPathsArgs(from, to), offline: true, maxStdout: maxChangedOutput})
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

// changedPathsArgs is the git command of ChangedPaths after the global
// options; from and to are validated SHAs.
func changedPathsArgs(from, to string) []string {
	return []string{"diff", "--name-only", "-z", "--no-renames", "--no-ext-diff", "--no-textconv", "--end-of-options", from, to, "--"}
}
