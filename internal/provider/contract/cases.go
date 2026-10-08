package contract

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

type suite struct {
	f  Fixture
	tr Traits
}

// serve serves pr through the fixture with a fresh request log.
func (s *suite) serve(t *testing.T, pr Spec) (provider.Provider, provider.PRRef, *requestLog) {
	t.Helper()
	log := &requestLog{}
	pr.Env.log = log
	p, ref := s.f.Serve(t, pr)
	if p == nil {
		t.Fatal("Serve returned a nil provider")
	}
	if !log.isStarted() {
		t.Fatal("the fixture did not serve through contract.StartServer")
	}
	if p.Kind() != s.f.Kind() || ref.Kind != s.f.Kind() {
		t.Fatalf("kinds differ: fixture %q, provider %q, ref %q", s.f.Kind(), p.Kind(), ref.Kind)
	}
	return p, ref, log
}

func mustPR(t *testing.T, p provider.Provider, ref provider.PRRef) *provider.PullRequest {
	t.Helper()
	pr, err := p.GetPullRequest(t.Context(), ref)
	if err != nil {
		t.Fatalf("GetPullRequest: %v", err)
	}
	return pr
}

// diffView indexes a Diff by path. A path reported more than once fails t.
type diffView struct {
	d       *provider.Diff
	files   map[string]*provider.FilePatch
	skipped map[string]string
}

func mustDiff(t *testing.T, p provider.Provider, ref provider.PRRef) diffView {
	t.Helper()
	pr := mustPR(t, p, ref)
	d, err := p.GetDiff(t.Context(), ref, pr, provider.DiffOptions{})
	if err != nil {
		t.Fatalf("GetDiff: %v", err)
	}
	v := diffView{d: d, files: map[string]*provider.FilePatch{}, skipped: map[string]string{}}
	for i := range d.Files {
		fp := &d.Files[i]
		if _, dup := v.files[fp.Path]; dup {
			t.Fatalf("%s is listed twice", fp.Path)
		}
		v.files[fp.Path] = fp
	}
	for _, sk := range d.Skipped {
		if _, dup := v.skipped[sk.Path]; dup {
			t.Fatalf("%s is skipped twice", sk.Path)
		}
		v.skipped[sk.Path] = sk.Reason
	}
	return v
}

// fileCase names the contract case a spec file stands for.
func fileCase(f *File) string {
	switch {
	case f.Binary:
		return "binary"
	case f.TooLarge:
		return "too_large"
	case f.Type == provider.ChangeRenamed:
		return "renamed_with_edits"
	case f.Type != provider.ChangeDeleted && !strings.HasSuffix(f.Head, "\n"):
		return "no_trailing_newline"
	}
	return string(f.Type)
}

// expectedHunks is f.Hunks as the provider must return it.
func (s *suite) expectedHunks(f *File) string {
	if !s.tr.OmitsNoNewlineMarker {
		return f.Hunks
	}
	return strings.ReplaceAll(f.Hunks, noNewlineMarker+"\n", "")
}

func countChanges(hunks string) (add, del int) {
	for _, l := range strings.Split(hunks, "\n") {
		switch {
		case strings.HasPrefix(l, "+"):
			add++
		case strings.HasPrefix(l, "-"):
			del++
		}
	}
	return add, del
}

// metadata: GetPullRequest returns the spec's metadata, and the base
// strategy is one the provider documents.
func (s *suite) metadata(t *testing.T) {
	spec := samplePR()
	p, ref, _ := s.serve(t, spec)
	pr := mustPR(t, p, ref)
	for _, c := range []struct{ field, got, want string }{
		{"Title", pr.Title, spec.Title},
		{"Description", pr.Description, spec.Description},
		{"Author", pr.Author, spec.Author.Login},
		{"SourceBranch", pr.SourceBranch, spec.SourceBranch},
		{"TargetBranch", pr.TargetBranch, spec.TargetBranch},
		{"HeadSHA", pr.HeadSHA, spec.HeadSHA},
		{"BaseSHA", pr.BaseSHA, spec.BaseSHA},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.field, c.got, c.want)
		}
	}
	if !slices.Contains(s.tr.BaseStrategies, pr.BaseStrategy) {
		t.Errorf("BaseStrategy = %q, want one of %q", pr.BaseStrategy, s.tr.BaseStrategies)
	}
	d, err := p.GetDiff(t.Context(), ref, pr, provider.DiffOptions{})
	if err != nil {
		t.Fatalf("GetDiff: %v", err)
	}
	if d.BaseStrategy != pr.BaseStrategy {
		t.Errorf("Diff.BaseStrategy = %q, want the pull request's %q", d.BaseStrategy, pr.BaseStrategy)
	}
}

// fileList: every file is reported once, in the host's order, with its
// change type and old path; binary and oversized files are reported as such.
func (s *suite) fileList(t *testing.T) {
	spec := samplePR()
	p, ref, _ := s.serve(t, spec)
	v := mustDiff(t, p, ref)

	t.Run("each_file_once_in_order", func(t *testing.T) {
		var want []string
		for i := range spec.Files {
			f := &spec.Files[i]
			_, listed := v.files[f.Path]
			_, skipped := v.skipped[f.Path]
			if listed == skipped {
				t.Errorf("%s: listed %v and skipped %v, want exactly one", f.Path, listed, skipped)
			}
			if listed {
				want = append(want, f.Path)
			}
		}
		var got []string
		for _, fp := range v.d.Files {
			got = append(got, fp.Path)
		}
		if !slices.Equal(got, want) {
			t.Errorf("listed files = %q, want %q (the host's order)", got, want)
		}
		if n := len(v.files) + len(v.skipped); n != len(spec.Files) {
			t.Errorf("%d files reported, want %d", n, len(spec.Files))
		}
	})
	for i := range spec.Files {
		f := &spec.Files[i]
		t.Run(fileCase(f), func(t *testing.T) {
			fp, reason := v.files[f.Path], v.skipped[f.Path]
			switch {
			case f.Binary:
				if fp != nil || reason != provider.SkipBinary {
					t.Errorf("%s: want skipped %q only; listed %v, skip reason %q", f.Path, provider.SkipBinary, fp != nil, reason)
				}
			case f.TooLarge:
				s.checkLimited(t, f, fp, reason, provider.SkipSizeLimit, provider.ContentNotFetchedSizeCap)
			default:
				if fp == nil {
					t.Fatalf("%s: not listed (skip reason %q)", f.Path, reason)
				}
				if fp.Type != f.Type || fp.OldPath != f.OldPath || fp.Binary {
					t.Errorf("%s: type %q, old path %q, binary %v; want %q, %q, false",
						f.Path, fp.Type, fp.OldPath, fp.Binary, f.Type, f.OldPath)
				}
				if f.Type == provider.ChangeRenamed && fp.Patch == "" {
					t.Errorf("%s: a rename with edits has no hunks", f.Path)
				}
			}
		})
	}
}

// checkLimited checks a file a diff limit applies to. A provider either
// skips it with reason, or, when it has the patch without the contents,
// lists it with the patch and every existing side's content not fetched
// with status (see the DESIGN-QUESTION of WP-2a).
func (s *suite) checkLimited(t *testing.T, f *File, fp *provider.FilePatch, reason, skipReason string, status provider.ContentStatus) {
	t.Helper()
	switch {
	case fp != nil && reason != "":
		t.Errorf("%s: both listed and skipped (%q)", f.Path, reason)
	case fp == nil && reason != skipReason:
		t.Errorf("%s: skip reason %q, want %q (or listed with content status %q)", f.Path, reason, skipReason, status)
	case fp != nil:
		if want := s.expectedHunks(f); fp.Patch != want {
			t.Errorf("%s: listed with patch\n%q\nwant\n%q", f.Path, fp.Patch, want)
		}
		if f.Type != provider.ChangeAdded && (fp.BaseStatus != status || fp.BaseContent != nil) {
			t.Errorf("%s: base status %q (content set %v), want %q without content", f.Path, fp.BaseStatus, fp.BaseContent != nil, status)
		}
		if f.Type != provider.ChangeDeleted && (fp.HeadStatus != status || fp.HeadContent != nil) {
			t.Errorf("%s: head status %q (content set %v), want %q without content", f.Path, fp.HeadStatus, fp.HeadContent != nil, status)
		}
	}
}

// fileLimit: files within the file cap are fetched in full; a file beyond
// it is reported as file_limit.
func (s *suite) fileLimit(t *testing.T) {
	spec := fileLimitPR()
	p, ref, _ := s.serve(t, spec)
	v := mustDiff(t, p, ref)
	within, beyond := spec.Files[:spec.Env.MaxFiles], spec.Files[spec.Env.MaxFiles:]
	t.Run("within_cap", func(t *testing.T) {
		for i := range within {
			f := &within[i]
			fp := v.files[f.Path]
			switch {
			case fp == nil:
				t.Errorf("%s: not listed (skip reason %q)", f.Path, v.skipped[f.Path])
			case fp.BaseStatus != provider.ContentFull || fp.HeadStatus != provider.ContentFull:
				t.Errorf("%s: statuses %q/%q, want full/full", f.Path, fp.BaseStatus, fp.HeadStatus)
			}
		}
	})
	t.Run("beyond_cap", func(t *testing.T) {
		for i := range beyond {
			f := &beyond[i]
			s.checkLimited(t, f, v.files[f.Path], v.skipped[f.Path], provider.SkipFileLimit, provider.ContentNotFetchedFileCap)
		}
	})
}

// hunksAndContent: every listed file's hunks, counts and contents match the
// spec byte for byte.
func (s *suite) hunksAndContent(t *testing.T) {
	spec := samplePR()
	p, ref, _ := s.serve(t, spec)
	v := mustDiff(t, p, ref)
	for i := range spec.Files {
		f := &spec.Files[i]
		if f.Binary || f.TooLarge {
			continue
		}
		t.Run(fileCase(f), func(t *testing.T) {
			fp := v.files[f.Path]
			if fp == nil {
				t.Fatalf("%s: not listed", f.Path)
			}
			want := s.expectedHunks(f)
			if fp.Patch != want {
				t.Errorf("%s: patch\n%q\nwant\n%q", f.Path, fp.Patch, want)
			}
			if add, del := countChanges(want); fp.Additions != add || fp.Deletions != del {
				t.Errorf("%s: +%d -%d, want +%d -%d", f.Path, fp.Additions, fp.Deletions, add, del)
			}
			checkSide(t, f.Path+" base", fp.BaseContent, fp.BaseStatus, f.Base, f.Type == provider.ChangeAdded)
			checkSide(t, f.Path+" head", fp.HeadContent, fp.HeadStatus, f.Head, f.Type == provider.ChangeDeleted)
		})
	}
}

func checkSide(t *testing.T, what string, got *string, status provider.ContentStatus, want string, absent bool) {
	t.Helper()
	if absent {
		if got != nil || status != provider.ContentNotApplicable {
			t.Errorf("%s: status %q (content set %v), want %q without content", what, status, got != nil, provider.ContentNotApplicable)
		}
		return
	}
	switch {
	case status != provider.ContentFull || got == nil:
		t.Errorf("%s: status %q (content set %v), want %q", what, status, got != nil, provider.ContentFull)
	case *got != want:
		t.Errorf("%s: content\n%q\nwant\n%q", what, *got, want)
	}
}

// threads: general threads first, replies oldest first, and a resolved
// state only where the provider reports resolution.
func (s *suite) threads(t *testing.T) {
	spec := samplePR()
	p, ref, _ := s.serve(t, spec)
	got, err := p.ListThreads(t.Context(), ref)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	want := slices.Clone(spec.Threads)
	sort.SliceStable(want, func(i, j int) bool {
		a, b := &want[i], &want[j]
		if a.Kind != b.Kind {
			return a.Kind == provider.ThreadGeneral
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Comments[0].Created.Before(b.Comments[0].Created)
	})

	t.Run("general_before_inline", func(t *testing.T) {
		var g, w []string
		for _, th := range got {
			g = append(g, string(th.Kind)+":"+th.ID)
		}
		for _, th := range want {
			w = append(w, string(th.Kind)+":"+strconv.FormatInt(th.Comments[0].ID, 10))
		}
		if !slices.Equal(g, w) {
			t.Errorf("threads = %q, want %q", g, w)
		}
	})
	byID := map[string]*provider.Thread{}
	for i := range got {
		byID[got[i].ID] = &got[i]
	}
	t.Run("replies_in_order", func(t *testing.T) {
		for _, w := range want {
			th := byID[strconv.FormatInt(w.Comments[0].ID, 10)]
			if th == nil {
				t.Errorf("thread %d missing", w.Comments[0].ID)
				continue
			}
			if th.Path != w.Path || (w.Kind == provider.ThreadInline && th.Line != w.Line) {
				t.Errorf("thread %s: anchor %s:%d, want %s:%d", th.ID, th.Path, th.Line, w.Path, w.Line)
			}
			var g, e []string
			for _, c := range th.Comments {
				g = append(g, c.ID+"/"+c.Author+"/"+c.Body)
			}
			for _, c := range w.Comments {
				e = append(e, strconv.FormatInt(c.ID, 10)+"/"+c.Author.Login+"/"+c.Body)
			}
			if !slices.Equal(g, e) {
				t.Errorf("thread %s comments = %q, want %q", th.ID, g, e)
			}
		}
	})
	t.Run("resolved_hidden_only_where_reported", func(t *testing.T) {
		for _, w := range want {
			th := byID[strconv.FormatInt(w.Comments[0].ID, 10)]
			if th == nil {
				continue // reported above
			}
			if !slices.Contains(s.tr.ResolvableThreads, w.Kind) {
				if th.Resolved != nil {
					t.Errorf("%s thread %s: Resolved = %v, want nil (the provider reports no resolution here)", w.Kind, th.ID, *th.Resolved)
				}
				continue
			}
			if th.Resolved == nil || *th.Resolved != w.Resolved {
				t.Errorf("%s thread %s: Resolved = %s, want %v (hidden %v)", w.Kind, th.ID, fmtBoolPtr(th.Resolved), w.Resolved, w.Resolved)
			}
		}
	})
}

func fmtBoolPtr(b *bool) string {
	if b == nil {
		return "nil"
	}
	return strconv.FormatBool(*b)
}

// reply: a reply's in_thread matches what ListThreads says about the
// thread, and exactly one write request is sent.
func (s *suite) reply(t *testing.T) {
	spec := samplePR()
	p, ref, log := s.serve(t, spec)
	ts, err := p.ListThreads(t.Context(), ref)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	for _, id := range []int64{idGeneralOther, idInlineRoot} {
		sid := strconv.FormatInt(id, 10)
		var th *provider.Thread
		for i := range ts {
			if ts[i].ID == sid {
				th = &ts[i]
			}
		}
		if th == nil {
			t.Fatalf("thread %s missing", sid)
		}
		t.Run(string(th.Kind), func(t *testing.T) {
			_, w0 := log.count()
			rr, err := p.ReplyToComment(t.Context(), ref, sid, "Thanks, noted.")
			if err != nil {
				t.Fatalf("ReplyToComment(%s): %v", sid, err)
			}
			if rr.InThread != th.ReplyInThread {
				t.Errorf("in_thread = %v, but ListThreads says reply_in_thread = %v", rr.InThread, th.ReplyInThread)
			}
			if rr.Comment.ID == "" {
				t.Error("the reply has no id")
			}
			if _, w1 := log.count(); w1-w0 != 1 {
				t.Errorf("%d write requests, want 1", w1-w0)
			}
		})
	}
}

// editOwnership: an edit of another user's comment is refused with
// not_owner before any write request; the token user's own comment is
// edited.
func (s *suite) editOwnership(t *testing.T) {
	t.Run("refuses_other_author_before_any_write", func(t *testing.T) {
		p, ref, log := s.serve(t, samplePR())
		err := p.EditComment(t.Context(), ref, strconv.Itoa(idGeneralOther), "Edited by the bot.")
		if !errors.Is(err, provider.ErrNotOwner) {
			t.Errorf("EditComment = %v, want not_owner", err)
		}
		checkClean(t, "EditComment", err)
		if _, w := log.count(); w != 0 {
			t.Errorf("%d write requests before the refusal, want 0", w)
		}
	})
	t.Run("edits_own_comment", func(t *testing.T) {
		p, ref, log := s.serve(t, samplePR())
		if err := p.EditComment(t.Context(), ref, strconv.Itoa(idGeneralOwn), "Updated summary."); err != nil {
			t.Fatalf("EditComment: %v", err)
		}
		if _, w := log.count(); w != 1 {
			t.Errorf("%d write requests, want 1", w)
		}
	})
}

// inline: comments on an added and a context line are posted; a comment on
// a line outside the hunks is not, with a fixed sentence.
func (s *suite) inline(t *testing.T) {
	spec := samplePR()
	p, ref, _ := s.serve(t, spec)
	pr := mustPR(t, p, ref)
	var f *File
	for i := range spec.Files {
		if spec.Files[i].Path == pathModified {
			f = &spec.Files[i]
		}
	}
	lines := f.NewLines()
	pick := func(lt provider.LineType) int {
		best := 0
		for n, typ := range lines {
			if typ == lt && (best == 0 || n < best) {
				best = n
			}
		}
		return best
	}
	outside := 1
	for lines[outside] != "" {
		outside++
	}
	items := []provider.InlineComment{
		{Path: f.Path, Line: pick(provider.LineAdded), LineType: provider.LineAdded, Body: "On the added line."},
		{Path: f.Path, Line: pick(provider.LineContext), LineType: provider.LineContext, Body: "On a context line."},
		{Path: f.Path, Line: outside, LineType: provider.LineContext, Body: "Outside the hunks."},
	}
	res, err := p.PostInlineComments(t.Context(), ref, pr, items)
	if err != nil {
		t.Fatalf("PostInlineComments: %v", err)
	}
	if len(res) != len(items) {
		t.Fatalf("%d results, want %d", len(res), len(items))
	}
	for i, name := range []string{"added_line_posted", "context_line_posted"} {
		t.Run(name, func(t *testing.T) {
			if !res[i].Posted || res[i].ID == "" || res[i].Error != "" {
				t.Errorf("line %d: %+v, want posted with an id", items[i].Line, res[i])
			}
		})
	}
	t.Run("out_of_hunk_line_unanchorable", func(t *testing.T) {
		r := res[2]
		if r.Posted || r.Error == "" || r.ID != "" || r.URL != "" {
			t.Errorf("line %d: %+v, want not posted, with an error sentence and no id or URL", items[2].Line, r)
		}
		checkCleanText(t, "InlineResult.Error", r.Error)
	})
}

// reviewStatus: verdicts fold to approved, changes requested and stale;
// dismissed verdicts and review-mcp's own review are not listed.
func (s *suite) reviewStatus(t *testing.T) {
	spec := samplePR()
	p, ref, _ := s.serve(t, spec)
	pr := mustPR(t, p, ref)
	me := provider.User{ID: strconv.FormatInt(spec.TokenUser.ID, 10), Name: spec.TokenUser.Login}
	st := p.GetReviewStatus(t.Context(), ref, pr, provider.ReviewStatusOptions{
		Me:    &me,
		IsOwn: func(body string) bool { return strings.Contains(body, OwnMarker) },
	})
	if st == nil || st.Reviewers == nil {
		t.Fatalf("no reviewers read: %+v", st)
	}
	for _, n := range st.Notes {
		if n == provider.NoteReviewsUnreadable || n == provider.NoteActivityUnclassified {
			t.Errorf("unexpected note %q", n)
		}
	}
	find := func(u User) *provider.Reviewer {
		for i := range st.Reviewers {
			r := &st.Reviewers[i]
			if provider.IsUser(provider.User{ID: strconv.FormatInt(u.ID, 10), Name: u.Login}, r.User.ID, r.User.Name) {
				return r
			}
		}
		return nil
	}
	for _, w := range spec.Reviewers {
		t.Run(reviewerCase(w), func(t *testing.T) {
			got := find(w.User)
			if w.Dismissed || w.Own {
				if got != nil {
					t.Errorf("%s is listed (%+v), want left out", w.User.Login, *got)
				}
				return
			}
			switch {
			case got == nil:
				t.Errorf("%s is not listed", w.User.Login)
			case got.State != w.State || got.Stale != w.Stale:
				t.Errorf("%s: state %q stale %v, want %q stale %v", w.User.Login, got.State, got.Stale, w.State, w.Stale)
			case w.State == provider.ReviewPending && !got.Requested:
				t.Errorf("%s: pending but not requested", w.User.Login)
			}
		})
	}
	t.Run("no_other_reviewers", func(t *testing.T) {
		known := map[string]bool{}
		for _, w := range spec.Reviewers {
			known[strings.ToLower(w.User.Login)] = !w.Dismissed && !w.Own
		}
		commenters := map[string]bool{}
		for _, th := range spec.Threads {
			for _, c := range th.Comments {
				commenters[strings.ToLower(c.Author.Login)] = true
			}
		}
		for _, r := range st.Reviewers {
			name := strings.ToLower(r.User.Name)
			listed, inSpec := known[name]
			switch {
			case inSpec && listed:
				continue
			case !inSpec && commenters[name] && r.State == provider.ReviewCommented:
				// A host may count an inline comment as a review.
				continue
			}
			t.Errorf("unexpected reviewer %s (%q)", r.User.Name, r.State)
		}
	})
}

func reviewerCase(r Reviewer) string {
	switch {
	case r.Own:
		return "own_review_excluded"
	case r.Dismissed:
		return "dismissed"
	case r.Stale:
		return "stale"
	}
	return string(r.State)
}

// fileLineURL: the URL is absolute, names the file's (new) path and the
// line, and is built without any request.
func (s *suite) fileLineURL(t *testing.T) {
	spec := samplePR()
	p, ref, log := s.serve(t, spec)
	pr := mustPR(t, p, ref)
	n0, _ := log.count()
	for _, c := range []struct {
		name, path, old string
		line            int
	}{
		{"modified", pathModified, "", 6},
		{"renamed", pathRenamed, pathRenamedOl, 3},
	} {
		t.Run(c.name, func(t *testing.T) {
			u := p.FileLineURL(ref, pr, c.path, c.line)
			pu, err := url.Parse(u)
			if err != nil || (pu.Scheme != "http" && pu.Scheme != "https") || pu.Host == "" {
				t.Fatalf("FileLineURL = %q, want an absolute http(s) URL", u)
			}
			if !strings.Contains(u, c.path) {
				t.Errorf("FileLineURL = %q, want it to name %s", u, c.path)
			}
			if c.old != "" && strings.Contains(u, c.old) {
				t.Errorf("FileLineURL = %q names the old path %s", u, c.old)
			}
			if other := p.FileLineURL(ref, pr, c.path, c.line+1); other == u {
				t.Errorf("FileLineURL is %q for lines %d and %d, want the line in the URL", u, c.line, c.line+1)
			}
		})
	}
	if n1, _ := log.count(); n1 != n0 {
		t.Errorf("FileLineURL sent %d requests, want none", n1-n0)
	}
}

// errorCases: 401, 403, 404, 500 and a network failure map to the error
// classes, and no error text carries the token or response body text.
func (s *suite) errorCases(t *testing.T) {
	for _, c := range []struct {
		name  string
		fail  Failure
		class *provider.Error
	}{
		{"status_401", Failure{Status: 401}, provider.ErrAuth},
		{"status_403", Failure{Status: 403}, provider.ErrAuth},
		{"status_404", Failure{Status: 404}, provider.ErrNotFound},
		{"status_500", Failure{Status: 500}, provider.ErrUpstream},
		{"network", Failure{Network: true}, provider.ErrTransport},
	} {
		t.Run(c.name, func(t *testing.T) {
			spec := samplePR()
			spec.Env.Failure = c.fail
			p, ref, _ := s.serve(t, spec)
			s.checkFailures(t, p, ref, spec, c.class)
		})
	}
}

func (s *suite) checkFailures(t *testing.T, p provider.Provider, ref provider.PRRef, spec Spec, class *provider.Error) {
	ctx := t.Context()
	// A pull request as GetPullRequest would have returned it, for the
	// calls that need one.
	pr := &provider.PullRequest{
		Title: spec.Title, Author: spec.Author.Login, State: "open",
		SourceBranch: spec.SourceBranch, TargetBranch: spec.TargetBranch,
		HeadSHA: spec.HeadSHA, BaseSHA: spec.BaseSHA, BaseStrategy: s.tr.BaseStrategies[0],
	}
	calls := []struct {
		name string
		call func() error
	}{
		{"GetPullRequest", func() error { _, err := p.GetPullRequest(ctx, ref); return err }},
		{"GetCommitMessages", func() error { _, err := p.GetCommitMessages(ctx, ref); return err }},
		{"GetDiff", func() error { _, err := p.GetDiff(ctx, ref, pr, provider.DiffOptions{}); return err }},
		{"ListThreads", func() error { _, err := p.ListThreads(ctx, ref); return err }},
		{"CurrentUser", func() error { _, err := p.CurrentUser(ctx); return err }},
		{"PostComment", func() error { _, err := p.PostComment(ctx, ref, "A comment."); return err }},
		{"ReplyToComment", func() error {
			_, err := p.ReplyToComment(ctx, ref, strconv.Itoa(idGeneralOther), "A reply.")
			return err
		}},
		{"EditComment", func() error { return p.EditComment(ctx, ref, strconv.Itoa(idGeneralOwn), "An edit.") }},
	}
	for _, c := range calls {
		err := c.call()
		switch {
		case err == nil:
			t.Errorf("%s: no error, want %s", c.name, class.Class)
		case !errors.Is(err, class):
			t.Errorf("%s: error %q, want class %s", c.name, err, class.Class)
		}
		checkClean(t, c.name, err)
	}

	items := []provider.InlineComment{{Path: pathModified, Line: 6, LineType: provider.LineAdded, Body: "Inline."}}
	res, err := p.PostInlineComments(ctx, ref, pr, items)
	switch {
	case err != nil:
		if !errors.Is(err, class) {
			t.Errorf("PostInlineComments: error %q, want class %s", err, class.Class)
		}
		checkClean(t, "PostInlineComments", err)
	case len(res) != len(items):
		t.Errorf("PostInlineComments: %d results, want %d", len(res), len(items))
	default:
		// A provider that reports per item: the item carries the class's
		// fixed sentence.
		for _, r := range res {
			if r.Posted || !strings.HasPrefix(r.Error, class.Error()) {
				t.Errorf("PostInlineComments: %+v, want not posted with %q", r, class.Error())
			}
			checkCleanText(t, "InlineResult.Error", r.Error)
		}
	}

	st := p.GetReviewStatus(ctx, ref, pr, provider.ReviewStatusOptions{})
	if st == nil || st.Reviewers != nil || !slices.Contains(st.Notes, provider.NoteReviewsUnreadable) {
		t.Errorf("GetReviewStatus = %+v, want no reviewers and the note %q", st, provider.NoteReviewsUnreadable)
	}
}

// checkClean fails t when any text of err or of an error it wraps carries
// the response body sentinel or the token. It reports the first such text
// only.
func checkClean(t *testing.T, what string, err error) {
	t.Helper()
	for _, s := range errorTexts(err) {
		if !checkCleanText(t, what, s) {
			return
		}
	}
}

// checkCleanText fails t and returns false when s carries the response body
// sentinel or the token.
func checkCleanText(t *testing.T, what, s string) bool {
	t.Helper()
	ok := true
	if strings.Contains(s, Sentinel) {
		t.Errorf("%s: error text carries response body text (sentinel): %q", what, s)
		ok = false
	}
	if strings.Contains(s, testToken) {
		t.Errorf("%s: error text carries the token", what)
		ok = false
	}
	return ok
}

// errorTexts returns the texts of err and of every error it wraps, in the
// %v, %+v and %#v forms.
func errorTexts(err error) []string {
	var out []string
	var walk func(error)
	walk = func(e error) {
		if e == nil {
			return
		}
		out = append(out, e.Error(), fmt.Sprintf("%+v", e), fmt.Sprintf("%#v", e))
		if m, ok := e.(interface{ Unwrap() []error }); ok { //nolint:errorlint // walks the tree itself
			for _, x := range m.Unwrap() {
				walk(x)
			}
			return
		}
		walk(errors.Unwrap(e))
	}
	walk(err)
	return out
}
