package incremental

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/gitctx"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

const (
	lastSHA = "1111111111111111111111111111111111111111"
	headSHA = "2222222222222222222222222222222222222222"
)

var testRef = provider.PRRef{Kind: provider.KindGitea, URL: "https://git.example.test/owner/repo/pulls/7", Namespace: "owner", Repo: "repo", Number: 7}

// ---- fakes ----

// fakeReader serves file contents per revision. A path missing from a
// revision is not_found; errs overrides a (revision, path) with an error.
type fakeReader struct {
	files   map[string]map[string]string
	errs    map[string]error // rev + ":" + path
	listErr error
	// delay is how long every GetFileAt takes; block makes it wait for ctx.
	delay time.Duration
	block bool

	mu       sync.Mutex
	reads    []string // rev + ":" + path
	lists    int
	inFlight atomic.Int32
	maxSeen  atomic.Int32
}

func (f *fakeReader) GetFileAt(ctx context.Context, _ provider.PRRef, path, rev string) ([]byte, error) {
	n := f.inFlight.Add(1)
	defer f.inFlight.Add(-1)
	for {
		m := f.maxSeen.Load()
		if n <= m || f.maxSeen.CompareAndSwap(m, n) {
			break
		}
	}
	f.mu.Lock()
	f.reads = append(f.reads, rev+":"+path)
	f.mu.Unlock()
	if f.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	if err := f.errs[rev+":"+path]; err != nil {
		return nil, err
	}
	c, ok := f.files[rev][path]
	if !ok {
		return nil, &provider.Error{Class: provider.ClassNotFound}
	}
	return []byte(c), nil
}

func (f *fakeReader) ListDir(_ context.Context, _ provider.PRRef, _, _ string) (*provider.DirListing, error) {
	f.mu.Lock()
	f.lists++
	f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	return &provider.DirListing{}, nil
}

func (f *fakeReader) readCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reads)
}

// fakeLocal is a repository-context cache holding the commits in has.
type fakeLocal struct {
	has   map[string]bool
	paths []string
	err   error
	calls [][2]string
}

func (l *fakeLocal) HasCommit(_ context.Context, _ gitctx.Checkout, rev string) bool {
	return l.has[rev]
}

func (l *fakeLocal) ChangedPaths(ctx context.Context, _ gitctx.Checkout, from, to string) ([]string, error) {
	l.calls = append(l.calls, [2]string{from, to})
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return l.paths, l.err
}

func str(s string) *string { return &s }

// modified is a listed file whose head content the diff did not fetch.
func modified(path string) provider.FilePatch {
	return provider.FilePatch{Path: path, Type: provider.ChangeModified, HeadStatus: provider.ContentNotFetchedFileCap}
}

// withHead is a listed file whose head content the diff fetched in full.
func withHead(path, content string) provider.FilePatch {
	return provider.FilePatch{Path: path, Type: provider.ChangeModified, HeadContent: str(content), HeadStatus: provider.ContentFull}
}

func renamed(path, old string) provider.FilePatch {
	return provider.FilePatch{Path: path, OldPath: old, Type: provider.ChangeRenamed, HeadStatus: provider.ContentNotFetchedFileCap}
}

func deleted(path string) provider.FilePatch {
	return provider.FilePatch{Path: path, Type: provider.ChangeDeleted, HeadStatus: provider.ContentNotApplicable}
}

func added(path string) provider.FilePatch {
	return provider.FilePatch{Path: path, Type: provider.ChangeAdded, HeadStatus: provider.ContentNotFetchedFileCap}
}

func statesOf(r Result) map[string]State {
	out := map[string]State{}
	for _, f := range r.Files {
		out[f.Path] = f.State
	}
	return out
}

func wantStates(t *testing.T, r Result, want map[string]State) {
	t.Helper()
	got := statesOf(r)
	for p, w := range want {
		if got[p] != w {
			t.Errorf("%s: %s, want %s", p, got[p], w)
		}
	}
	if len(r.Files) != len(want) {
		t.Errorf("%d files classified, want %d", len(r.Files), len(want))
	}
}

func wantFull(t *testing.T, r Result, err error, note string) {
	t.Helper()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !r.Full || r.Strategy != "" || r.Files != nil || !slices.Equal(r.Notes, []string{note}) {
		t.Fatalf("result = %+v, want a full review with the note %q", r, note)
	}
}

// apiFixture is the pull request of the API cases: every kind of file, with
// its content at the last reviewed commit and at the head.
func apiFixture() ([]provider.FilePatch, *fakeReader, map[string]State) {
	files := []provider.FilePatch{
		withHead("unchanged.go", "package a\n"),
		withHead("edited.go", "package a // edited\n"),
		modified("reverted.go"),
		modified("unchanged-read.go"),
		renamed("since.go", "before.go"),
		renamed("renamed-earlier.go", "orig.go"),
		deleted("deleted-earlier.go"),
		deleted("deleted-since.go"),
		added("added-since.go"),
		added("added-earlier.go"),
		modified("big-at-last.go"),
		withHead("broken-at-last.go", "package a\n"),
		modified("broken-at-head.go"),
		modified("big-at-head.go"),
		modified("dir with space/ünï.go"),
	}
	r := &fakeReader{
		files: map[string]map[string]string{
			lastSHA: {
				"unchanged.go":          "package a\n",
				"edited.go":             "package a\n",
				"reverted.go":           "package a\n",
				"unchanged-read.go":     "package a\n",
				"before.go":             "package a\n",
				"renamed-earlier.go":    "package a\n",
				"deleted-since.go":      "package a\n",
				"added-earlier.go":      "package a\n",
				"broken-at-head.go":     "package a\n",
				"big-at-head.go":        "package a\n",
				"dir with space/ünï.go": "package a\n",
			},
			headSHA: {
				"reverted.go":           "package a\n",
				"unchanged-read.go":     "package a\n",
				"since.go":              "package a\n",
				"renamed-earlier.go":    "package a\n",
				"added-since.go":        "package a\n",
				"added-earlier.go":      "package a\n",
				"big-at-last.go":        "package a\n",
				"dir with space/ünï.go": "package a // ü\n",
			},
		},
		errs: map[string]error{
			lastSHA + ":big-at-last.go":     &provider.Error{Class: provider.ClassTooLarge},
			lastSHA + ":broken-at-last.go":  &provider.Error{Class: provider.ClassUpstream},
			headSHA + ":broken-at-head.go":  errors.New("connection reset"),
			headSHA + ":big-at-head.go":     &provider.Error{Class: provider.ClassTooLarge},
			headSHA + ":unrelated-path.txt": errors.New("never read"),
		},
	}
	want := map[string]State{
		"unchanged.go":          Unchanged,
		"edited.go":             Changed,
		"reverted.go":           Unchanged, // edited and reverted since: the content decides
		"unchanged-read.go":     Unchanged,
		"since.go":              Changed, // renamed since the last review: absent there
		"renamed-earlier.go":    Unchanged,
		"deleted-earlier.go":    Unchanged, // absent at the last commit too
		"deleted-since.go":      Changed,
		"added-since.go":        Changed,
		"added-earlier.go":      Unchanged,
		"big-at-last.go":        Changed,
		"broken-at-last.go":     Changed,
		"broken-at-head.go":     Changed,
		"big-at-head.go":        Changed,
		"dir with space/ünï.go": Changed,
	}
	return files, r, want
}

// ---- API strategy ----

// TestAPIStrategy: every file is decided by its content at the last
// reviewed commit and at the head; doubt is changed.
func TestAPIStrategy(t *testing.T) {
	files, r, want := apiFixture()
	res, err := Detect(context.Background(), Input{Ref: testRef, Files: files, Head: headSHA, Last: lastSHA, Provider: r})
	if err != nil {
		t.Fatal(err)
	}
	if res.Full || res.Strategy != StrategyAPI || len(res.Notes) != 0 {
		t.Fatalf("result = %+v, want the API strategy without notes", res)
	}
	wantStates(t, res, want)
	for i, f := range res.Files {
		if f.Path != files[i].Path || f.OldPath != files[i].OldPath {
			t.Errorf("file %d = %s (%s), want %s (%s): the order or the paths changed", i, f.Path, f.OldPath, files[i].Path, files[i].OldPath)
		}
	}
	if c, u := res.Counts(); c != 9 || u != 6 {
		t.Errorf("Counts = %d changed, %d unchanged, want 9 and 6", c, u)
	}
	// The diff's full head content is used, not read again; a file absent or
	// unreadable at the last commit is not read at the head.
	for _, rd := range r.reads {
		for _, p := range []string{"unchanged.go", "edited.go", "broken-at-last.go", "since.go", "added-since.go", "big-at-last.go"} {
			if rd == headSHA+":"+p {
				t.Errorf("%s was read at the head", p)
			}
		}
		if strings.Contains(rd, "before.go") || strings.Contains(rd, "orig.go") {
			t.Errorf("an old path was read: %s", rd)
		}
	}
	if r.lists != 1 {
		t.Errorf("%d probes of the last commit, want 1", r.lists)
	}
}

// TestAPIReadErrorIsChanged [canary]: a file that cannot be read at the last
// reviewed commit, or at the head, is changed, never skipped.
func TestAPIReadErrorIsChanged(t *testing.T) {
	files := []provider.FilePatch{withHead("last-fails.go", "x\n"), modified("head-fails.go"), withHead("too-large.go", "x\n")}
	r := &fakeReader{
		files: map[string]map[string]string{lastSHA: {"head-fails.go": "x\n"}},
		errs: map[string]error{
			lastSHA + ":last-fails.go": &provider.Error{Class: provider.ClassUpstream},
			headSHA + ":head-fails.go": &provider.Error{Class: provider.ClassTransport},
			lastSHA + ":too-large.go":  &provider.Error{Class: provider.ClassTooLarge},
		},
	}
	res, err := Detect(context.Background(), Input{Ref: testRef, Files: files, Head: headSHA, Last: lastSHA, Provider: r})
	if err != nil {
		t.Fatal(err)
	}
	wantStates(t, res, map[string]State{"last-fails.go": Changed, "head-fails.go": Changed, "too-large.go": Changed})
}

// TestForcePushContentDecides: the last reviewed commit is no ancestor of
// the head (a force-push), but it still exists: the content decides.
func TestForcePushContentDecides(t *testing.T) {
	files := []provider.FilePatch{withHead("same.go", "v1\n"), withHead("rebased.go", "v2\n")}
	r := &fakeReader{files: map[string]map[string]string{lastSHA: {"same.go": "v1\n", "rebased.go": "v1\n"}}}
	res, err := Detect(context.Background(), Input{Ref: testRef, Files: files, Head: headSHA, Last: lastSHA, Provider: r})
	if err != nil {
		t.Fatal(err)
	}
	wantStates(t, res, map[string]State{"same.go": Unchanged, "rebased.go": Changed})
}

// TestUnusableLastCommit: the last reviewed commit is checked before any
// file is read: gone is a full review with the first note, any other error
// one with the second; no file is read.
func TestUnusableLastCommit(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		note string
	}{
		"gone":        {&provider.Error{Class: provider.ClassNotFound}, NoteCommitGone},
		"server":      {&provider.Error{Class: provider.ClassUpstream}, NoteNotCompared},
		"auth":        {&provider.Error{Class: provider.ClassAuth}, NoteNotCompared},
		"plain error": {errors.New("boom"), NoteNotCompared},
	} {
		t.Run(name, func(t *testing.T) {
			files, r, _ := apiFixture()
			r.listErr = tc.err
			res, err := Detect(context.Background(), Input{Ref: testRef, Files: files, Head: headSHA, Last: lastSHA, Provider: r})
			wantFull(t, res, err, tc.note)
			if n := r.readCount(); n != 0 {
				t.Errorf("%d files read after an unusable last commit", n)
			}
		})
	}
}

// TestTooManyFiles: above the limit nothing is read, not even the probe,
// and the review is full with the note; at the limit the files are
// compared. The default limit is 300.
func TestTooManyFiles(t *testing.T) {
	many := func(n int) []provider.FilePatch {
		out := make([]provider.FilePatch, n)
		for i := range out {
			out[i] = modified("f" + strconv.Itoa(i) + ".go")
		}
		return out
	}
	r := &fakeReader{}
	res, err := Detect(context.Background(), Input{Ref: testRef, Files: many(4), Head: headSHA, Last: lastSHA, Provider: r, MaxFiles: 3})
	wantFull(t, res, err, "The pull request has more than 3 files, so it was not compared with the last review and was reviewed in full.")
	if r.readCount() != 0 || r.lists != 0 {
		t.Errorf("%d GetFileAt and %d ListDir calls above the limit, want none", r.readCount(), r.lists)
	}

	r = &fakeReader{}
	res, err = Detect(context.Background(), Input{Ref: testRef, Files: many(DefaultMaxFiles + 1), Head: headSHA, Last: lastSHA, Provider: r})
	wantFull(t, res, err, NoteTooManyFiles(300))
	if r.readCount() != 0 || r.lists != 0 {
		t.Errorf("%d GetFileAt and %d ListDir calls above the default limit, want none", r.readCount(), r.lists)
	}

	r = &fakeReader{}
	res, err = Detect(context.Background(), Input{Ref: testRef, Files: many(3), Head: headSHA, Last: lastSHA, Provider: r, MaxFiles: 3})
	if err != nil || res.Full || res.Strategy != StrategyAPI || len(res.Files) != 3 {
		t.Errorf("at the limit: %+v, %v, want a comparison", res, err)
	}
}

// TestUnusableSHAs: a head or last commit that is not a full SHA cannot be
// compared; nothing is read.
func TestUnusableSHAs(t *testing.T) {
	for _, revs := range [][2]string{{"", lastSHA}, {headSHA, ""}, {headSHA, "HEAD"}, {headSHA, lastSHA[:12]}, {"--output=x", lastSHA}} {
		r := &fakeReader{}
		res, err := Detect(context.Background(), Input{Ref: testRef, Files: []provider.FilePatch{modified("a.go")}, Head: revs[0], Last: revs[1], Provider: r})
		wantFull(t, res, err, NoteNotCompared)
		if r.readCount() != 0 || r.lists != 0 {
			t.Errorf("%q: the provider was asked", revs)
		}
	}
}

// TestConcurrencyBound: the reads run in parallel, never more than the
// bound at once; the default bound is the providers' own.
func TestConcurrencyBound(t *testing.T) {
	files := make([]provider.FilePatch, 24)
	for i := range files {
		files[i] = withHead("f"+strconv.Itoa(i)+".go", "x\n")
	}
	for _, tc := range []struct{ conc, want int }{{3, 3}, {0, provider.ContentFetchConcurrency}, {1, 1}} {
		r := &fakeReader{delay: 20 * time.Millisecond}
		res, err := Detect(context.Background(), Input{Ref: testRef, Files: files, Head: headSHA, Last: lastSHA, Provider: r, Concurrency: tc.conc})
		if err != nil || res.Full || len(res.Files) != len(files) {
			t.Fatalf("concurrency %d: %+v, %v", tc.conc, res, err)
		}
		if got := int(r.maxSeen.Load()); got != tc.want {
			t.Errorf("concurrency %d: %d reads at once, want %d", tc.conc, got, tc.want)
		}
	}
}

// TestCancellation: a canceled context ends the detection with its error;
// every read has returned by then and no further one starts.
func TestCancellation(t *testing.T) {
	files, _, _ := apiFixture()
	t.Run("before", func(t *testing.T) {
		r := &fakeReader{}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := Detect(ctx, Input{Ref: testRef, Files: files, Head: headSHA, Last: lastSHA, Provider: r}); !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
		if r.readCount() != 0 || r.lists != 0 {
			t.Error("the provider was asked after cancellation")
		}
	})
	t.Run("during the reads", func(t *testing.T) {
		r := &fakeReader{block: true}
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			for r.inFlight.Load() < int32(provider.ContentFetchConcurrency) {
				time.Sleep(time.Millisecond)
			}
			cancel()
		}()
		done := make(chan error, 1)
		go func() {
			_, err := Detect(ctx, Input{Ref: testRef, Files: files, Head: headSHA, Last: lastSHA, Provider: r})
			done <- err
		}()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("err = %v, want context.Canceled", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("Detect did not return after cancellation")
		}
		if n := r.inFlight.Load(); n != 0 {
			t.Errorf("%d reads still running after Detect returned", n)
		}
		if n := r.readCount(); n != provider.ContentFetchConcurrency {
			t.Errorf("%d reads started, want only the first %d", n, provider.ContentFetchConcurrency)
		}
	})
	t.Run("during the probe", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		r := &fakeReader{listErr: context.Canceled}
		cancel()
		_, err := Detect(ctx, Input{Ref: testRef, Files: files, Head: headSHA, Last: lastSHA, Provider: r})
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	})
}

// ---- strategy choice (fake cache) ----

// TestLocalStrategy: with both commits in the cache, git's list decides; a
// file is changed when its path or its old path is listed; the provider is
// not asked.
func TestLocalStrategy(t *testing.T) {
	files := []provider.FilePatch{
		modified("same.go"), modified("edited.go"), renamed("new.go", "old.go"), renamed("moved.go", "kept.go"),
		deleted("deleted-earlier.go"), deleted("deleted-since.go"), modified("dir with space/ünï.go"),
	}
	l := &fakeLocal{
		has:   map[string]bool{lastSHA: true, headSHA: true},
		paths: []string{"edited.go", "old.go", "deleted-since.go", "dir with space/ünï.go", "unrelated.go"},
	}
	r := &fakeReader{}
	res, err := Detect(context.Background(), Input{Ref: testRef, Files: files, Head: strings.ToUpper(headSHA), Last: lastSHA, Provider: r, Local: l})
	if err != nil {
		t.Fatal(err)
	}
	if res.Full || res.Strategy != StrategyGit || len(res.Notes) != 0 {
		t.Fatalf("result = %+v, want the git strategy", res)
	}
	wantStates(t, res, map[string]State{
		"same.go": Unchanged, "edited.go": Changed, "new.go": Changed, "moved.go": Unchanged,
		"deleted-earlier.go": Unchanged, "deleted-since.go": Changed, "dir with space/ünï.go": Changed,
	})
	if !slices.Equal(l.calls, [][2]string{{lastSHA, headSHA}}) {
		t.Errorf("ChangedPaths calls = %q, want one from the last commit to the head", l.calls)
	}
	if r.readCount() != 0 || r.lists != 0 {
		t.Error("the provider was asked although the cache answered")
	}
}

// TestLocalFallsBackToAPI: the cache is used only when it holds both
// commits; a failed comparison there falls back to the provider; never to
// a skip.
func TestLocalFallsBackToAPI(t *testing.T) {
	for name, l := range map[string]*fakeLocal{
		"lacks the last commit": {has: map[string]bool{headSHA: true}},
		"lacks the head":        {has: map[string]bool{lastSHA: true}},
		"git fails":             {has: map[string]bool{lastSHA: true, headSHA: true}, err: &gitctx.Error{Reason: gitctx.ReasonGitFailed}},
		"output too large":      {has: map[string]bool{lastSHA: true, headSHA: true}, err: &gitctx.Error{Reason: gitctx.ReasonTooLarge}},
	} {
		t.Run(name, func(t *testing.T) {
			files, r, want := apiFixture()
			res, err := Detect(context.Background(), Input{Ref: testRef, Files: files, Head: headSHA, Last: lastSHA, Provider: r, Local: l})
			if err != nil || res.Strategy != StrategyAPI {
				t.Fatalf("result = %+v, %v, want the API strategy", res, err)
			}
			wantStates(t, res, want)
			if r.lists != 1 {
				t.Errorf("%d probes of the last commit through the provider, want 1", r.lists)
			}
		})
	}
	t.Run("gone everywhere", func(t *testing.T) {
		files, r, _ := apiFixture()
		r.listErr = &provider.Error{Class: provider.ClassNotFound}
		res, err := Detect(context.Background(), Input{Ref: testRef, Files: files, Head: headSHA, Last: lastSHA, Provider: r,
			Local: &fakeLocal{has: map[string]bool{headSHA: true}}})
		wantFull(t, res, err, NoteCommitGone)
	})
	t.Run("canceled during git", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		l := &fakeLocal{has: map[string]bool{lastSHA: true, headSHA: true}}
		cancel()
		r := &fakeReader{}
		_, err := Detect(ctx, Input{Ref: testRef, Files: []provider.FilePatch{modified("a.go")}, Head: headSHA, Last: lastSHA, Provider: r, Local: l})
		if !errors.Is(err, context.Canceled) || r.lists != 0 {
			t.Errorf("err = %v (%d probes), want context.Canceled and no provider call", err, r.lists)
		}
	})
}

// TestNotes: the fixed sentences, verbatim (spec §3 item 4).
func TestNotes(t *testing.T) {
	for got, want := range map[string]string{
		NoteCommitGone:        "The commit of the last review is no longer available, so the whole pull request was reviewed.",
		NoteNotCompared:       "The last review could not be compared with the current head, so the whole pull request was reviewed.",
		NoteTooManyFiles(300): "The pull request has more than 300 files, so it was not compared with the last review and was reviewed in full.",
	} {
		if got != want {
			t.Errorf("note = %q, want %q", got, want)
		}
	}
}
