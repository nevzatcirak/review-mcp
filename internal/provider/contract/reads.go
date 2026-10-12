package contract

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// Unchanged files of samplePR, and the directories of the list_dir cases.
const (
	pathReadme   = "README.md"
	pathHelpers  = "src/util/helpers.go"
	dirOverCap   = "gen/many"
	dirAtCap     = "gen/exact"
	pathNotThere = "src/missing.go"
)

// allCheckStates are the six provider check states.
var allCheckStates = []provider.CheckState{
	provider.CheckSuccess, provider.CheckFailure, provider.CheckPending,
	provider.CheckNeutral, provider.CheckSkipped, provider.CheckError,
}

func plainCheck(name, url string, state provider.CheckState) Check {
	return Check{Name: name, URL: url, State: state, WantName: name, WantURL: url}
}

// sampleChecks are samplePR's checks: one per state, URLs that are not
// http(s) or carry user info (dropped), names with control and
// bidirectional characters and over the length limit (cleaned and cut),
// and a state value the host does not document (neutral).
func sampleChecks() []Check {
	long := strings.Repeat("x", 150)
	accents := strings.Repeat("\u00e9", 120)
	return []Check{
		plainCheck("build", "https://ci.example.com/build/1", provider.CheckSuccess),
		plainCheck("unit tests", "https://ci.example.com/test/1", provider.CheckFailure),
		plainCheck("lint", "http://ci.example.com/lint/1", provider.CheckPending),
		plainCheck("docs", "", provider.CheckNeutral),
		plainCheck("deploy preview", "https://ci.example.com/preview/1", provider.CheckSkipped),
		{Name: "integration", URL: "javascript:alert(1)", State: provider.CheckError, WantName: "integration"},
		{Name: "  hostile\x1b[31m\u202e name\n" + long, URL: "ftp://ci.example.com/x", State: provider.CheckSuccess,
			WantName: "hostile[31m name" + long[:maxNameRunes-len("hostile[31m name")]},
		{Name: "accents " + accents, URL: "https://someone@ci.example.com/a", State: provider.CheckFailure,
			WantName: "accents " + string([]rune(accents)[:maxNameRunes-len("accents ")])},
		{Name: "mystery", URL: "https://ci.example.com/m/1", Unknown: true, State: provider.CheckNeutral,
			WantName: "mystery", WantURL: "https://ci.example.com/m/1"},
	}
}

// maxNameRunes is the check name limit of the spec (100 runes), written
// out so that the expectations do not follow the code under test.
const maxNameRunes = 100

// sampleUnchanged are samplePR's files outside the change.
func sampleUnchanged() []RepoFile {
	return []RepoFile{
		{Path: pathReadme, Content: "# Demo\n\nA synthetic repository.\n"},
		{Path: pathHelpers, Content: "package util\n"},
	}
}

// TreeAt returns the files of the repository at revision sha, path to
// content: the Unchanged files and, at BaseSHA, every changed file that
// exists there (at its BasePath) or, at HeadSHA, every changed file that
// exists there. Any other revision is unknown to the host: nil.
func (s *Spec) TreeAt(sha string) map[string]string {
	if sha != s.BaseSHA && sha != s.HeadSHA {
		return nil
	}
	out := map[string]string{}
	for _, f := range s.Unchanged {
		out[f.Path] = f.Content
	}
	for i := range s.Files {
		f := &s.Files[i]
		switch {
		case sha == s.BaseSHA && f.Type != provider.ChangeAdded:
			out[f.BasePath()] = f.Base
		case sha == s.HeadSHA && f.Type != provider.ChangeDeleted:
			out[f.Path] = f.Head
		}
	}
	return out
}

// FileAt returns the content of the file path at sha; ok is false when
// there is no such file.
func (s *Spec) FileAt(sha, path string) (content string, ok bool) {
	content, ok = s.TreeAt(sha)[path]
	return content, ok
}

// DirAt returns the direct entries of directory dir ("" for the root) at
// sha, sorted by name; ok is false when there is no such directory.
func (s *Spec) DirAt(sha, dir string) (entries []provider.DirEntry, ok bool) {
	tree := s.TreeAt(sha)
	if tree == nil {
		return nil, false
	}
	prefix := ""
	if dir != "" {
		prefix = dir + "/"
	}
	seen := map[string]bool{}
	for p := range tree {
		rest, found := strings.CutPrefix(p, prefix)
		if !found {
			continue
		}
		name, _, isDir := strings.Cut(rest, "/")
		if seen[name] {
			continue
		}
		seen[name] = true
		entries = append(entries, provider.DirEntry{Name: name, Dir: isDir})
	}
	if len(entries) == 0 {
		return nil, dir == ""
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, true
}

// forHost keeps the checks of spec whose state the host can report.
func (s *suite) forHost(spec Spec) Spec {
	states := s.tr.CheckStates
	if states == nil {
		states = allCheckStates
	}
	var kept []Check
	for _, c := range spec.Checks {
		if c.Unknown || slices.Contains(states, c.State) {
			kept = append(kept, c)
		}
	}
	spec.Checks = kept
	return spec
}

// manyChecks has n plain checks in the host's states, in turn.
func (s *suite) manyChecks(n int) Spec {
	spec := samplePR()
	spec.Threads, spec.Reviewers = nil, nil
	states := s.tr.CheckStates
	if states == nil {
		states = allCheckStates
	}
	spec.Checks = nil
	for i := range n {
		spec.Checks = append(spec.Checks, plainCheck(fmt.Sprintf("check-%04d", i),
			fmt.Sprintf("https://ci.example.com/run/%d", i), states[i%len(states)]))
	}
	return spec
}

type checkKey struct {
	name, url string
	state     provider.CheckState
}

func checkKeys(cs []provider.Check) []checkKey {
	out := make([]checkKey, 0, len(cs))
	for _, c := range cs {
		out = append(out, checkKey{c.Name, c.URL, c.State})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

func wantKeys(cs []Check) []checkKey {
	out := make([]checkKey, 0, len(cs))
	for _, c := range cs {
		out = append(out, checkKey{c.WantName, c.WantURL, c.State})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// checks: GetChecks reports every check of the head commit with its state
// mapped (an undocumented value is neutral), names cleaned and cut, and
// URLs that are not http(s) dropped; the strategy is documented; a commit
// with more than provider.MaxChecks checks is truncated; an older server
// still answers through a fallback; an unknown commit is not_found.
func (s *suite) checks(t *testing.T) {
	read := func(t *testing.T, spec Spec, sha string) (*provider.Checks, error) {
		t.Helper()
		p, ref, log := s.serve(t, spec)
		_, w0 := log.count()
		got, err := p.GetChecks(t.Context(), ref, sha)
		if _, w := log.count(); w != w0 {
			t.Errorf("GetChecks sent %d write requests, want none", w-w0)
		}
		checkClean(t, "GetChecks", err)
		return got, err
	}
	same := func(t *testing.T, spec Spec, got *provider.Checks) {
		t.Helper()
		if got == nil {
			t.Fatal("GetChecks returned nil checks")
		}
		if !slices.Contains(s.tr.CheckStrategies, got.Strategy) {
			t.Errorf("strategy %q is not one the provider documents %q", got.Strategy, s.tr.CheckStrategies)
		}
		if got.Truncated {
			t.Error("Truncated is set for a complete listing")
		}
		if g, w := checkKeys(got.Items), wantKeys(spec.Checks); !slices.Equal(g, w) {
			t.Errorf("checks\n got  %+v\n want %+v", g, w)
		}
	}

	t.Run("states_names_and_urls", func(t *testing.T) {
		spec := s.forHost(samplePR())
		got, err := read(t, spec, spec.HeadSHA)
		if err != nil {
			t.Fatalf("GetChecks: %v", err)
		}
		same(t, spec, got)
	})
	t.Run("unknown_state_is_neutral", func(t *testing.T) {
		spec := s.forHost(samplePR())
		spec.Checks = []Check{{Name: "mystery", Unknown: true, State: provider.CheckNeutral, WantName: "mystery"}}
		got, err := read(t, spec, spec.HeadSHA)
		if err != nil {
			t.Fatalf("GetChecks: %v", err)
		}
		same(t, spec, got)
	})
	t.Run("no_checks", func(t *testing.T) {
		spec := samplePR()
		spec.Checks = nil
		got, err := read(t, spec, spec.HeadSHA)
		if err != nil {
			t.Fatalf("GetChecks: %v", err)
		}
		if got == nil || len(got.Items) != 0 || got.Truncated {
			t.Errorf("GetChecks = %+v, want no checks", got)
		}
	})
	t.Run("at_the_cap", func(t *testing.T) {
		spec := s.manyChecks(provider.MaxChecks)
		got, err := read(t, spec, spec.HeadSHA)
		if err != nil {
			t.Fatalf("GetChecks: %v", err)
		}
		same(t, spec, got)
	})
	t.Run("truncated", func(t *testing.T) {
		spec := s.manyChecks(provider.MaxChecks + 20)
		got, err := read(t, spec, spec.HeadSHA)
		if err != nil {
			t.Fatalf("GetChecks: %v", err)
		}
		if !got.Truncated || len(got.Items) != provider.MaxChecks {
			t.Fatalf("%d checks, truncated %v; want %d and truncated", len(got.Items), got.Truncated, provider.MaxChecks)
		}
		want := map[checkKey]bool{}
		for _, k := range wantKeys(spec.Checks) {
			want[k] = true
		}
		seen := map[string]bool{}
		for _, k := range checkKeys(got.Items) {
			if !want[k] || seen[k.name] {
				t.Fatalf("check %+v is not in the spec or reported twice", k)
			}
			seen[k.name] = true
		}
	})
	t.Run("older_server", func(t *testing.T) {
		spec := s.forHost(samplePR())
		spec.Env.LegacyChecks = true
		got, err := read(t, spec, spec.HeadSHA)
		if err != nil {
			t.Fatalf("GetChecks on an older server: %v", err)
		}
		same(t, spec, got)
	})
	t.Run("unknown_commit_is_not_found", func(t *testing.T) {
		spec := s.forHost(samplePR())
		_, err := read(t, spec, "e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5")
		if !errors.Is(err, provider.ErrNotFound) {
			t.Errorf("GetChecks of an unknown commit: error %v, want not_found", err)
		}
	})
	t.Run("invalid_revision_sends_nothing", func(t *testing.T) {
		p, ref, log := s.serve(t, samplePR())
		n0, _ := log.count()
		for _, rev := range []string{"", "..", "a\nb", "a\x00b"} {
			_, err := p.GetChecks(t.Context(), ref, rev)
			if !errors.Is(err, provider.ErrProtocol) {
				t.Errorf("GetChecks(%q): error %v, want protocol", rev, err)
			}
		}
		if n, _ := log.count(); n != n0 {
			t.Errorf("%d requests were sent for invalid revisions, want none", n-n0)
		}
	})
}

// fileAt: GetFileAt returns a file's bytes at the revision asked for, byte
// for byte; a path that does not exist there is not_found; a file over
// diff.max_file_bytes is too_large; invalid input sends nothing.
func (s *suite) fileAt(t *testing.T) {
	spec := samplePR()
	p, ref, log := s.serve(t, spec)
	ctx := t.Context()
	var renamed, binary, added *File
	for i := range spec.Files {
		f := &spec.Files[i]
		switch {
		case f.Type == provider.ChangeRenamed:
			renamed = f
		case f.Binary:
			binary = f
		case f.Type == provider.ChangeAdded && !f.TooLarge:
			added = f
		}
	}
	found := []struct {
		name, path, rev, want string
	}{
		{"modified_at_base", pathModified, spec.BaseSHA, spec.Files[0].Base},
		{"modified_at_head", pathModified, spec.HeadSHA, spec.Files[0].Head},
		{"renamed_at_base_under_its_old_path", renamed.OldPath, spec.BaseSHA, renamed.Base},
		{"binary_byte_for_byte", binary.Path, spec.BaseSHA, binary.Base},
		{"unchanged_file", pathReadme, spec.BaseSHA, sampleUnchanged()[0].Content},
		{"added_at_head", added.Path, spec.HeadSHA, added.Head},
	}
	for _, c := range found {
		t.Run(c.name, func(t *testing.T) {
			got, err := p.GetFileAt(ctx, ref, c.path, c.rev)
			if err != nil {
				t.Fatalf("GetFileAt(%s): %v", c.path, err)
			}
			if string(got) != c.want {
				t.Errorf("GetFileAt(%s) = %q, want %q", c.path, got, c.want)
			}
		})
	}
	missing := []struct {
		name, path, rev string
	}{
		{"added_at_base", added.Path, spec.BaseSHA},
		{"renamed_old_path_at_head", renamed.OldPath, spec.HeadSHA},
		{"no_such_file", pathNotThere, spec.BaseSHA},
	}
	for _, c := range missing {
		t.Run(c.name, func(t *testing.T) {
			_, err := p.GetFileAt(ctx, ref, c.path, c.rev)
			if !errors.Is(err, provider.ErrNotFound) {
				t.Errorf("GetFileAt(%s): error %v, want not_found", c.path, err)
			}
			checkClean(t, "GetFileAt", err)
		})
	}
	t.Run("too_large", func(t *testing.T) {
		_, err := p.GetFileAt(ctx, ref, pathTooLarge, spec.HeadSHA)
		if !errors.Is(err, provider.ErrTooLarge) {
			t.Errorf("GetFileAt of a file over the limit: error %v, want too_large", err)
		}
		checkClean(t, "GetFileAt", err)
	})
	t.Run("invalid_input_sends_nothing", func(t *testing.T) {
		n0, w0 := log.count()
		for _, c := range []struct{ path, rev string }{
			{"", spec.BaseSHA}, {"../etc/passwd", spec.BaseSHA}, {"src//app.go", spec.BaseSHA},
			{pathModified, ""}, {pathModified, ".."},
		} {
			if _, err := p.GetFileAt(ctx, ref, c.path, c.rev); !errors.Is(err, provider.ErrProtocol) {
				t.Errorf("GetFileAt(%q, %q): error %v, want protocol", c.path, c.rev, err)
			}
		}
		if n, w := log.count(); n != n0 || w != w0 {
			t.Errorf("%d requests were sent for invalid input, want none", n-n0)
		}
	})
}

// treePR is samplePR with a directory of more than provider.MaxDirEntries
// files and one of exactly that many.
func treePR() Spec {
	spec := samplePR()
	spec.Threads, spec.Reviewers, spec.Checks = nil, nil, nil
	for i := range provider.MaxDirEntries + 10 {
		spec.Unchanged = append(spec.Unchanged, RepoFile{Path: fmt.Sprintf("%s/f%04d.txt", dirOverCap, i), Content: "x\n"})
	}
	for i := range provider.MaxDirEntries {
		spec.Unchanged = append(spec.Unchanged, RepoFile{Path: fmt.Sprintf("%s/f%04d.txt", dirAtCap, i), Content: "x\n"})
	}
	return spec
}

// listDir: ListDir returns a directory's direct entries at the revision
// asked for, sorted by name, subdirectories marked; the root is ""; a
// directory that does not exist is not_found; a file is a protocol error;
// more than provider.MaxDirEntries entries are truncated.
func (s *suite) listDir(t *testing.T) {
	spec := treePR()
	p, ref, log := s.serve(t, spec)
	ctx := t.Context()
	list := func(t *testing.T, dir, rev string) *provider.DirListing {
		t.Helper()
		got, err := p.ListDir(ctx, ref, dir, rev)
		if err != nil {
			t.Fatalf("ListDir(%q): %v", dir, err)
		}
		if got == nil {
			t.Fatalf("ListDir(%q) returned nil", dir)
		}
		return got
	}
	t.Run("root_at_base", func(t *testing.T) {
		got := list(t, "", spec.BaseSHA)
		want := []provider.DirEntry{
			{Name: pathReadme}, {Name: "assets", Dir: true}, {Name: "docs", Dir: true}, {Name: "gen", Dir: true},
			{Name: "old", Dir: true}, {Name: "pkg", Dir: true}, {Name: "src", Dir: true},
		}
		if !slices.Equal(got.Entries, want) || got.Truncated {
			t.Errorf("ListDir(root) = %+v, want %+v", got, want)
		}
	})
	t.Run("revision_matters", func(t *testing.T) {
		base := list(t, "src", spec.BaseSHA)
		head := list(t, "src", spec.HeadSHA)
		wantBase := []provider.DirEntry{{Name: "app.go"}, {Name: "util", Dir: true}}
		wantHead := []provider.DirEntry{{Name: "added.go"}, {Name: "app.go"}, {Name: "util", Dir: true}}
		if !slices.Equal(base.Entries, wantBase) || !slices.Equal(head.Entries, wantHead) {
			t.Errorf("src at base %+v, at head %+v; want %+v and %+v", base.Entries, head.Entries, wantBase, wantHead)
		}
	})
	t.Run("nested", func(t *testing.T) {
		got := list(t, "src/util", spec.BaseSHA)
		if want, _ := spec.DirAt(spec.BaseSHA, "src/util"); !slices.Equal(got.Entries, want) || len(want) != 1 {
			t.Errorf("ListDir(src/util) = %+v, want %+v", got.Entries, want)
		}
	})
	t.Run("at_the_cap", func(t *testing.T) {
		got := list(t, dirAtCap, spec.BaseSHA)
		want, _ := spec.DirAt(spec.BaseSHA, dirAtCap)
		if got.Truncated || !slices.Equal(got.Entries, want) {
			t.Errorf("ListDir(%s): %d entries, truncated %v; want all %d", dirAtCap, len(got.Entries), got.Truncated, len(want))
		}
	})
	t.Run("truncated", func(t *testing.T) {
		got := list(t, dirOverCap, spec.BaseSHA)
		if !got.Truncated || len(got.Entries) != provider.MaxDirEntries {
			t.Fatalf("ListDir(%s): %d entries, truncated %v; want %d and truncated", dirOverCap, len(got.Entries), got.Truncated, provider.MaxDirEntries)
		}
		all, _ := spec.DirAt(spec.BaseSHA, dirOverCap)
		if !slices.IsSortedFunc(got.Entries, func(a, b provider.DirEntry) int { return strings.Compare(a.Name, b.Name) }) {
			t.Error("entries are not sorted by name")
		}
		for _, e := range got.Entries {
			if !slices.Contains(all, e) {
				t.Errorf("entry %+v is not in the directory", e)
			}
		}
	})
	for _, c := range []struct{ name, dir, rev string }{
		{"no_such_directory", "nowhere", spec.BaseSHA},
		{"directory_only_at_head", "data", spec.BaseSHA},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := p.ListDir(ctx, ref, c.dir, c.rev)
			if !errors.Is(err, provider.ErrNotFound) {
				t.Errorf("ListDir(%q): error %v, want not_found", c.dir, err)
			}
			checkClean(t, "ListDir", err)
		})
	}
	t.Run("a_file_is_not_a_directory", func(t *testing.T) {
		_, err := p.ListDir(ctx, ref, pathModified, spec.BaseSHA)
		if !errors.Is(err, provider.ErrProtocol) {
			t.Errorf("ListDir of a file: error %v, want protocol", err)
		}
		checkClean(t, "ListDir", err)
	})
	t.Run("invalid_input_sends_nothing", func(t *testing.T) {
		n0, _ := log.count()
		for _, c := range []struct{ dir, rev string }{{"../x", spec.BaseSHA}, {"src//util", spec.BaseSHA}, {"src", ""}} {
			if _, err := p.ListDir(ctx, ref, c.dir, c.rev); !errors.Is(err, provider.ErrProtocol) {
				t.Errorf("ListDir(%q, %q): error %v, want protocol", c.dir, c.rev, err)
			}
		}
		if n, _ := log.count(); n != n0 {
			t.Errorf("%d requests were sent for invalid input, want none", n-n0)
		}
	})
}
