package gitctx

import (
	"bytes"
	"context"
	"slices"
	"strconv"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// Reads of one commit the cache already holds (X-32): the project's
// guidelines file and the listings of the directories a pull request
// touches, at its base revision, when an earlier fetch happened to bring
// that commit in. They are the offline plumbing of Grep and nothing more:
// git cat-file and git ls-tree against a commit (a tree-ish) of the bare
// cache, never a working tree, with no credential, every protocol
// forbidden and GIT_NO_LAZY_FETCH=1. Nothing from the repository is run or
// loaded as configuration (R-1): no checkout, no hook (core.hooksPath is
// the null device), no filter or textconv (cat-file prints the stored
// object as it is).
//
// A path that does not exist at the commit is a provider not_found error and
// a blob larger than the cap a provider too_large error, so callers treat
// them exactly like the provider's own GetFileAt and ListDir. Any other
// failure (a blob the partial clone lacks, a git failure, a timeout) is an
// *Error, and callers then read through the provider instead. Each git
// invocation has the search's limit (grepTimeout).

// HasCommit reports whether the repository of c holds the commit rev (a
// full SHA), without any network access. Any failure is false.
func (r *Runner) HasCommit(ctx context.Context, c Checkout, rev string) bool {
	rev = strings.ToLower(rev)
	if !shaRE.MatchString(rev) {
		return false
	}
	g, err := r.openCheckout(c)
	if err != nil {
		return false
	}
	typ, _, ok, err := g.objectInfo(ctx, rev+"^{commit}")
	return err == nil && ok && typ == "commit"
}

// FileAt returns the content of the file path at commit rev of c's
// repository, at most maxBytes bytes (larger is too_large). A path that is
// not a file at rev (missing, a directory, a submodule) is not_found.
func (r *Runner) FileAt(ctx context.Context, c Checkout, rev, path string, maxBytes int) ([]byte, error) {
	rev = strings.ToLower(rev)
	if !shaRE.MatchString(rev) || !usablePath(path) || path == "" {
		return nil, fail(ReasonUnsupported)
	}
	g, err := r.openCheckout(c)
	if err != nil {
		return nil, err
	}
	dir, name := "", path
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		dir, name = path[:i], path[i+1:]
	}
	entries, err := g.listTree(ctx, rev, dir)
	if err != nil {
		return nil, err
	}
	var oid string
	for _, e := range entries {
		if e.name == name && e.typ == "blob" {
			oid = e.oid
			break
		}
	}
	if oid == "" {
		return nil, &provider.Error{Class: provider.ClassNotFound}
	}
	return g.readBlob(ctx, oid, maxBytes)
}

// ListDir returns the direct entries of directory dir ("" is the root) at
// commit rev of c's repository, sorted and capped like the provider's
// (provider.FinishDirListing). A dir that is not a directory at rev is
// not_found.
func (r *Runner) ListDir(ctx context.Context, c Checkout, rev, dir string) (*provider.DirListing, error) {
	rev = strings.ToLower(rev)
	if !shaRE.MatchString(rev) || !usablePath(dir) {
		return nil, fail(ReasonUnsupported)
	}
	g, err := r.openCheckout(c)
	if err != nil {
		return nil, err
	}
	entries, err := g.listTree(ctx, rev, dir)
	if err != nil {
		return nil, err
	}
	return provider.FinishDirListing(dirEntries(entries), false), nil
}

// DirEntries returns every direct entry of directory dir ("" is the root)
// at commit rev of c's repository, sorted by name and not capped: the
// caller caps and counts what it leaves out (the list_dir tool, X-43). A
// submodule is listed as a file. A dir that is not a directory at rev is
// not_found.
func (r *Runner) DirEntries(ctx context.Context, c Checkout, rev, dir string) ([]provider.DirEntry, error) {
	rev = strings.ToLower(rev)
	if !shaRE.MatchString(rev) || !usablePath(dir) {
		return nil, fail(ReasonUnsupported)
	}
	g, err := r.openCheckout(c)
	if err != nil {
		return nil, err
	}
	entries, err := g.listTree(ctx, rev, dir)
	if err != nil {
		return nil, err
	}
	out := dirEntries(entries)
	slices.SortStableFunc(out, func(a, b provider.DirEntry) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// dirEntries maps ls-tree entries to directory entries.
func dirEntries(entries []treeEntry) []provider.DirEntry {
	out := make([]provider.DirEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, provider.DirEntry{Name: e.name, Dir: e.typ == "tree"})
	}
	return out
}

// usablePath reports whether p can name a path in an object specification
// read from cat-file's standard input: valid UTF-8, no control character
// (one specification per line), no leading or trailing "/" and no "//".
func usablePath(p string) bool {
	if len(p) > 4096 || strings.ContainsFunc(p, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return false
	}
	return !strings.HasPrefix(p, "/") && !strings.HasSuffix(p, "/") && !strings.Contains(p, "//")
}

// treeEntry is one line of git ls-tree.
type treeEntry struct {
	typ, oid, name string
}

// objectInfo asks git cat-file --batch-check for the type and size of the
// object spec names. ok is false when git reports it missing (also a blob
// the partial clone lacks: its header cannot be read offline).
func (g *gitRun) objectInfo(ctx context.Context, spec string) (typ string, size int, ok bool, err error) {
	cctx, cancel := context.WithTimeout(ctx, grepTimeout)
	defer cancel()
	res := g.run(cctx, gitCmd{
		args:    []string{"cat-file", "--batch-check"},
		offline: true,
		stdin:   []byte(spec + "\n"),
	})
	if res.err != nil {
		_, ferr := res.failure()
		return "", 0, false, ferr
	}
	m := blobHeadRE.FindStringSubmatch(strings.TrimRight(string(res.stdout), "\n"))
	if m == nil {
		return "", 0, false, nil // "<spec> missing" (or "ambiguous")
	}
	size, _ = strconv.Atoi(m[3])
	return m[2], size, true, nil
}

// listTree lists the direct entries of the tree dir ("" is the root) of
// commit rev. A dir that is not a tree at rev is not_found. The listing
// reads tree objects only, never a blob.
func (g *gitRun) listTree(ctx context.Context, rev, dir string) ([]treeEntry, error) {
	spec := rev + "^{tree}"
	if dir != "" {
		spec = rev + ":" + dir
	}
	typ, _, ok, err := g.objectInfo(ctx, spec)
	if err != nil {
		return nil, err
	}
	if !ok || typ != "tree" {
		return nil, &provider.Error{Class: provider.ClassNotFound}
	}
	lctx, cancel := context.WithTimeout(ctx, grepTimeout)
	defer cancel()
	// The tree-ish is one argument that starts with the SHA, so no part of
	// dir can be read as an option or a pathspec.
	res := g.run(lctx, gitCmd{args: []string{"ls-tree", "-z", spec}, offline: true, maxStdout: maxListOutput})
	if res.err != nil {
		_, ferr := res.failure()
		return nil, ferr
	}
	var out []treeEntry
	for _, ent := range bytes.Split(res.stdout, []byte{0}) {
		// "<mode> SP <type> SP <oid> TAB <name>"
		meta, name, found := bytes.Cut(ent, []byte{'\t'})
		if !found || len(name) == 0 {
			continue
		}
		f := strings.Fields(string(meta))
		if len(f) != 3 {
			continue
		}
		out = append(out, treeEntry{typ: f[1], oid: f[2], name: string(name)})
	}
	return out, nil
}

// readBlob reads the blob oid with git cat-file --batch, at most maxBytes
// bytes of content: a larger blob is too_large (and is not read in full). A
// blob git cannot read offline is an *Error.
func (g *gitRun) readBlob(ctx context.Context, oid string, maxBytes int) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, grepTimeout)
	defer cancel()
	res := g.run(cctx, gitCmd{
		args:    []string{"cat-file", "--batch"},
		offline: true,
		stdin:   []byte(oid + "\n"),
		// The header line, the content and its trailing newline.
		maxStdout: max(maxBytes, 0) + 256,
	})
	if res.err != nil {
		_, ferr := res.failure()
		return nil, ferr
	}
	nl := bytes.IndexByte(res.stdout, '\n')
	if nl < 0 {
		return nil, fail(ReasonGitFailed)
	}
	head := string(res.stdout[:nl])
	if strings.HasSuffix(head, " missing") {
		return nil, fail(ReasonBlobMissing) // the partial clone lacks it
	}
	m := blobHeadRE.FindStringSubmatch(head)
	if m == nil || m[2] != "blob" {
		return nil, fail(ReasonGitFailed)
	}
	size, err := strconv.Atoi(m[3])
	if err != nil {
		return nil, fail(ReasonGitFailed)
	}
	if size > maxBytes {
		return nil, &provider.Error{Class: provider.ClassTooLarge}
	}
	body := res.stdout[nl+1:]
	if len(body) < size {
		return nil, fail(ReasonGitFailed)
	}
	return bytes.Clone(body[:size]), nil
}
