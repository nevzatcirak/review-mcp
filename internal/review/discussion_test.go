package review

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

var (
	botUser      = provider.User{ID: "5", Name: "review-bot"}
	discussionAt = time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
)

// citem is a comment of a test thread; created is minutes after discussionAt.
func citem(author provider.User, minutes int, body string) provider.CommentItem {
	at := discussionAt.Add(time.Duration(minutes) * time.Minute)
	return provider.CommentItem{ID: "c" + body[:min(4, len(body))], Author: author.Name, Body: body,
		CreatedAt: at, UpdatedAt: at, AuthorID: author.ID, AuthorLogin: author.Name}
}

func general(cs ...provider.CommentItem) provider.Thread {
	return provider.Thread{ID: "g", Kind: provider.ThreadGeneral, Comments: cs}
}

func inlineThread(path string, line int, resolved bool, cs ...provider.CommentItem) provider.Thread {
	return provider.Thread{ID: "i", Kind: provider.ThreadInline, Path: path, Line: line, Resolved: &resolved, Comments: cs}
}

var (
	alice = provider.User{ID: "11", Name: "alice"}
	bob   = provider.User{ID: "12", Name: "bob"}
	carol = provider.User{ID: "13", Name: "carol"}
	dave  = provider.User{ID: "14", Name: "dave"}
	// mallory writes comments that carry our markers.
	mallory = provider.User{ID: "66", Name: "mallory"}
)

// sampleDiscussion is the thread list of the discussion golden: a general
// thread with more replies than are shown, a resolved inline thread, a
// comment with a code fence, a thread of ours whose root is excluded but
// which has a human reply, an overview of ours (excluded whole) and a
// foreign comment that carries our marker (kept: anyone can type it).
func sampleDiscussion() []provider.Thread {
	ours := FingerprintMarker("0123456789ab")
	return []provider.Thread{
		general(
			citem(alice, 0, "Why not use a context with a deadline for the request?"),
			citem(bob, 5, "Good point, I will add one."),
			citem(alice, 9, "Thanks."),
			citem(carol, 12, "This third reply is not shown."),
		),
		general(citem(botUser, 30, "## PR Review\n\nsummary\n\n"+OverviewMarker)),
		general(citem(mallory, 31, "Looks fine to me.\n\n"+OverviewMarker)),
		inlineThread("src/app.go", 11, true,
			citem(carol, 40, "This loop looks off by one."),
			citem(bob, 44, "Fixed in the latest commit.")),
		inlineThread("src/util.go", 0, false,
			citem(alice, 50, "Consider this instead:\n```go\nx := compute()\n```\n")),
		inlineThread("src/app.go", 20, false,
			citem(botUser, 60, "**Unchecked error**\n\nThe error is dropped.\n\n"+ours),
			citem(dave, 65, "Not an issue, the value is checked by the caller.")),
	}
}

func TestOwnMarked(t *testing.T) {
	fp := FingerprintMarker("0123456789ab")
	for name, tc := range map[string]struct {
		author provider.User
		body   string
		want   bool
	}{
		"our overview":              {botUser, "text\n\n" + OverviewMarker, true},
		"our fingerprint":           {botUser, "text\n\n" + fp, true},
		"our comment without":       {botUser, "a plain comment", false},
		"marker not on the last":    {botUser, OverviewMarker + "\nmore", false},
		"foreign overview":          {mallory, "text\n\n" + OverviewMarker, false},
		"foreign fingerprint":       {mallory, "text\n\n" + fp, false},
		"our login, another id":     {provider.User{ID: "6", Name: "review-bot"}, "t\n\n" + fp, false},
		"our id, no login (by id)":  {provider.User{ID: "5"}, "t\n\n" + fp, true},
		"no ids, other login":       {provider.User{Name: "someone"}, "t\n\n" + fp, false},
		"fingerprint with bad hash": {botUser, "t\n\n[//]: # (review-mcp:finding:XYZ)", false},
	} {
		c := citem(tc.author, 0, tc.body)
		if got := ownMarked(&c, botUser); got != tc.want {
			t.Errorf("%s: ownMarked = %v, want %v", name, got, tc.want)
		}
	}
}

func TestHumanThreads(t *testing.T) {
	in := sampleDiscussion()
	got := humanThreads(in, botUser)
	// Dropped: our overview. Kept: everything else, the thread of ours
	// without its root, and mallory's comment with our marker.
	if len(got) != 5 {
		t.Fatalf("threads = %d, want 5", len(got))
	}
	last := got[4]
	if len(last.Comments) != 1 || last.Comments[0].Author != "dave" || last.Path != "src/app.go" || last.Line != 20 {
		t.Errorf("thread of ours with a human reply = %+v", last)
	}
	if c := got[1].Comments[0]; c.Author != "mallory" || !HasOverviewMarker(c.Body) {
		t.Errorf("the foreign marker comment must stay: %+v", c)
	}
	if len(in[5].Comments) != 2 {
		t.Errorf("the input was modified")
	}
	// A thread with nothing human left is dropped.
	only := []provider.Thread{inlineThread("a.go", 1, false, citem(botUser, 0, "x\n\n"+FingerprintMarker("0123456789ab")))}
	if got := humanThreads(only, botUser); len(got) != 0 {
		t.Errorf("threads = %+v", got)
	}
}

func TestFingerprintsOf(t *testing.T) {
	a, b, c := FingerprintMarker("aaaaaaaaaaaa"), FingerprintMarker("bbbbbbbbbbbb"), FingerprintMarker("cccccccccccc")
	threads := []provider.Thread{
		inlineThread("a.go", 1, false, citem(botUser, 0, "t\n\n"+a)),
		// Gitea groups the review comments of one line: ours may be a reply.
		inlineThread("a.go", 2, false, citem(alice, 0, "human"), citem(botUser, 1, "t\n\n"+b)),
		// Not ours, and not an inline thread.
		inlineThread("a.go", 3, false, citem(mallory, 0, "t\n\n"+c)),
		general(citem(botUser, 0, "t\n\n"+c)),
	}
	got := fingerprintsOf(threads, botUser)
	if len(got) != 2 || !got["aaaaaaaaaaaa"] || !got["bbbbbbbbbbbb"] {
		t.Errorf("fingerprints = %v", got)
	}
}

func TestCleanBody(t *testing.T) {
	long := strings.Repeat("é", 700)
	for name, tc := range map[string]struct{ in, want string }{
		"trimmed":          {"  hi \n", "hi"},
		"crlf":             {"a\r\nb\rc", "a\nb\nc"},
		"invalid utf8":     {"a\xffb", "a�b"},
		"control chars":    {"a\x00b\x1b[31mc\td", "ab[31mc\td"},
		"cut at 600 runes": {long, strings.Repeat("é", 600) + "…"},
		"exactly 600":      {strings.Repeat("x", 600), strings.Repeat("x", 600)},
	} {
		if got := cleanBody(tc.in); got != tc.want {
			t.Errorf("%s: cleanBody = %q, want %q", name, got, tc.want)
		}
	}
	if got := cleanLine("  al\nice  ", 64); got != "al ice" {
		t.Errorf("cleanLine = %q", got)
	}
}

// TestDiscussionGolden pins the block's rendering (header, entry layout,
// reply cap, the adaptive fence, an excluded root with a human reply). The
// same text is the discussion of the prompt-golden case with_discussion.
func TestDiscussionGolden(t *testing.T) {
	factor := config.Defaults().LLM.TokenEstimateFactor
	d := renderDiscussion(humanThreads(sampleDiscussion(), botUser), DefaultMaxDiscussionTokens, factor)
	if d.Included != 5 || d.Omitted != 0 {
		t.Fatalf("included %d omitted %d", d.Included, d.Omitted)
	}
	checkGolden(t, "testdata/discussion/sample.txt", d.Block+"\n")
}

func TestDiscussionBudget(t *testing.T) {
	factor := config.Defaults().LLM.TokenEstimateFactor
	// Thread 0 is the newest (its comment is the latest) though it comes
	// first; thread 4 is the oldest.
	var threads []provider.Thread
	for i, min := range []int{50, 10, 20, 30, 0} {
		threads = append(threads, general(citem(alice, min, "thread "+string(rune('A'+i))+" "+strings.Repeat("words ", 60))))
	}
	full := renderDiscussion(threads, 100000, factor)
	if full.Included != 5 || full.Omitted != 0 {
		t.Fatalf("full: %d/%d", full.Included, full.Omitted)
	}
	need := tokens.Estimate(full.Block, factor)
	d := renderDiscussion(threads, need-1, factor)
	if d.Included >= 5 || d.Omitted != 5-d.Included || d.Included < 1 {
		t.Fatalf("clipped: included %d omitted %d", d.Included, d.Omitted)
	}
	if got := tokens.Estimate(d.Block, factor); got > need-1 {
		t.Errorf("block is %d tokens, budget %d", got, need-1)
	}
	// Newest first: A (newest) is kept and E (oldest) is the first to go.
	if !strings.Contains(d.Block, "thread A ") || strings.Contains(d.Block, "thread E ") {
		t.Errorf("kept the wrong threads:\n%s", d.Block)
	}
	// What is kept keeps its provider order.
	if ia, ib := strings.Index(d.Block, "thread A "), strings.Index(d.Block, "thread D "); ib >= 0 && ia > ib {
		t.Errorf("order changed")
	}
	// Not even the newest fits: no block, everything counts as omitted.
	if d := renderDiscussion(threads, 10, factor); d.Block != "" || d.Included != 0 || d.Omitted != 5 {
		t.Errorf("tiny budget: %+v", d)
	}
	// Off.
	if d := renderDiscussion(threads, 0, factor); d != (discussion{}) {
		t.Errorf("budget 0: %+v", d)
	}
}

// discussionHarness plants the sampleDiscussion on a harness's PR.
func discussionHarness(answers ...string) *harness {
	h := newHarness(answers...)
	h.prov.threads = sampleDiscussion()
	return h
}

func TestRunDiscussionBlock(t *testing.T) {
	t.Run("in the prompt, between the description and the diff", func(t *testing.T) {
		h := discussionHarness(goodAnswer)
		res, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL})
		if err != nil {
			t.Fatal(err)
		}
		user := h.llm.calls[0].user
		i, j, k := strings.Index(user, descMarker), strings.Index(user, DiscussionHeader), strings.Index(user, "The PR code diff:")
		if i <= 0 || i >= j || j >= k {
			t.Fatalf("order description %d, discussion %d, diff %d", i, j, k)
		}
		for _, want := range []string{"[general]\nWhy not use a context", "[inline src/app.go:11] (resolved)\nThis loop looks off by one.\nbob: Fixed in the latest commit.",
			"[inline src/app.go:20]\nNot an issue, the value is checked by the caller."} {
			if !strings.Contains(user, want) {
				t.Errorf("prompt lacks %q", want)
			}
		}
		for _, bad := range []string{"The error is dropped", "summary", "third reply"} {
			if strings.Contains(user, bad) {
				t.Errorf("prompt has %q", bad)
			}
		}
		if res.Metadata.AlreadyDiscussed != 5 || len(res.Notes) != 0 {
			t.Errorf("already_discussed %d, notes %q", res.Metadata.AlreadyDiscussed, res.Notes)
		}
		if h.prov.lists != 1 {
			t.Errorf("ListThreads ran %d times", h.prov.lists)
		}
	})

	t.Run("counted in the prompt tokens", func(t *testing.T) {
		with := discussionHarness(goodAnswer)
		a, err := Run(context.Background(), with.deps, Args{PRURL: testPRURL})
		if err != nil {
			t.Fatal(err)
		}
		without := discussionHarness(goodAnswer)
		zero := 0
		b, err := Run(context.Background(), without.deps, Args{PRURL: testPRURL, MaxDiscussionTokens: &zero})
		if err != nil {
			t.Fatal(err)
		}
		factor := with.deps.Config.LLM.TokenEstimateFactor
		block := renderDiscussion(humanThreads(sampleDiscussion(), botUser), DefaultMaxDiscussionTokens, factor).Block
		got, want := a.Metadata.PromptTokens-b.Metadata.PromptTokens, tokens.Estimate(block, factor)
		// The BPE count is not exactly additive at the seams (blank lines).
		if got < want-10 || got > want+10 || want < 200 {
			t.Errorf("the block adds %d prompt tokens, its own estimate is %d", got, want)
		}
		if strings.Contains(without.llm.calls[0].user, DiscussionHeader) {
			t.Errorf("a budget of 0 still renders the block")
		}
	})

	t.Run("a budget of 0 reads nothing without a publish", func(t *testing.T) {
		h := discussionHarness(goodAnswer)
		zero := 0
		res, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL, MaxDiscussionTokens: &zero})
		if err != nil {
			t.Fatal(err)
		}
		if h.prov.lists != 0 || res.Metadata.AlreadyDiscussed != 0 || len(res.Notes) != 0 {
			t.Errorf("lists %d, already_discussed %d, notes %q", h.prov.lists, res.Metadata.AlreadyDiscussed, res.Notes)
		}
	})

	t.Run("the budget leaves threads out and says so", func(t *testing.T) {
		h := discussionHarness(goodAnswer)
		factor := h.deps.Config.LLM.TokenEstimateFactor
		full := renderDiscussion(humanThreads(sampleDiscussion(), botUser), DefaultMaxDiscussionTokens, factor)
		budget := tokens.Estimate(full.Block, factor) - 1
		res, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL, MaxDiscussionTokens: &budget})
		if err != nil {
			t.Fatal(err)
		}
		n := 5 - res.Metadata.AlreadyDiscussed
		if n < 1 || res.Metadata.AlreadyDiscussed < 1 {
			t.Fatalf("already_discussed %d", res.Metadata.AlreadyDiscussed)
		}
		if want := noteDiscussionLeftOut(n); !slices.Equal(res.Notes, []string{want}) {
			t.Errorf("notes = %q, want %q", res.Notes, want)
		}
	})
}

func TestRunDiscussionReadFailureNeverFailsTheReview(t *testing.T) {
	for name, set := range map[string]func(p *fakeProvider){
		"ListThreads fails":  func(p *fakeProvider) { p.listErr = &provider.Error{Class: provider.ClassUpstream} },
		"CurrentUser fails":  func(p *fakeProvider) { p.meErr = &provider.Error{Class: provider.ClassAuth} },
		"unclassified error": func(p *fakeProvider) { p.listErr = errors.New("boom " + descMarker) },
	} {
		t.Run(name, func(t *testing.T) {
			h := discussionHarness(goodAnswer)
			set(h.prov)
			res, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL})
			if err != nil {
				t.Fatalf("the review failed: %v", err)
			}
			if !slices.Equal(res.Notes, []string{NoteDiscussionUnreadable}) || res.Metadata.AlreadyDiscussed != 0 {
				t.Errorf("notes %q, already_discussed %d", res.Notes, res.Metadata.AlreadyDiscussed)
			}
			if strings.Contains(h.llm.calls[0].user, DiscussionHeader) {
				t.Errorf("a block was rendered without the discussion")
			}
			h.checkNoLeaks(t, err)
		})
	}
}

// TestRunDiscussionNeverMakesTheReviewNotFit: a context window that only
// fits the diff without the optional discussion still gets its review.
func TestRunDiscussionNeverMakesTheReviewNotFit(t *testing.T) {
	// Find the smallest window that fits without the discussion.
	fits := func(window int, disc bool) bool {
		h := discussionHarness(goodAnswer)
		h.deps.Config.LLM.ContextWindow = window
		zero := 0
		args := Args{PRURL: testPRURL}
		if !disc {
			args.MaxDiscussionTokens = &zero
		}
		_, err := Run(context.Background(), h.deps, args)
		return err == nil
	}
	window := 0
	for w := 3000; w < 12000; w += 25 {
		if fits(w, false) {
			window = w
			break
		}
	}
	if window == 0 {
		t.Fatal("no window found")
	}
	h := discussionHarness(goodAnswer)
	h.deps.Config.LLM.ContextWindow = window
	res, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL})
	if err != nil {
		t.Fatalf("window %d: %v", window, err)
	}
	if strings.Contains(h.llm.calls[0].user, DiscussionHeader) {
		t.Skipf("window %d also fits the discussion; nothing to degrade", window)
	}
	if res.Metadata.AlreadyDiscussed != 0 || !slices.Contains(res.Notes, noteDiscussionLeftOut(5)) {
		t.Errorf("already_discussed %d, notes %q", res.Metadata.AlreadyDiscussed, res.Notes)
	}
}
