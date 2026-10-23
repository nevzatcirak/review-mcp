package gitctx

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// The read view (X-45, WP-15). Every offline read runs with
//
//	--git-dir=<view> --shallow-file <checkout>/shallow, GIT_OBJECT_DIRECTORY=<checkout>/objects
//
// The view is a bare repository with no extensions.partialClone, no remote,
// no hooks and no refs, so git has no promisor to fetch a missing object
// from: on every supported git a blob the partial clone lacks is reported
// "missing" and a lazy fetch is never started (git 2.31 to 2.44 start one
// from the checkout itself, which -c protocol.allow=never alone stops, and
// 2.39 to 2.44.x then die instead of reporting the blob). The two older
// guards (protocol.allow=never, GIT_NO_LAZY_FETCH=1) stay as further
// layers.
//
// The objects are the checkout's own. --shallow-file names the checkout's
// shallow file, read in place and never written (no read updates it), so a
// walk from the head commit stops at the depth=1 boundary exactly as it
// does in the checkout; without it git would look for the missing parent.
//
// There is one view per object format, <cache_dir>/.view-<format>, written
// by review-mcp itself (no git init: no template, no hooks). Its contents
// are fixed, so they are checked on every use; anything else there (a link,
// a stray file, a changed config) has it rebuilt.
//
// DESIGN-QUESTION (WP-15 §2 DQ-9): --shallow-file is a git option that git
// itself uses internally (receive-pack) and does not document; it exists
// in every supported git (verified on 2.31.0 to 2.47.3). The documented
// alternative is a view per checkout holding a copy of its shallow file,
// which must then be kept in step with every fetch. Recommendation: keep
// --shallow-file; the real-git tests on the CI versions pin it.
const viewPrefix = ".view-"

// staleViewAge is how old a temporary view directory must be before ensureView
// removes it: a younger one may be a process still building its view.
const staleViewAge = time.Hour

// viewHead is the HEAD of a view: an unborn branch. git needs HEAD to accept
// a directory as a repository; nothing is ever resolved through it.
const viewHead = "ref: refs/heads/main\n"

// viewConfig is the whole configuration of the view of each object format.
var viewConfig = map[string]string{
	"sha1":   "[core]\n\trepositoryformatversion = 0\n\tbare = true\n",
	"sha256": "[core]\n\trepositoryformatversion = 1\n\tbare = true\n[extensions]\n\tobjectformat = sha256\n",
}

// objectFormatOf is the object format of a checkout, from the length of its
// head SHA (Ensure checks it against shaRE): 64 hex digits are sha256,
// anything else sha1, the format of every repository git init creates by
// default.
func objectFormatOf(headSHA string) string {
	if len(headSHA) == 64 {
		return "sha256"
	}
	return "sha1"
}

// ensureView returns the view of format under root, creating or rebuilding
// it when it is missing or not exactly what writeView writes. Every failure
// is cache_unusable. It first removes the temporary directories that a
// process killed while building a view left behind.
func ensureView(root, format string) (string, error) {
	if _, ok := viewConfig[format]; !ok {
		return "", fail(ReasonCache)
	}
	sweepStaleViews(root, time.Now())
	v := filepath.Join(root, viewPrefix+format)
	if viewOK(v, format) {
		return v, nil
	}
	// Build it next to its place and rename it in: a concurrent reader sees
	// either no view or a whole one.
	tmp, err := os.MkdirTemp(root, viewPrefix+format+".tmp")
	if err != nil {
		return "", fail(ReasonCache)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	if err := writeView(tmp, format); err != nil {
		return "", fail(ReasonCache)
	}
	if viewOK(v, format) {
		return v, nil // another process put it there meanwhile
	}
	if err := removeView(root, v); err != nil {
		return "", fail(ReasonCache)
	}
	if err := os.Rename(tmp, v); err != nil && !viewOK(v, format) {
		// Not a racing process that put a good view there first.
		return "", fail(ReasonCache)
	}
	if !viewOK(v, format) {
		return "", fail(ReasonCache)
	}
	return v, nil
}

// sweepStaleViews removes every <root>/.view-*.tmp* directory whose
// modification time is older than staleViewAge, with the rules of removeView
// (inside root only; nothing is followed). A younger directory is left to the
// process that may be building it. Something at such a name that is not a
// directory (a link, a file) is no build in progress and goes at once, as
// itself. It is best effort: a failure leaves the entry for the next call.
func sweepStaleViews(root string, now time.Time) {
	ents, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range ents {
		if ok, _ := filepath.Match(viewPrefix+"*.tmp*", e.Name()); !ok {
			continue
		}
		p := filepath.Join(root, e.Name())
		fi, err := os.Lstat(p)
		if err != nil {
			continue
		}
		if fi.IsDir() && now.Sub(fi.ModTime()) <= staleViewAge {
			continue
		}
		_ = removeView(root, p)
	}
}

// writeView writes the files of a view of format into the empty directory
// dir.
func writeView(dir, format string) error {
	for _, d := range []string{"objects", "refs"} {
		if err := os.Mkdir(filepath.Join(dir, d), 0o700); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "HEAD"), []byte(viewHead), 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "config"), []byte(viewConfig[format]), 0o600)
}

// viewOK reports whether v is a real directory holding exactly the view of
// format: HEAD and config with their fixed contents, and the empty
// directories objects and refs.
func viewOK(v, format string) bool {
	if fi, err := os.Lstat(v); err != nil || !fi.IsDir() || fi.Mode()&fs.ModeSymlink != 0 {
		return false
	}
	ents, err := os.ReadDir(v)
	if err != nil {
		return false
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	if !slices.Equal(names, []string{"HEAD", "config", "objects", "refs"}) {
		return false
	}
	for name, want := range map[string]string{"HEAD": viewHead, "config": viewConfig[format]} {
		p := filepath.Join(v, name)
		fi, err := os.Lstat(p)
		if err != nil || !fi.Mode().IsRegular() || fi.Size() != int64(len(want)) {
			return false
		}
		b, err := os.ReadFile(p) //nolint:gosec // G304: a fixed file of the view under the cache directory
		if err != nil || string(b) != want {
			return false
		}
	}
	for _, d := range []string{"objects", "refs"} {
		p := filepath.Join(v, d)
		fi, err := os.Lstat(p)
		if err != nil || !fi.IsDir() || fi.Mode()&fs.ModeSymlink != 0 {
			return false
		}
		if ents, err := os.ReadDir(p); err != nil || len(ents) != 0 {
			return false
		}
	}
	return true
}

// removeView removes whatever is at v (under root): a link or a file as
// itself, never its target, a directory with its contents (os.RemoveAll
// removes links inside it as links).
func removeView(root, v string) error {
	if !within(root, v) {
		return errors.New("view outside the cache")
	}
	fi, err := os.Lstat(v)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	case fi.IsDir() && fi.Mode()&fs.ModeSymlink == 0:
		return os.RemoveAll(v)
	default:
		return os.Remove(v)
	}
}
