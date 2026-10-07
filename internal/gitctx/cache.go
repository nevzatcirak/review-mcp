package gitctx

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Cache layout (RC-6):
//
//	<cache_dir>/CACHEDIR.TAG                  review-mcp's cache tag
//	<cache_dir>/<host>/<namespace>/<repo>/    one entry per repository
//	    last-used                             touched on every use
//	    .lock                                 O_CREATE|O_EXCL lock file
//	    git/                                  the bare repository
//
// Only directories at exactly that depth that hold a regular last-used file
// are entries. Removing an entry removes its git directory, its marker and
// its lock, then the entry and parent directories only if they are empty:
// nothing else is ever deleted, so a cache_dir pointed at the wrong place
// cannot lose foreign files. A non-empty cache_dir without review-mcp's tag
// is refused.
const (
	tagName       = "CACHEDIR.TAG"
	markerName    = "last-used"
	lockName      = ".lock"
	gitDirName    = "git"
	cacheSubdir   = "review-mcp"
	reposSubdir   = "repos"
	tagSignature  = "Signature: 8a477f597d28d172789f06886806bc55"
	tagContent    = tagSignature + "\n# This file is a cache directory tag created by review-mcp (repository context).\n# For information about cache directory tags see https://bford.info/cachedir/\n"
	tagOwnerProbe = "review-mcp"

	// lockStaleAfter is the age after which a lock counts as abandoned. A
	// held lock is refreshed every lockHeartbeat, so only a dead holder's
	// lock gets this old. The LRU sweep also spares entries used within this
	// window, so a Checkout stays usable for a while after Ensure.
	lockStaleAfter = 10 * time.Minute
	lockHeartbeat  = time.Minute
	lockPoll       = 100 * time.Millisecond
)

// DefaultCacheDir is os.UserCacheDir()/review-mcp/repos.
func DefaultCacheDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, cacheSubdir, reposSubdir), nil
}

// CacheDir returns the cache directory the Runner uses.
func (r *Runner) CacheDir() (string, error) {
	if r.opts.CacheDir != "" {
		return filepath.Clean(r.opts.CacheDir), nil
	}
	return DefaultCacheDir()
}

// openRoot returns the cache directory, creating it with mode 0700 and
// writing the tag when it is new or empty. A directory that holds anything
// but has no review-mcp tag is refused.
func (r *Runner) openRoot(create bool) (string, error) {
	root, err := r.CacheDir()
	if err != nil {
		return "", fail(ReasonCache)
	}
	fi, err := os.Stat(root)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if !create {
			return root, fs.ErrNotExist
		}
		if err := os.MkdirAll(root, 0o700); err != nil {
			return "", fail(ReasonCache)
		}
	case err != nil:
		return "", fail(ReasonCache)
	case !fi.IsDir():
		return "", fail(ReasonCache)
	}
	if hasTag(root) {
		return root, nil
	}
	ents, err := os.ReadDir(root)
	if err != nil || len(ents) > 0 || !create {
		return "", fail(ReasonCache)
	}
	if err := os.Chmod(root, 0o700); err != nil { //nolint:gosec // G302: a directory; 0700 is owner-only
		return "", fail(ReasonCache)
	}
	if err := os.WriteFile(filepath.Join(root, tagName), []byte(tagContent), 0o600); err != nil {
		return "", fail(ReasonCache)
	}
	return root, nil
}

func hasTag(root string) bool {
	p := filepath.Join(root, tagName)
	fi, err := os.Lstat(p) //nolint:gosec // G703: a fixed file name under the configured cache directory
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > 4096 {
		return false
	}
	//nolint:gosec // G304: a fixed file name under the cache directory.
	b, err := os.ReadFile(p)
	return err == nil && strings.HasPrefix(string(b), tagSignature) && strings.Contains(string(b), tagOwnerProbe)
}

// within reports whether p is root or below it, lexically.
func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != "." && filepath.IsLocal(rel)
}

// realDirs reports whether every component of rel below root is a real
// directory (not a symlink, not a file).
func realDirs(root, rel string) bool {
	cur := root
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		cur = filepath.Join(cur, seg)
		fi, err := os.Lstat(cur)
		if err != nil || !fi.IsDir() || fi.Mode()&fs.ModeSymlink != 0 {
			return false
		}
	}
	return true
}

// ---- locks ----

// repoLock is a held entry lock. Its mtime is refreshed every lockHeartbeat
// until release.
type repoLock struct {
	path string
	stop chan struct{}
	wg   sync.WaitGroup
}

// tryLock takes the lock at path without waiting. ok is false when another
// holder's lock is fresh. A lock older than lockStaleAfter is removed and
// taken over.
func tryLock(path string) (*repoLock, bool, error) {
	for attempt := 0; attempt < 2; attempt++ {
		//nolint:gosec // G304: the lock file of a validated cache entry.
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = f.WriteString(strconv.Itoa(os.Getpid()) + "\n")
			_ = f.Close()
			l := &repoLock{path: path, stop: make(chan struct{})}
			l.wg.Add(1)
			go l.heartbeat()
			return l, true, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, false, err
		}
		fi, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue // released in between
		}
		if err != nil || !fi.Mode().IsRegular() {
			return nil, false, errors.New("lock file is not a regular file")
		}
		if time.Since(fi.ModTime()) <= lockStaleAfter {
			return nil, false, nil
		}
		// Stale: the holder died. Remove it and try once more; a racing
		// process that wins the O_EXCL create keeps the lock.
		_ = os.Remove(path)
	}
	return nil, false, nil
}

// acquireLock waits for the lock at path until ctx is done.
func acquireLock(ctx context.Context, path string) (*repoLock, error) {
	t := time.NewTicker(lockPoll)
	defer t.Stop()
	for {
		l, ok, err := tryLock(path)
		if errors.Is(err, fs.ErrNotExist) {
			// Another process's sweep removed the empty entry directory in
			// between: recreate it and try again.
			if os.MkdirAll(filepath.Dir(path), 0o700) == nil {
				l, ok, err = tryLock(path)
			}
		}
		if err != nil {
			return nil, fail(ReasonCache)
		}
		if ok {
			return l, nil
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.Canceled) {
				return nil, ctx.Err()
			}
			return nil, fail(ReasonBusy)
		case <-t.C:
		}
	}
}

func (l *repoLock) heartbeat() {
	defer l.wg.Done()
	t := time.NewTicker(lockHeartbeat)
	defer t.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-t.C:
			now := time.Now()
			_ = os.Chtimes(l.path, now, now)
		}
	}
}

func (l *repoLock) release() {
	close(l.stop)
	l.wg.Wait()
	_ = os.Remove(l.path)
}

// touch sets the mtime of the marker at path to now, creating it.
func touch(path string) error {
	//nolint:gosec // G304: the marker of a validated cache entry.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	now := time.Now()
	return os.Chtimes(path, now, now)
}

// ---- entries ----

// Entry is one cached repository.
type Entry struct {
	// Repo is "<host>/<namespace>/<repo>" as laid out in the cache.
	Repo string `json:"repo"`
	// Path is the entry directory.
	Path string `json:"path"`
	// SizeBytes is the size of the bare repository's regular files.
	SizeBytes int64 `json:"size_bytes"`
	// LastUsed is the mtime of the last-used marker.
	LastUsed time.Time `json:"last_used"`
	// IdleDays is the number of whole days since LastUsed.
	IdleDays int `json:"idle_days"`
	// InUse is true when another use holds a fresh lock.
	InUse bool `json:"in_use"`
}

// scan lists the entries under root. Symlinks are never followed: a
// symlinked directory at any level is skipped.
func scan(root string) []Entry {
	var out []Entry
	dirs := func(p string) []string {
		ents, err := os.ReadDir(p)
		if err != nil {
			return nil
		}
		var names []string
		for _, e := range ents {
			if e.IsDir() && e.Type()&fs.ModeSymlink == 0 {
				names = append(names, e.Name())
			}
		}
		return names
	}
	now := time.Now()
	for _, host := range dirs(root) {
		for _, ns := range dirs(filepath.Join(root, host)) {
			for _, name := range dirs(filepath.Join(root, host, ns)) {
				p := filepath.Join(root, host, ns, name)
				fi, err := os.Lstat(filepath.Join(p, markerName))
				if err != nil || !fi.Mode().IsRegular() {
					continue
				}
				e := Entry{
					Repo:      host + "/" + ns + "/" + name,
					Path:      p,
					SizeBytes: treeSize(filepath.Join(p, gitDirName)),
					LastUsed:  fi.ModTime(),
				}
				if idle := now.Sub(e.LastUsed); idle > 0 {
					e.IdleDays = int(idle / (24 * time.Hour))
				}
				if lf, err := os.Lstat(filepath.Join(p, lockName)); err == nil && now.Sub(lf.ModTime()) <= lockStaleAfter {
					e.InUse = true
				}
				out = append(out, e)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Repo < out[j].Repo })
	return out
}

// treeSize sums the sizes of the regular files below p without following
// symlinks.
func treeSize(p string) int64 {
	var n int64
	_ = filepath.WalkDir(p, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

// removeEntry deletes the entry at p (under root) when its lock can be
// taken. It reports whether the entry was removed.
func removeEntry(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil || !within(root, p) || !realDirs(root, rel) {
		return false
	}
	l, ok, err := tryLock(filepath.Join(p, lockName))
	if err != nil || !ok {
		return false
	}
	removed := removeEntryLocked(root, p)
	l.release()
	if removed {
		// Only empty directories go: anything else in them stays.
		_ = os.Remove(p)
		_ = os.Remove(filepath.Dir(p))
		_ = os.Remove(filepath.Dir(filepath.Dir(p)))
	}
	return removed
}

// removeEntryLocked deletes the git directory and the marker of an entry
// whose lock the caller holds. The git directory must be a real directory
// under root; os.RemoveAll removes symlinks inside it as links and never
// follows them.
func removeEntryLocked(root, p string) bool {
	g := filepath.Join(p, gitDirName)
	if !within(root, g) {
		return false
	}
	switch fi, err := os.Lstat(g); {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return false
	case fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir():
		if os.Remove(g) != nil { // the link itself, never its target
			return false
		}
	default:
		if os.RemoveAll(g) != nil {
			return false
		}
	}
	err := os.Remove(filepath.Join(p, markerName))
	return err == nil || errors.Is(err, fs.ErrNotExist)
}

// sweep runs the two sweeps of RC-6 under root: entries idle longer than
// idle_days, then least-recently-used entries until the total size is at
// most max_cache_mb. keep (the entry in use by the caller, "" for none),
// entries whose lock is held and entries used within lockStaleAfter are
// never removed. It returns the removed entries.
func (r *Runner) sweep(root, keep string) []Entry {
	var removed []Entry
	idleLimit := time.Duration(r.opts.IdleDays) * 24 * time.Hour
	now := time.Now()
	var live []Entry
	for _, e := range scan(root) {
		if e.Path != keep && now.Sub(e.LastUsed) > idleLimit && removeEntry(root, e.Path) {
			removed = append(removed, e)
			continue
		}
		live = append(live, e)
	}

	var total int64
	for _, e := range live {
		total += e.SizeBytes
	}
	if total <= r.opts.MaxCacheBytes {
		return removed
	}
	sort.SliceStable(live, func(i, j int) bool { return live[i].LastUsed.Before(live[j].LastUsed) })
	for _, e := range live {
		if total <= r.opts.MaxCacheBytes {
			break
		}
		if e.Path == keep || now.Sub(e.LastUsed) <= lockStaleAfter {
			continue
		}
		if removeEntry(root, e.Path) {
			removed = append(removed, e)
			total -= e.SizeBytes
		}
	}
	return removed
}

// List returns the cached repositories. A cache directory that does not
// exist yet is empty; one that is not a review-mcp cache is an error.
func (r *Runner) List() ([]Entry, error) {
	root, err := r.openRoot(false)
	if errors.Is(err, fs.ErrNotExist) {
		return []Entry{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := scan(root)
	if out == nil {
		out = []Entry{}
	}
	return out, nil
}

// Prune runs the sweeps of RC-6 now and returns the removed entries.
func (r *Runner) Prune() ([]Entry, error) {
	root, err := r.openRoot(false)
	if errors.Is(err, fs.ErrNotExist) {
		return []Entry{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := r.sweep(root, "")
	if out == nil {
		out = []Entry{}
	}
	return out, nil
}
