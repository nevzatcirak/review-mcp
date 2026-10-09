package contract

import (
	"errors"
	"fmt"
	"net/url"
	"reflect"
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

// resolves reports whether caps says the provider reports resolution for
// threads of kind k.
func resolves(caps provider.Capabilities, k provider.ThreadKind) bool {
	if k == provider.ThreadInline {
		return caps.InlineThreadResolution
	}
	return caps.GeneralThreadResolution
}

// capabilityName is the Capabilities field that covers threads of kind k.
func capabilityName(k provider.ThreadKind) string {
	if k == provider.ThreadInline {
		return "InlineThreadResolution"
	}
	return "GeneralThreadResolution"
}

// capabilities checks that the resolution flags agree with what the
// provider reports for the sample pull request's resolved general thread and
// resolved inline thread: a flag is true exactly when its thread comes back
// with a Resolved state. The fake servers report a resolution only where the
// host stores one.
func (s *suite) capabilities(t *testing.T) {
	spec := samplePR()
	p, ref, _ := s.serve(t, spec)
	caps := p.Capabilities()
	if caps.SuggestionBlocks != (caps.SuggestionStyle != provider.SuggestionStyleNone) {
		t.Errorf("Capabilities().SuggestionBlocks is %v but SuggestionStyle is %q: a provider with native suggestion "+
			"blocks names their style, and only such a provider does", caps.SuggestionBlocks, caps.SuggestionStyle)
	}
	switch caps.SuggestionStyle {
	case provider.SuggestionStyleNone, provider.SuggestionStyleRange, provider.SuggestionStyleOffset:
	default:
		t.Errorf("Capabilities().SuggestionStyle %q is not a known style", caps.SuggestionStyle)
	}
	got, err := p.ListThreads(t.Context(), ref)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	byID := map[string]*provider.Thread{}
	for i := range got {
		byID[got[i].ID] = &got[i]
	}
	for _, w := range spec.Threads {
		if !w.Resolved {
			continue
		}
		id := strconv.FormatInt(w.Comments[0].ID, 10)
		th := byID[id]
		if th == nil {
			t.Errorf("%s thread %s is missing", w.Kind, id)
			continue
		}
		flag := resolves(caps, w.Kind)
		name := capabilityName(w.Kind)
		switch {
		case flag && th.Resolved == nil:
			t.Errorf("Capabilities().%s is true but the resolved %s thread %s comes back with Resolved nil", name, w.Kind, id)
		case !flag && th.Resolved != nil:
			t.Errorf("Capabilities().%s is false but the resolved %s thread %s comes back with Resolved = %v", name, w.Kind, id, *th.Resolved)
		}
	}
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
		caps := p.Capabilities()
		for _, w := range want {
			th := byID[strconv.FormatInt(w.Comments[0].ID, 10)]
			if th == nil {
				continue // reported above
			}
			if !resolves(caps, w.Kind) {
				if th.Resolved != nil {
					t.Errorf("%s thread %s: Resolved = %v, want nil (Capabilities().%s is false)", w.Kind, th.ID, *th.Resolved, capabilityName(w.Kind))
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

// generalReply: a reply to a general comment is either the last comment of
// the same thread (in_thread) or a new general thread that quotes or names
// the original comment; the original thread is unchanged either way.
func (s *suite) generalReply(t *testing.T) {
	spec := samplePR()
	p, ref, _ := s.serve(t, spec)
	const reply = "Thanks, I will take a look."
	sid := strconv.FormatInt(idGeneralOther, 10)
	before, err := p.ListThreads(t.Context(), ref)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	orig := findThread(before, sid)
	if orig == nil {
		t.Fatalf("thread %s missing", sid)
	}
	rr, err := p.ReplyToComment(t.Context(), ref, sid, reply)
	if err != nil {
		t.Fatalf("ReplyToComment(%s): %v", sid, err)
	}
	if rr.InThread != orig.ReplyInThread {
		t.Errorf("in_thread = %v, but ListThreads says reply_in_thread = %v", rr.InThread, orig.ReplyInThread)
	}
	after, err := p.ListThreads(t.Context(), ref)
	if err != nil {
		t.Fatalf("ListThreads after the reply: %v", err)
	}
	cur := findThread(after, sid)
	if cur == nil {
		t.Fatalf("thread %s missing after the reply", sid)
	}
	commentKey := func(c provider.CommentItem) string { return c.ID + "/" + c.Author + "/" + c.Body }
	if rr.InThread {
		if len(after) != len(before) {
			t.Errorf("%d threads after the reply, want %d (the reply joins its thread)", len(after), len(before))
		}
		if len(cur.Comments) != len(orig.Comments)+1 {
			t.Fatalf("thread %s has %d comments, want %d", sid, len(cur.Comments), len(orig.Comments)+1)
		}
		last := cur.Comments[len(cur.Comments)-1]
		if last.Author != spec.TokenUser.Login || last.Body != reply {
			t.Errorf("last comment = %s/%s/%q, want the token user's reply %q", last.ID, last.Author, last.Body, reply)
		}
		if last.ID != rr.Comment.ID {
			t.Errorf("last comment id = %s, want the reply's %s", last.ID, rr.Comment.ID)
		}
		return
	}
	// A reply that does not join the thread: the original thread is the
	// same, and one new general thread holds the reply.
	var g, w []string
	for _, c := range cur.Comments {
		g = append(g, commentKey(c))
	}
	for _, c := range orig.Comments {
		w = append(w, commentKey(c))
	}
	if !slices.Equal(g, w) {
		t.Errorf("thread %s changed: comments = %q, want %q", sid, g, w)
	}
	if len(after) != len(before)+1 {
		t.Fatalf("%d threads after the reply, want %d (one new thread)", len(after), len(before)+1)
	}
	known := map[string]bool{}
	for i := range before {
		known[before[i].ID] = true
	}
	var fresh []*provider.Thread
	for i := range after {
		if !known[after[i].ID] {
			fresh = append(fresh, &after[i])
		}
	}
	if len(fresh) != 1 {
		t.Fatalf("%d new threads, want 1", len(fresh))
	}
	nt := fresh[0]
	if nt.Kind != provider.ThreadGeneral || len(nt.Comments) == 0 {
		t.Fatalf("new thread %s: kind %q with %d comments, want a general thread with a root", nt.ID, nt.Kind, len(nt.Comments))
	}
	root := nt.Comments[0]
	if root.Author != spec.TokenUser.Login {
		t.Errorf("new thread root author = %q, want the token user %q", root.Author, spec.TokenUser.Login)
	}
	if !strings.Contains(root.Body, reply) {
		t.Errorf("new thread root = %q, want it to contain the reply %q", root.Body, reply)
	}
	// The provider names the original comment's author in a quote header.
	if !strings.Contains(root.Body, orig.Comments[0].Author) {
		t.Errorf("new thread root = %q, want it to name the original author %q", root.Body, orig.Comments[0].Author)
	}
}

func findThread(ts []provider.Thread, id string) *provider.Thread {
	for i := range ts {
		if ts[i].ID == id {
			return &ts[i]
		}
	}
	return nil
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

// prSnapshot is what an update of the title or description must not
// disturb besides the field it names: the other field, the draft flag and
// the reviewers with their verdicts.
type prSnapshot struct {
	title, description string
	draft              *bool
	version            string
	reviewers          []provider.Reviewer
}

func (s *suite) snapshot(t *testing.T, p provider.Provider, ref provider.PRRef, spec Spec) prSnapshot {
	t.Helper()
	pr := mustPR(t, p, ref)
	me := provider.User{ID: strconv.FormatInt(spec.TokenUser.ID, 10), Name: spec.TokenUser.Login}
	st := p.GetReviewStatus(t.Context(), ref, pr, provider.ReviewStatusOptions{
		Me:    &me,
		IsOwn: func(body string) bool { return strings.Contains(body, OwnMarker) },
	})
	if st == nil || st.Reviewers == nil {
		t.Fatalf("no reviewers read: %+v", st)
	}
	return prSnapshot{title: pr.Title, description: pr.Description, draft: pr.Draft, version: pr.Version, reviewers: st.Reviewers}
}

// sameUntouched fails t when the draft flag or the reviewers differ between
// two snapshots: an update never drops a reviewer, a verdict or the draft
// flag.
func sameUntouched(t *testing.T, before, after prSnapshot) {
	t.Helper()
	if !reflect.DeepEqual(before.reviewers, after.reviewers) {
		t.Errorf("the reviewers changed:\nbefore %+v\nafter  %+v", before.reviewers, after.reviewers)
	}
	if (before.draft == nil) != (after.draft == nil) || (before.draft != nil && *before.draft != *after.draft) {
		t.Errorf("the draft flag changed: %s -> %s", fmtBoolPtr(before.draft), fmtBoolPtr(after.draft))
	}
}

func strPtr(s string) *string { return &s }

// updatePullRequest: the title and the description can be updated
// separately or together and nothing else changes (not the other field, not
// the draft flag, not the reviewers and their verdicts); input is validated
// before any request; a stale version is a conflict that changes nothing,
// on a host that has versions; errors map to their classes (see
// errorCases). Whether the host has versions is read from the pull request
// (PullRequest.Version), not from its kind.
func (s *suite) updatePullRequest(t *testing.T) {
	spec := samplePR()
	spec.Draft = true
	// A description with CRLF, trailing spaces, an emoji and a code fence
	// must come back byte for byte.
	newDesc := "First line  \r\n\r\n```go\r\nfunc main() {}\r\n```\r\n\r\nEmoji \U0001F680 and <b>markup</b> & more\r\n"
	serve := func(t *testing.T) (provider.Provider, provider.PRRef, *requestLog) {
		p, ref, log := s.serve(t, spec)
		if !p.Capabilities().DescriptionEdit {
			t.Skip("the provider cannot edit the pull request description")
		}
		return p, ref, log
	}

	t.Run("title_only", func(t *testing.T) {
		p, ref, log := serve(t)
		before := s.snapshot(t, p, ref, spec)
		_, w0 := log.count()
		if err := p.UpdatePullRequest(t.Context(), ref, provider.UpdatePR{Title: strPtr("A new title \U0001F680"), Version: before.version}); err != nil {
			t.Fatalf("UpdatePullRequest: %v", err)
		}
		if _, w := log.count(); w-w0 != 1 {
			t.Errorf("%d write requests, want 1", w-w0)
		}
		after := s.snapshot(t, p, ref, spec)
		if after.title != "A new title \U0001F680" || after.description != before.description {
			t.Errorf("title %q description %q, want the new title and the old description %q", after.title, after.description, before.description)
		}
		sameUntouched(t, before, after)
	})
	t.Run("description_only", func(t *testing.T) {
		p, ref, _ := serve(t)
		before := s.snapshot(t, p, ref, spec)
		if err := p.UpdatePullRequest(t.Context(), ref, provider.UpdatePR{Description: &newDesc, Version: before.version}); err != nil {
			t.Fatalf("UpdatePullRequest: %v", err)
		}
		after := s.snapshot(t, p, ref, spec)
		if after.description != newDesc || after.title != before.title {
			t.Errorf("title %q description %q, want the old title %q and the new description %q", after.title, after.description, before.title, newDesc)
		}
		sameUntouched(t, before, after)
	})
	t.Run("title_and_description", func(t *testing.T) {
		p, ref, _ := serve(t)
		before := s.snapshot(t, p, ref, spec)
		if err := p.UpdatePullRequest(t.Context(), ref, provider.UpdatePR{Title: strPtr("Both"), Description: strPtr(""), Version: before.version}); err != nil {
			t.Fatalf("UpdatePullRequest: %v", err)
		}
		after := s.snapshot(t, p, ref, spec)
		if after.title != "Both" || after.description != "" {
			t.Errorf("title %q description %q, want %q and an empty description", after.title, after.description, "Both")
		}
		sameUntouched(t, before, after)
	})
	t.Run("two_updates_in_a_row", func(t *testing.T) {
		p, ref, _ := serve(t)
		first := mustPR(t, p, ref)
		if err := p.UpdatePullRequest(t.Context(), ref, provider.UpdatePR{Title: strPtr("One"), Version: first.Version}); err != nil {
			t.Fatalf("first update: %v", err)
		}
		second := mustPR(t, p, ref)
		if err := p.UpdatePullRequest(t.Context(), ref, provider.UpdatePR{Description: strPtr("Two"), Version: second.Version}); err != nil {
			t.Fatalf("second update on the fresh read: %v", err)
		}
		if got := mustPR(t, p, ref); got.Title != "One" || got.Description != "Two" {
			t.Errorf("title %q description %q", got.Title, got.Description)
		}
	})
	t.Run("validated_before_any_request", func(t *testing.T) {
		p, ref, log := serve(t)
		version := mustPR(t, p, ref).Version
		n0, w0 := log.count()
		bad := []struct {
			name string
			up   provider.UpdatePR
		}{
			{"nothing_to_update", provider.UpdatePR{Version: version}},
			{"empty_title", provider.UpdatePR{Title: strPtr(""), Version: version}},
			{"blank_title", provider.UpdatePR{Title: strPtr(" \t "), Version: version}},
			{"multiline_title", provider.UpdatePR{Title: strPtr("two\nlines"), Version: version}},
			{"nul_in_description", provider.UpdatePR{Description: strPtr("a\x00b"), Version: version}},
			{"malformed_version", provider.UpdatePR{Title: strPtr("T"), Version: "v1"}},
		}
		if version != "" {
			// A host with versions refuses an update that names none.
			bad = append(bad, struct {
				name string
				up   provider.UpdatePR
			}{"missing_version", provider.UpdatePR{Title: strPtr("T")}})
		}
		for _, c := range bad {
			err := p.UpdatePullRequest(t.Context(), ref, c.up)
			if !errors.Is(err, provider.ErrProtocol) {
				t.Errorf("%s: error %v, want protocol", c.name, err)
			}
			checkClean(t, c.name, err)
		}
		if n, w := log.count(); n != n0 || w != w0 {
			t.Errorf("%d requests (%d writes) were sent for invalid input, want none", n-n0, w-w0)
		}
	})
	t.Run("stale_version_is_a_conflict", func(t *testing.T) {
		p, ref, _ := serve(t)
		stale := mustPR(t, p, ref)
		if stale.Version == "" {
			t.Skip("the provider has no pull request version")
		}
		if err := p.UpdatePullRequest(t.Context(), ref, provider.UpdatePR{Title: strPtr("Winner"), Version: stale.Version}); err != nil {
			t.Fatalf("first update: %v", err)
		}
		before := s.snapshot(t, p, ref, spec)
		err := p.UpdatePullRequest(t.Context(), ref, provider.UpdatePR{Title: strPtr("Loser"), Description: strPtr("Lost"), Version: stale.Version})
		if !errors.Is(err, provider.ErrConflict) {
			t.Fatalf("stale update: error %v, want conflict", err)
		}
		checkClean(t, "stale update", err)
		after := s.snapshot(t, p, ref, spec)
		if !reflect.DeepEqual(before, after) || after.title != "Winner" {
			t.Errorf("a refused update changed the pull request:\nbefore %+v\nafter  %+v", before, after)
		}
	})
}

// inline: comments on an added and a context line are posted; a comment on
// a line outside the hunks is not, with a fixed sentence. A comment on a
// range of new-side lines of one hunk (InlineComment.EndLine) is posted
// too, and listed at its last line by a provider with Traits.InlineRanges,
// at its first line by any other.
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
	// The range runs from the line before the added line to the line after
	// it: three new-side lines of the one hunk.
	added := pick(provider.LineAdded)
	if lines[added-1] == "" || lines[added+1] == "" {
		t.Fatalf("the sample hunk has no new-side lines around line %d", added)
	}
	items := []provider.InlineComment{
		{Path: f.Path, Line: added, LineType: provider.LineAdded, Body: "On the added line."},
		{Path: f.Path, Line: pick(provider.LineContext), LineType: provider.LineContext, Body: "On a context line."},
		{Path: f.Path, Line: outside, LineType: provider.LineContext, Body: "Outside the hunks."},
		{Path: f.Path, Line: added - 1, EndLine: added + 1, LineType: lines[added-1], Body: "On a range of lines."},
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
			if !res[i].Posted || res[i].ID == "" || res[i].Error != "" || res[i].Reason != provider.InlineReasonPosted {
				t.Errorf("line %d: %+v, want posted with an id and the reason %q", items[i].Line, res[i], provider.InlineReasonPosted)
			}
		})
	}
	t.Run("out_of_hunk_line_unanchorable", func(t *testing.T) {
		r := res[2]
		if r.Posted || r.Error == "" || r.ID != "" || r.URL != "" {
			t.Errorf("line %d: %+v, want not posted, with an error sentence and no id or URL", items[2].Line, r)
		}
		if r.Reason != provider.InlineReasonUnanchorable {
			t.Errorf("line %d: reason %q, want %q", items[2].Line, r.Reason, provider.InlineReasonUnanchorable)
		}
		checkCleanText(t, "InlineResult.Error", r.Error)
	})
	t.Run("range_posted", func(t *testing.T) {
		it, r := items[3], res[3]
		if !r.Posted || r.ID == "" || r.Error != "" || r.Reason != provider.InlineReasonPosted {
			t.Fatalf("lines %d-%d: %+v, want posted with an id and the reason %q", it.Line, it.EndLine, r, provider.InlineReasonPosted)
		}
		threads, err := p.ListThreads(t.Context(), ref)
		if err != nil {
			t.Fatalf("ListThreads: %v", err)
		}
		want := it.Line
		if s.tr.InlineRanges {
			want = it.EndLine
		}
		var at []int
		for _, th := range threads {
			if th.Kind == provider.ThreadInline && th.Path == it.Path && len(th.Comments) > 0 && th.Comments[0].Body == it.Body {
				at = append(at, th.Line)
			}
		}
		if len(at) != 1 || at[0] != want {
			t.Errorf("the range comment is listed at lines %v, want once at line %d (Traits.InlineRanges %v)", at, want, s.tr.InlineRanges)
		}
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

// failureCallNames are the subtests of each errors case, one per Provider
// call, in order.
var failureCallNames = []string{
	"GetPullRequest", "GetCommitMessages", "GetDiff", "ListThreads", "CurrentUser", "PostComment",
	"ReplyToComment", "EditComment", "UpdatePullRequest", "PostInlineComments", "GetReviewStatus",
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
		{"UpdatePullRequest", func() error {
			return p.UpdatePullRequest(ctx, ref, provider.UpdatePR{Title: strPtr("A title."), Version: "0"})
		}},
	}
	names := []string{}
	for _, c := range calls {
		names = append(names, c.name)
	}
	if names = append(names, "PostInlineComments", "GetReviewStatus"); !slices.Equal(names, failureCallNames) {
		t.Fatalf("failureCallNames %q is out of step with the calls %q", failureCallNames, names)
	}
	for _, c := range calls {
		t.Run(c.name, func(t *testing.T) {
			s.skipPending(t, "errors/"+c.name)
			err := c.call()
			switch {
			case err == nil:
				t.Errorf("%s: no error, want %s", c.name, class.Class)
			case !errors.Is(err, class):
				t.Errorf("%s: error %q, want class %s", c.name, err, class.Class)
			}
			checkClean(t, c.name, err)
		})
	}

	t.Run("PostInlineComments", func(t *testing.T) {
		s.skipPending(t, "errors/PostInlineComments")
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
				if r.Reason != provider.InlineReasonFailed {
					t.Errorf("PostInlineComments: reason %q, want %q", r.Reason, provider.InlineReasonFailed)
				}
				checkCleanText(t, "InlineResult.Error", r.Error)
			}
		}
	})

	t.Run("GetReviewStatus", func(t *testing.T) {
		s.skipPending(t, "errors/GetReviewStatus")
		st := p.GetReviewStatus(ctx, ref, pr, provider.ReviewStatusOptions{})
		if st == nil || st.Reviewers != nil || !slices.Contains(st.Notes, provider.NoteReviewsUnreadable) {
			t.Errorf("GetReviewStatus = %+v, want no reviewers and the note %q", st, provider.NoteReviewsUnreadable)
		}
	})
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
