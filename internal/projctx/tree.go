package projctx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/mdutil"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

// The file tree (X-32): the direct entries, at the base revision, of every
// directory that holds a changed file, the most-changed directories first,
// within review.max_tree_tokens.

// TreeHeader is the line above the fenced file-tree block (X-32, verbatim).
const TreeHeader = "Files in the directories this change touches, at the target branch (data)"

// TreeCutLine follows the closing fence of a listing that was cut.
const TreeCutLine = "(The listing is cut here to fit the prompt.)"

// MaxTreeDirs is the most directories the tree lists.
const MaxTreeDirs = 20

// Fixed texts inside the listing. They follow a directory line ("dir/") or
// stand for the entries a host did not list; they carry no host text.
const (
	markNewDir     = "(new directory)"
	markUnreadable = "(could not be listed)"
	lineMore       = "  ... (more entries not listed)"
	// rootLabel is the directory line of the repository root.
	rootLabel = "./"
)

// Fixed notes of the file tree.
const (
	// NoteTreeUnreadable: no touched directory could be listed.
	NoteTreeUnreadable = "The directories this change touches could not be listed at the target branch."
	// NoteTreePartlyUnreadable: some could not; the listing marks them.
	NoteTreePartlyUnreadable = "Some directories this change touches could not be listed at the target branch."
	// NoteTreeDroppedForDiff: left out entirely to make room for the diff.
	NoteTreeDroppedForDiff = "The listing of the directories this change touches was left out of the prompt to make room for the diff."
)

// noteTreeMoreDirs: more directories hold changed files than the tree
// lists.
func noteTreeMoreDirs(n int) string {
	return fmt.Sprintf("The file tree lists the %d directories with the most changed files; %s not listed.",
		MaxTreeDirs, llmrun.CountPhrase(n, "more directory was", "more directories were"))
}

// dirListing is one touched directory as read.
type dirListing struct {
	path string
	// isNew: the directory does not exist at the base revision.
	isNew bool
	// unreadable: it could not be listed.
	unreadable bool
	entries    []provider.DirEntry
	truncated  bool
}

// tree is the file-tree part of a Context.
type tree struct {
	// status is off or unreadable, or "" when listings were read (the
	// block decides used, cut or left_out).
	status string
	dirs   []dirListing
	// more counts the touched directories beyond MaxTreeDirs.
	more int
	// someUnreadable: at least one directory could not be listed.
	someUnreadable bool
	block          string
	// shown is the number of directories the block lists.
	shown          int
	cut            bool
	droppedForDiff bool
}

// TouchedDirs returns the directories that hold a changed file ("" is the
// root), the one with the most changed files first (then by path), at most
// MaxTreeDirs of them, and how many more there are. A file's directory is
// that of its path in the diff (the new path; the old one for a deletion).
// Files the ignore rules filtered out do not count.
func TouchedDirs(files []provider.FilePatch, skipped []provider.SkippedFile) (dirs []string, more int) {
	counts := map[string]int{}
	add := func(p string) {
		if p == "" {
			return
		}
		dir := ""
		if i := strings.LastIndexByte(p, '/'); i >= 0 {
			dir = p[:i]
		}
		counts[dir]++
	}
	for _, f := range files {
		add(f.Path)
	}
	for _, f := range skipped {
		if f.Reason != provider.SkipFiltered {
			add(f.Path)
		}
	}
	dirs = sortedByCount(counts)
	if len(dirs) > MaxTreeDirs {
		more, dirs = len(dirs)-MaxTreeDirs, dirs[:MaxTreeDirs]
	}
	return dirs, more
}

// readTree lists the touched directories. A directory that does not exist
// at the base is new. After the first failure of another kind the remaining
// directories are not asked for (the cause, such as the token's scope or a
// rate limit, would fail them too) and are marked unreadable.
func readTree(ctx context.Context, src Source, rev string, s Settings, files []provider.FilePatch,
	skipped []provider.SkippedFile, log *slog.Logger) tree {
	t := tree{status: llmrun.ProjectOff}
	if !s.treeOn() {
		return t
	}
	t.status = ""
	paths, more := TouchedDirs(files, skipped)
	t.more = more
	failed := rev == ""
	read := 0
	for _, p := range paths {
		d := dirListing{path: p}
		if failed {
			d.unreadable = true
			t.dirs = append(t.dirs, d)
			continue
		}
		l, err := src.ListDir(ctx, p)
		switch {
		case errors.Is(err, provider.ErrNotFound):
			d.isNew = true
			read++
		case err != nil || l == nil:
			log.Debug("project context: directory not listed", "error", errorClass(err))
			d.unreadable, failed = true, true
		default:
			d.entries, d.truncated = l.Entries, l.Truncated
			read++
		}
		t.someUnreadable = t.someUnreadable || d.unreadable
		t.dirs = append(t.dirs, d)
	}
	if read == 0 && len(t.dirs) > 0 {
		t.status = llmrun.ProjectUnreadable
	}
	return t
}

// lines renders the listing, one line per directory and per entry. Each
// directory line starts a group: groupStart[i] is true for a directory
// line.
func (t *tree) lines() (lines []string, groupStart []bool) {
	for _, d := range t.dirs {
		label := rootLabel
		if d.path != "" {
			label = cleanName(d.path) + "/"
		}
		switch {
		case d.isNew:
			label += " " + markNewDir
		case d.unreadable:
			label += " " + markUnreadable
		}
		lines, groupStart = append(lines, label), append(groupStart, true)
		for _, e := range d.entries {
			name := cleanName(e.Name)
			if e.Dir {
				name += "/"
			}
			lines, groupStart = append(lines, "  "+name), append(groupStart, false)
		}
		if d.truncated {
			lines, groupStart = append(lines, lineMore), append(groupStart, false)
		}
	}
	return lines, groupStart
}

// render renders the block within maxTokens (tokens.Estimate of the whole
// block): the largest prefix of whole lines that fits, without a trailing
// directory line whose entries were all cut.
func (t *tree) render(maxTokens int, factor float64) {
	t.block, t.shown, t.cut = "", 0, false
	if t.status != "" || len(t.dirs) == 0 || maxTokens <= 0 {
		return
	}
	lines, start := t.lines()
	n := largestFitting(len(lines), func(k int) bool {
		return tokens.Estimate(treeBlockOf(lines, k), factor) <= maxTokens
	})
	// A directory line cut off from all of its entries says nothing true
	// about the directory: it goes too, unless it has no entries anyway.
	if n > 0 && n < len(lines) && start[n-1] && !start[n] {
		n--
	}
	if n == 0 {
		return
	}
	t.block = treeBlockOf(lines, n)
	t.cut = n < len(lines)
	for _, s := range start[:n] {
		if s {
			t.shown++
		}
	}
}

// treeBlockOf is the block of the first k lines.
func treeBlockOf(lines []string, k int) string {
	var b strings.Builder
	b.WriteString(TreeHeader + "\n")
	mdutil.WriteFenced(&b, strings.Join(lines[:k], "\n"), "", "")
	if k < len(lines) {
		b.WriteString(TreeCutLine + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// coverageStatus is coverage.file_tree.status.
func (t *tree) coverageStatus() string {
	switch {
	case t.status != "":
		return t.status
	case t.block == "":
		return llmrun.ProjectLeftOut
	case t.cut:
		return llmrun.ProjectCut
	}
	return llmrun.ProjectUsed
}

// notes are the fixed notes of the file tree. A listing cut to
// review.max_tree_tokens has no note: coverage.file_tree says cut, and the
// block itself says so to the model.
func (t *tree) notes() []string {
	switch t.status {
	case llmrun.ProjectOff:
		return nil
	case llmrun.ProjectUnreadable:
		return []string{NoteTreeUnreadable}
	}
	var out []string
	if t.droppedForDiff {
		out = append(out, NoteTreeDroppedForDiff)
	}
	if t.someUnreadable {
		out = append(out, NoteTreePartlyUnreadable)
	}
	if t.more > 0 {
		out = append(out, noteTreeMoreDirs(t.more))
	}
	return out
}
