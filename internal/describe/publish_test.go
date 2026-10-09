package describe

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// pubProvider serves the fakeProvider's PR with mutable title, description
// and version, comments, and a write log. Hooks let a test change the PR at
// a chosen moment: beforeGet runs at the start of the n-th GetPullRequest
// (the pipeline's own read is the first), beforeUpdate at the start of the
// n-th UpdatePullRequest.
type pubProvider struct {
	*fakeProvider

	caps      provider.Capabilities
	title     string
	desc      string
	versioned bool
	version   int

	gets, updates int
	beforeGet     func(n int)
	beforeUpdate  func(n int)
	updateErr     error

	// writes counts every request that changes the PR or its comments.
	writes   int
	titles   []string // the titles written, in order
	descs    []string // the descriptions written, in order
	comments []*pubComment
	nextID   int
	failEdit bool
	failList bool
	failPost bool
	me       provider.User

	// reviewers is what GetReviewStatus reports; statusCalls counts its
	// calls; statusFail makes the n-th call report unreadable reviewers.
	reviewers   []provider.Reviewer
	statusCalls int
	statusFail  int
}

type pubComment struct {
	id, body, login, userID string
	at                      time.Time
}

func newPubProvider(h *harness, caps provider.Capabilities) *pubProvider {
	p := &pubProvider{
		fakeProvider: h.prov, caps: caps, title: h.prov.pr.Title, desc: h.prov.pr.Description,
		nextID: 100, me: provider.User{ID: "42", Name: "review-bot"},
	}
	h.deps.Resolver = pubResolver{p}
	return p
}

type pubResolver struct{ p *pubProvider }

func (r pubResolver) Resolve(u string) (provider.PRRef, provider.Provider, error) {
	return provider.PRRef{Kind: provider.KindGitea, Namespace: "octo", Repo: "demo", Number: 7, URL: u}, r.p, nil
}

var (
	giteaCaps = provider.Capabilities{GFM: true, MarkdownTables: true, DescriptionEdit: true}
	bbsCaps   = provider.Capabilities{MarkdownTables: true, DescriptionEdit: true, QuickActions: false}
)

func (p *pubProvider) Capabilities() provider.Capabilities { return p.caps }

func (p *pubProvider) GetPullRequest(context.Context, provider.PRRef) (*provider.PullRequest, error) {
	p.gets++
	if p.beforeGet != nil {
		p.beforeGet(p.gets)
	}
	pr := p.pr
	pr.Title, pr.Description, pr.WebURL = p.title, p.desc, "https://your-gitea.example/octo/demo/pulls/7"
	if p.versioned {
		pr.Version = strconv.Itoa(p.version)
	}
	return &pr, nil
}

func (p *pubProvider) GetReviewStatus(context.Context, provider.PRRef, *provider.PullRequest, provider.ReviewStatusOptions) *provider.ReviewStatus {
	p.statusCalls++
	if p.statusCalls == p.statusFail {
		return &provider.ReviewStatus{Notes: []string{provider.NoteReviewsUnreadable}}
	}
	return &provider.ReviewStatus{Reviewers: append([]provider.Reviewer(nil), p.reviewers...)}
}

// edit simulates someone else changing the description: the version moves.
func (p *pubProvider) edit(desc string) {
	p.desc = desc
	p.version++
}

func (p *pubProvider) UpdatePullRequest(_ context.Context, _ provider.PRRef, up provider.UpdatePR) error {
	p.updates++
	if p.beforeUpdate != nil {
		p.beforeUpdate(p.updates)
	}
	if p.updateErr != nil {
		return p.updateErr
	}
	if err := provider.ValidateUpdatePR(up); err != nil {
		return err
	}
	if p.versioned && up.Version != strconv.Itoa(p.version) {
		return &provider.Error{Class: provider.ClassConflict, Status: 409}
	}
	p.writes++
	if up.Title != nil {
		p.title = *up.Title
		p.titles = append(p.titles, *up.Title)
	}
	if up.Description != nil {
		p.desc = *up.Description
		p.descs = append(p.descs, *up.Description)
	}
	p.version++
	return nil
}

func (p *pubProvider) CurrentUser(context.Context) (provider.User, error) { return p.me, nil }

func (p *pubProvider) ListThreads(context.Context, provider.PRRef) ([]provider.Thread, error) {
	if p.failList {
		return nil, &provider.Error{Class: provider.ClassUpstream, Status: 500}
	}
	var out []provider.Thread
	for _, c := range p.comments {
		out = append(out, provider.Thread{ID: c.id, Kind: provider.ThreadGeneral, Comments: []provider.CommentItem{{
			ID: c.id, Body: c.body, CreatedAt: c.at, AuthorID: c.userID, AuthorLogin: c.login,
			URL: "https://your-gitea.example/octo/demo/pulls/7#issuecomment-" + c.id,
		}}})
	}
	return out, nil
}

func (p *pubProvider) PostComment(_ context.Context, _ provider.PRRef, body string) (*provider.Comment, error) {
	if p.failPost {
		return nil, &provider.Error{Class: provider.ClassAuth, Status: 403}
	}
	p.nextID++
	id := strconv.Itoa(p.nextID)
	p.comments = append(p.comments, &pubComment{id: id, body: body, login: p.me.Name, userID: p.me.ID,
		at: time.Date(2026, 1, 1, 0, 0, p.nextID, 0, time.UTC)})
	p.writes++
	return &provider.Comment{ID: id, URL: "https://your-gitea.example/octo/demo/pulls/7#issuecomment-" + id}, nil
}

func (p *pubProvider) EditComment(_ context.Context, _ provider.PRRef, id, body string) error {
	if p.failEdit {
		return &provider.Error{Class: provider.ClassNotOwner}
	}
	for _, c := range p.comments {
		if c.id == id {
			c.body = body
			p.writes++
			return nil
		}
	}
	return &provider.Error{Class: provider.ClassNotFound, Status: 404}
}

// publishHarness is a one-call run on a pubProvider with the test renderer.
func publishHarness(caps provider.Capabilities) (*harness, *pubProvider) {
	h := newHarness(map[int][]string{0: {oneCallAnswer}})
	p := newPubProvider(h, caps)
	h.deps.RenderProvider = testRender
	return h, p
}

// testRender is a stand-in for describe/render.Provider (which this package
// cannot import): it renders the summary, the files and the notes, plus the
// capability it saw, so tests can tell the profiles apart.
func testRender(res *Result, caps provider.Capabilities) string {
	var b strings.Builder
	b.WriteString("## PR Description")
	if caps.GFM {
		b.WriteString(" (gfm)")
	}
	b.WriteString("\n\n")
	if res.Description != nil {
		b.WriteString(*res.Description + "\n")
	}
	for _, f := range res.Files {
		b.WriteString("- " + f.Path + ": " + f.Title + "\n")
	}
	for _, n := range res.Notes {
		b.WriteString("note: " + n + "\n")
	}
	return b.String()
}

func descArgs(updateTitle bool) Args {
	return Args{Publish: true, PublishMode: PublishModeDescription, UpdateTitle: updateTitle}
}

// ---- publish_mode=comment ----

func TestPublishCommentPostsThenEditsInPlace(t *testing.T) {
	for name, caps := range map[string]provider.Capabilities{"gitea": giteaCaps, "bitbucket": bbsCaps} {
		t.Run(name, func(t *testing.T) {
			h, p := publishHarness(caps)
			res := h.run(t, Args{Publish: true})
			pub := res.Publish
			if pub == nil || !pub.Published || pub.Updated || pub.Mode != PublishModeComment || pub.CommentID == "" || pub.URL == "" || pub.Error != "" {
				t.Fatalf("publish = %+v", pub)
			}
			if len(p.comments) != 1 || !hasCommentMarker(p.comments[0].body) {
				t.Fatalf("comments = %+v", p.comments)
			}
			if (strings.Contains(p.comments[0].body, "(gfm)")) != caps.GFM {
				t.Errorf("the comment was not rendered for the provider profile: %q", p.comments[0].body)
			}
			first := p.comments[0].body

			// A second run with the same answer edits the same comment, with
			// the same bytes.
			res = h.run(t, Args{Publish: true})
			pub = res.Publish
			if !pub.Published || !pub.Updated || pub.CommentID != p.comments[0].id {
				t.Fatalf("second publish = %+v", pub)
			}
			if len(p.comments) != 1 || p.comments[0].body != first {
				t.Errorf("second run changed the comment bytes or posted again:\n%q\n%q", first, p.comments[0].body)
			}
			if p.writes != 2 {
				t.Errorf("writes = %d, want a post and an edit", p.writes)
			}
		})
	}
}

// hasCommentMarker reports whether body's last line is CommentMarker.
func hasCommentMarker(body string) bool {
	return strings.HasSuffix(strings.TrimRight(body, "\n"), "\n\n"+CommentMarker)
}

// X-12: the author is checked, and the older duplicates are noted and kept.
func TestPublishCommentEditRule(t *testing.T) {
	h, p := publishHarness(giteaCaps)
	at := func(s int) time.Time { return time.Date(2026, 1, 1, 0, 0, s, 0, time.UTC) }
	p.comments = []*pubComment{
		{id: "5", body: "old one\n\n" + CommentMarker, login: "review-bot", userID: "42", at: at(1)},
		{id: "9", body: "newest\n\n" + CommentMarker, login: "review-bot", userID: "42", at: at(3)},
		{id: "7", body: "planted\n\n" + CommentMarker, login: "mallory", userID: "66", at: at(5)},
		{id: "8", body: "newer but marker not last\n" + CommentMarker + "\ntext", login: "review-bot", userID: "42", at: at(6)},
	}
	p.nextID = 20
	res := h.run(t, Args{Publish: true})
	pub := res.Publish
	if !pub.Published || !pub.Updated || pub.CommentID != "9" {
		t.Fatalf("publish = %+v, want the newest own marked comment (9) edited", pub)
	}
	if !strings.HasSuffix(p.comments[1].body, CommentMarker) || strings.HasPrefix(p.comments[1].body, "newest") {
		t.Errorf("comment 9 was not rewritten: %q", p.comments[1].body)
	}
	for _, i := range []int{0, 2, 3} {
		if strings.Contains(p.comments[i].body, "PR Description") {
			t.Errorf("comment %s was changed", p.comments[i].id)
		}
	}
	if len(p.comments) != 4 || p.writes != 1 {
		t.Errorf("comments %d, writes %d: older comments must never be deleted or duplicated", len(p.comments), p.writes)
	}
	if !contains(res.Notes, "1 older description comment by the same user was left unchanged.") {
		t.Errorf("notes = %q", res.Notes)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestPublishCommentFallbacks(t *testing.T) {
	t.Run("edit fails, a new one is posted", func(t *testing.T) {
		h, p := publishHarness(giteaCaps)
		p.comments = []*pubComment{{id: "5", body: "x\n\n" + CommentMarker, login: "review-bot", userID: "42"}}
		p.failEdit = true
		res := h.run(t, Args{Publish: true})
		if !res.Publish.Published || res.Publish.Updated || len(p.comments) != 2 || !contains(res.Notes, NoteCommentReplaced) {
			t.Errorf("publish %+v comments %d notes %q", res.Publish, len(p.comments), res.Notes)
		}
	})
	t.Run("lookup fails, a new one is posted", func(t *testing.T) {
		h, p := publishHarness(giteaCaps)
		p.failList = true
		res := h.run(t, Args{Publish: true})
		if !res.Publish.Published || len(p.comments) != 1 || !contains(res.Notes, NoteCommentLookupFailed) {
			t.Errorf("publish %+v comments %d notes %q", res.Publish, len(p.comments), res.Notes)
		}
	})
	t.Run("nothing works: the description is still returned", func(t *testing.T) {
		h, p := publishHarness(giteaCaps)
		p.comments = []*pubComment{{id: "5", body: "x\n\n" + CommentMarker, login: "review-bot", userID: "42"}}
		p.failEdit, p.failPost = true, true
		res := h.run(t, Args{Publish: true})
		if res.Publish.Published || res.Publish.Error != "authentication failed: check the token and its scopes (HTTP 403)" {
			t.Errorf("publish = %+v", res.Publish)
		}
		if contains(res.Notes, NoteCommentReplaced) {
			t.Errorf("a note claims a new comment was posted: %q", res.Notes)
		}
		if res.Title == nil || len(res.Files) != 2 {
			t.Errorf("the description was discarded: %+v", res)
		}
	})
	t.Run("no renderer", func(t *testing.T) {
		h, _ := publishHarness(giteaCaps)
		h.deps.RenderProvider = nil
		res := h.run(t, Args{Publish: true})
		if res.Publish.Published || res.Publish.Error != publishFailedMessage {
			t.Errorf("publish = %+v", res.Publish)
		}
	})
}

// A run that does not publish leaves no trace.
func TestNoPublishNoWrite(t *testing.T) {
	h, p := publishHarness(giteaCaps)
	res := h.run(t, Args{})
	if res.Publish != nil || p.writes != 0 || p.gets != 1 {
		t.Errorf("publish %+v writes %d gets %d", res.Publish, p.writes, p.gets)
	}
}

// The published comment never carries model text that forges a marker.
func TestPublishedTextCannotForgeMarkers(t *testing.T) {
	// The real escaping lives in describe/render (its tests); here the
	// pipeline's own wrapping is checked: the marker is the last line, once.
	h, p := publishHarness(giteaCaps)
	h.run(t, Args{Publish: true})
	if n := strings.Count(p.comments[0].body, CommentMarker); n != 1 || !hasCommentMarker(p.comments[0].body) {
		t.Errorf("marker count %d in %q", n, p.comments[0].body)
	}
}

// ---- publish_mode=description ----

func TestPublishDescriptionRefusedWithoutDescriptionEdit(t *testing.T) {
	h, p := publishHarness(provider.Capabilities{GFM: true})
	res := h.run(t, descArgs(true))
	if res.Publish.Published || res.Publish.Error != "This provider does not support editing the pull request description; use publish_mode=comment." {
		t.Errorf("publish = %+v", res.Publish)
	}
	if p.writes != 0 || p.updates != 0 || p.gets != 1 {
		t.Errorf("writes %d updates %d gets %d: nothing may be requested", p.writes, p.updates, p.gets)
	}
	if res.Title == nil {
		t.Errorf("the description was discarded")
	}
}

func TestPublishDescriptionAppendThenReplace(t *testing.T) {
	for name, versioned := range map[string]bool{"gitea": false, "bitbucket": true} {
		t.Run(name, func(t *testing.T) {
			h, p := publishHarness(giteaCaps)
			p.versioned = versioned
			author := p.desc // "Raises the retry count. DESC-MARKER"
			res := h.run(t, descArgs(false))
			pub := res.Publish
			if !pub.Published || pub.Updated || pub.Mode != PublishModeDescription || pub.TitleUpdated || pub.CommentID != "" || pub.Error != "" {
				t.Fatalf("publish = %+v", pub)
			}
			if !strings.HasPrefix(p.desc, author+"\n\n"+RegionStart+"\n") || !strings.HasSuffix(p.desc, "\n"+RegionEnd) {
				t.Fatalf("description = %q", p.desc)
			}
			if p.title != "Retry more "+titleMarker || len(p.titles) != 0 {
				t.Errorf("the title changed: %q (%v)", p.title, p.titles)
			}
			first := p.desc

			// Idempotence: the same answer yields the same bytes. The write
			// is skipped because it would change nothing.
			res = h.run(t, descArgs(false))
			if !res.Publish.Published || !res.Publish.Updated || p.desc != first {
				t.Fatalf("second run: %+v desc changed=%v", res.Publish, p.desc != first)
			}

			// A different answer replaces only the region.
			h.llm.answers[0] = []string{strings.Replace(oneCallAnswer, "Raise the retry count to three", "Something else", 1)}
			res = h.run(t, descArgs(false))
			if !res.Publish.Updated || !strings.HasPrefix(p.desc, author+"\n\n"+RegionStart) || !strings.Contains(p.desc, "Something else") ||
				strings.Count(p.desc, RegionStart) != 1 {
				t.Errorf("third run: %+v desc %q", res.Publish, p.desc)
			}
		})
	}
}

func TestPublishDescriptionKeepsAuthorBytes(t *testing.T) {
	for name, versioned := range map[string]bool{"gitea": false, "bitbucket": true} {
		t.Run(name, func(t *testing.T) {
			h, p := publishHarness(giteaCaps)
			p.versioned = versioned
			p.desc = authorBefore + RegionStart + "\r\nold\r\n" + RegionEnd + authorAfter
			h.run(t, descArgs(false))
			if !strings.HasPrefix(p.desc, authorBefore+RegionStart+"\r\n") || !strings.HasSuffix(p.desc, RegionEnd+authorAfter) ||
				strings.Contains(p.desc, "old") {
				t.Errorf("description = %q", p.desc)
			}
		})
	}
}

func TestPublishDescriptionDamagedRegionWritesNothing(t *testing.T) {
	for name, desc := range map[string]string{
		"two starts":      "a\n" + RegionStart + "\nx\n" + RegionStart + "\n" + RegionEnd,
		"end before":      "a\n" + RegionEnd + "\nx\n" + RegionStart,
		"missing end":     "a\n" + RegionStart + "\nx",
		"end only":        "a\n" + RegionEnd,
		"two region sets": RegionStart + "\n" + RegionEnd + "\n" + RegionStart + "\n" + RegionEnd,
	} {
		t.Run(name, func(t *testing.T) {
			h, p := publishHarness(giteaCaps)
			p.desc = desc
			res := h.run(t, descArgs(true))
			if res.Publish.Published || res.Publish.Error != MsgDamagedRegion {
				t.Fatalf("publish = %+v", res.Publish)
			}
			if p.writes != 0 || p.updates != 0 || p.desc != desc || p.title != "Retry more "+titleMarker {
				t.Errorf("something was written: writes %d updates %d", p.writes, p.updates)
			}
			if res.Title == nil || len(res.Files) != 2 {
				t.Errorf("the description was discarded")
			}
		})
	}
}

func TestPublishDescriptionTitle(t *testing.T) {
	t.Run("update_title replaces the title", func(t *testing.T) {
		h, p := publishHarness(giteaCaps)
		res := h.run(t, descArgs(true))
		if !res.Publish.TitleUpdated || p.title != "Raise the retry count and test it" {
			t.Errorf("publish %+v title %q", res.Publish, p.title)
		}
	})
	t.Run("without update_title the title is untouched", func(t *testing.T) {
		h, p := publishHarness(giteaCaps)
		h.run(t, descArgs(false))
		if p.title != "Retry more "+titleMarker || len(p.titles) != 0 {
			t.Errorf("title %q writes %v", p.title, p.titles)
		}
	})
	t.Run("no generated title: untouched, with a note", func(t *testing.T) {
		h := threePartHarness(t)
		h.llm.errs[kindReduce] = errors.New("reduce down")
		p := newPubProvider(h, giteaCaps)
		h.deps.RenderProvider = testRender
		before := p.title
		res := h.run(t, descArgs(true))
		if res.Title != nil || !res.Publish.Published || res.Publish.TitleUpdated || p.title != before || !contains(res.Notes, NoteTitleNotGenerated) {
			t.Errorf("title %v publish %+v stored title %q notes %q", res.Title, res.Publish, p.title, res.Notes)
		}
		if p.desc == "" || !strings.Contains(p.desc, RegionStart) {
			t.Errorf("the description was not written")
		}
	})
	t.Run("update_title alone writes the title when the region is current", func(t *testing.T) {
		h, p := publishHarness(giteaCaps)
		h.run(t, descArgs(false))
		before := p.desc
		res := h.run(t, descArgs(true))
		if !res.Publish.TitleUpdated || p.desc != before || len(p.descs) != 1 {
			t.Errorf("publish %+v descs %d", res.Publish, len(p.descs))
		}
	})
}

func TestPublishDescriptionAuthFailureReusesTheFixedSentence(t *testing.T) {
	h, p := publishHarness(giteaCaps)
	p.updateErr = &provider.Error{Class: provider.ClassAuth, Status: 403}
	res := h.run(t, descArgs(false))
	if res.Publish.Published || res.Publish.Error != "authentication failed: check the token and its scopes (HTTP 403)" {
		t.Errorf("publish = %+v", res.Publish)
	}
}

// ---- concurrency ----

// Gitea has no version: a description that changed between the pipeline's
// read and the re-read is recomputed once, on the fresh text.
func TestConcurrencyGiteaChangedOnceIsRecomputed(t *testing.T) {
	h, p := publishHarness(giteaCaps)
	edited := "Edited by a human meanwhile."
	p.beforeGet = func(n int) {
		if n == 2 { // the re-read
			p.edit(edited)
		}
	}
	res := h.run(t, descArgs(false))
	if !res.Publish.Published || res.Publish.Error != "" {
		t.Fatalf("publish = %+v", res.Publish)
	}
	if !strings.HasPrefix(p.desc, edited+"\n\n"+RegionStart) {
		t.Errorf("the human edit was lost: %q", p.desc)
	}
	if p.gets != 3 || p.writes != 1 {
		t.Errorf("gets %d writes %d, want one re-read per computation and a single write", p.gets, p.writes)
	}
}

// [canary] dropping the re-read: the write goes out on the stale text.
func TestConcurrencyGiteaChangedTwiceIsRefused(t *testing.T) {
	h, p := publishHarness(giteaCaps)
	n := 0
	p.beforeGet = func(call int) {
		if call >= 2 { // both re-reads see a new edit
			n++
			p.edit("Edit number " + strconv.Itoa(n))
		}
	}
	res := h.run(t, descArgs(true))
	if res.Publish.Published || res.Publish.Error != MsgChangedWhileUpdating {
		t.Fatalf("publish = %+v", res.Publish)
	}
	if p.writes != 0 || p.updates != 0 || strings.Contains(p.desc, RegionStart) || p.title != "Retry more "+titleMarker {
		t.Errorf("something was written: writes %d desc %q", p.writes, p.desc)
	}
	if MsgChangedWhileUpdating != "The pull request description changed while it was being updated; nothing was written." {
		t.Errorf("sentence = %q", MsgChangedWhileUpdating)
	}
}

// Bitbucket Server: a 409 on the write (the PR changed after the re-read) is
// retried once on the freshly read text.
func TestConcurrencyBitbucketConflictRetriedOnce(t *testing.T) {
	h, p := publishHarness(bbsCaps)
	p.versioned = true
	p.beforeUpdate = func(n int) {
		if n == 1 {
			p.edit("Edited between the re-read and the write.")
		}
	}
	res := h.run(t, descArgs(false))
	if !res.Publish.Published || res.Publish.Error != "" {
		t.Fatalf("publish = %+v", res.Publish)
	}
	if !strings.HasPrefix(p.desc, "Edited between the re-read and the write.\n\n"+RegionStart) {
		t.Errorf("the human edit was lost: %q", p.desc)
	}
	if p.updates != 2 || p.writes != 1 {
		t.Errorf("updates %d writes %d, want a 409 and one retry", p.updates, p.writes)
	}
}

func TestConcurrencyBitbucketSecondConflictIsRefused(t *testing.T) {
	h, p := publishHarness(bbsCaps)
	p.versioned = true
	n := 0
	p.beforeUpdate = func(int) { n++; p.edit("Edit " + strconv.Itoa(n)) }
	res := h.run(t, descArgs(false))
	if res.Publish.Published || res.Publish.Error != MsgChangedWhileUpdating {
		t.Fatalf("publish = %+v", res.Publish)
	}
	if p.updates != 2 || p.writes != 0 || strings.Contains(p.desc, RegionStart) {
		t.Errorf("updates %d writes %d desc %q", p.updates, p.writes, p.desc)
	}
}

// The write carries the version of the re-read, not the pipeline's first
// read: a version that moved without the text changing is not a conflict.
func TestConcurrencyVersionOfTheReRead(t *testing.T) {
	h, p := publishHarness(bbsCaps)
	p.versioned = true
	p.beforeGet = func(n int) {
		if n == 2 {
			p.version += 5 // e.g. a reviewer was added
		}
	}
	res := h.run(t, descArgs(false))
	if !res.Publish.Published || p.updates != 1 {
		t.Errorf("publish %+v updates %d", res.Publish, p.updates)
	}
}

func TestPublishEmptyResultIsPublishedLikeReview(t *testing.T) {
	h, p := publishHarness(giteaCaps)
	h.deps.Config.Ignore.Glob = []string{"**"}
	res := h.run(t, Args{Publish: true})
	if !res.Publish.Published || len(p.comments) != 1 || !contains(res.Notes, NoteNoReviewableChanges) {
		t.Errorf("publish %+v comments %d notes %q", res.Publish, len(p.comments), res.Notes)
	}
}

// In description mode, with no described file and no summary, nothing is
// written and no request is sent beyond the pipeline's own reads; comment
// mode still publishes the coverage (above).
func TestPublishDescriptionNothingDescribed(t *testing.T) {
	for name, caps := range map[string]provider.Capabilities{"gitea": giteaCaps, "bitbucket": bbsCaps} {
		t.Run(name, func(t *testing.T) {
			h, p := publishHarness(caps)
			p.versioned = caps == bbsCaps
			h.deps.Config.Ignore.Glob = []string{"**"}
			before := p.desc
			res := h.run(t, descArgs(true))
			pub := res.Publish
			if pub.Published || pub.Error != MsgNothingDescribed || MsgNothingDescribed != "Nothing was described, so the pull request description was not changed." {
				t.Errorf("publish = %+v", pub)
			}
			if p.gets != 1 || p.updates != 0 || p.writes != 0 || p.statusCalls != 0 || len(p.comments) != 0 || p.desc != before {
				t.Errorf("requests: gets %d updates %d writes %d status %d comments %d", p.gets, p.updates, p.writes, p.statusCalls, len(p.comments))
			}
		})
	}
}

// Published bodies are slash-sanitised on a provider with quick actions, in
// both modes (provider.SanitizeBody).
func TestPublishSanitisesQuickActions(t *testing.T) {
	caps := provider.Capabilities{GFM: true, DescriptionEdit: true, QuickActions: true}
	render := func(*Result, provider.Capabilities) string { return "/approve\n- text\n/merge" }
	t.Run("comment", func(t *testing.T) {
		h, p := publishHarness(caps)
		h.deps.RenderProvider = render
		h.run(t, Args{Publish: true})
		if strings.Contains(p.comments[0].body, "\n/") || strings.HasPrefix(p.comments[0].body, "/") {
			t.Errorf("a line starts with a slash: %q", p.comments[0].body)
		}
	})
	t.Run("description", func(t *testing.T) {
		h, p := publishHarness(caps)
		h.deps.RenderProvider = render
		h.run(t, descArgs(false))
		if strings.Contains(p.desc, "\n/") || !strings.Contains(p.desc, "\n /approve\n- text\n /merge\n"+RegionEnd) {
			t.Errorf("description = %q", p.desc)
		}
	})
}

// A full-replace provider (one that reports a Version) can lose reviewer
// state while the description is written: the note says so, and the publish
// still counts as published.
func TestPublishDescriptionReviewerGuard(t *testing.T) {
	alice := provider.Reviewer{User: provider.User{ID: "1", Name: "alice"}, State: provider.ReviewApproved}
	bob := provider.Reviewer{User: provider.User{ID: "2", Name: "bob"}, State: provider.ReviewPending}
	setup := func(update func(p *pubProvider)) (*Result, *pubProvider) {
		h, p := publishHarness(bbsCaps)
		p.versioned, p.version = true, 3
		p.reviewers = []provider.Reviewer{alice, bob}
		p.beforeUpdate = func(int) {
			if update != nil {
				update(p)
			}
		}
		return h.run(t, descArgs(false)), p
	}
	t.Run("unchanged reviewers: no note", func(t *testing.T) {
		res, p := setup(nil)
		if !res.Publish.Published || contains(res.Notes, NoteReviewersChanged) || contains(res.Notes, NoteReviewersUnchecked) {
			t.Errorf("publish %+v notes %q", res.Publish, res.Notes)
		}
		if p.statusCalls != 2 {
			t.Errorf("status reads = %d, want one before and one after the write", p.statusCalls)
		}
	})
	t.Run("a verdict reset during the update", func(t *testing.T) {
		res, _ := setup(func(p *pubProvider) { p.reviewers[0].State = provider.ReviewPending })
		if !res.Publish.Published || !contains(res.Notes, NoteReviewersChanged) {
			t.Errorf("publish %+v notes %q", res.Publish, res.Notes)
		}
	})
	t.Run("a reviewer dropped during the update", func(t *testing.T) {
		res, _ := setup(func(p *pubProvider) { p.reviewers = p.reviewers[:1] })
		if !res.Publish.Published || !contains(res.Notes, NoteReviewersChanged) {
			t.Errorf("publish %+v notes %q", res.Publish, res.Notes)
		}
	})
	t.Run("the after-read fails: a different note", func(t *testing.T) {
		h, p := publishHarness(bbsCaps)
		p.versioned, p.version, p.reviewers, p.statusFail = true, 3, []provider.Reviewer{alice}, 2
		res := h.run(t, descArgs(false))
		if !res.Publish.Published || contains(res.Notes, NoteReviewersChanged) || !contains(res.Notes, NoteReviewersUnchecked) {
			t.Errorf("publish %+v notes %q", res.Publish, res.Notes)
		}
	})
	t.Run("no version: no extra reads", func(t *testing.T) {
		h, p := publishHarness(giteaCaps)
		p.reviewers = []provider.Reviewer{alice}
		res := h.run(t, descArgs(false))
		if !res.Publish.Published || p.statusCalls != 0 {
			t.Errorf("publish %+v status reads %d", res.Publish, p.statusCalls)
		}
	})
	t.Run("nothing written: no after-read", func(t *testing.T) {
		h, p := publishHarness(bbsCaps)
		p.versioned, p.version, p.reviewers = true, 3, []provider.Reviewer{alice}
		h.run(t, descArgs(false))
		p.statusCalls = 0
		h.run(t, descArgs(false)) // idempotent: the region is current
		if p.statusCalls != 0 {
			t.Errorf("status reads = %d on an idempotent run", p.statusCalls)
		}
	})
}
