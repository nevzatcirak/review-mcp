package gitlab

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

func TestParsePRPath(t *testing.T) {
	ok := []struct {
		in, ns, repo string
		n            int64
	}{
		{"/user/project/-/merge_requests/7", "user", "project", 7},
		{"/group/sub/sub2/project/-/merge_requests/12", "group/sub/sub2", "project", 12},
		{"/group/sub/project/-/merge_requests/7/diffs", "group/sub", "project", 7},
		{"/group/project/-/merge_requests/7/commits", "group", "project", 7},
		{"/group/project/-/merge_requests/7/pipelines", "group", "project", 7},
		{"/group/my.project/-/merge_requests/3", "group", "my.project", 3},
		{"/gr%6Fup/project/-/merge_requests/3", "group", "project", 3},
	}
	for _, c := range ok {
		ns, repo, n, err := NewFactory().ParsePRPath(c.in)
		if err != nil || ns != c.ns || repo != c.repo || n != c.n {
			t.Errorf("ParsePRPath(%q) = %q %q %d %v, want %q %q %d", c.in, ns, repo, n, err, c.ns, c.repo, c.n)
		}
	}
	for _, in := range []string{
		"",
		"group/project/-/merge_requests/7",
		"/project/-/merge_requests/7",                // no namespace
		"/group/project/merge_requests/7",            // the old form without /-/
		"/group/project/-/merge_requests",            // no iid
		"/group/project/-/merge_requests/0",          // not positive
		"/group/project/-/merge_requests/+7",         // a sign
		"/group/project/-/merge_requests/7x",         // not a number
		"/group/project/-/issues/7",                  // not a merge request
		"/group/project/-/merge_requests/7/changes",  // unknown tail
		"/group/project/-/merge_requests/7/diffs/x",  // tail too long
		"/group%2Fsub/project/-/merge_requests/7",    // an escaped slash
		"/group/pro%2fject/-/merge_requests/7",       // an escaped slash, lower case
		"/group//project/-/merge_requests/7",         // an empty segment
		"/group/../project/-/merge_requests/7",       // a dot segment
		"/group/%2E%2E/project/-/merge_requests/7",   // an escaped dot segment
		"/group/project/-/merge_requests/7/",         // an empty tail
		"/group/project/-/merge_requests/%zz",        // bad escape
		"/-/merge_requests/7",                        // nothing before /-/
		"/group/-/project/-/merge_requests/7/diffs/", // wrong shape
	} {
		if ns, repo, n, err := NewFactory().ParsePRPath(in); err == nil {
			t.Errorf("ParsePRPath(%q) = %q %q %d, want an error", in, ns, repo, n)
		}
	}
}

// TestResolverContextPath: a merge request URL under the context path
// resolves with the nested namespace; one outside it, or without the
// /-/ form, does not.
func TestResolverContextPath(t *testing.T) {
	cfg := config.Defaults()
	cfg.GitLab.BaseURL = "https://example.com/gitlab"
	cfg.Secrets.GitLabToken = config.NewSecret(testToken)
	r := provider.NewResolver(cfg, nil, NewFactory())
	ref, p, err := r.Resolve("https://example.com/gitlab/group/sub/project/-/merge_requests/7#note_12")
	if err != nil || p.Kind() != provider.KindGitLab || ref.Namespace != "group/sub" || ref.Repo != "project" || ref.Number != 7 {
		t.Fatalf("Resolve = %+v, %v", ref, err)
	}
	for _, u := range []string{
		"https://example.com/group/sub/project/-/merge_requests/7",
		"https://gitlab.example.com/group/project/-/merge_requests/7",
	} {
		if _, _, err := r.Resolve(u); !errors.Is(err, provider.ErrURLNotConfigured) {
			t.Errorf("Resolve(%s) = %v, want url_not_configured", u, err)
		}
	}
	for _, u := range []string{
		"https://example.com/gitlab/group/project/merge_requests/7",
		"https://example.com/gitlab/group%2Fsub/project/-/merge_requests/7",
		"https://example.com/gitlab/group/../project/-/merge_requests/7",
	} {
		if _, _, err := r.Resolve(u); !errors.Is(err, provider.ErrURLMalformed) {
			t.Errorf("Resolve(%s) = %v, want url_malformed", u, err)
		}
	}
}

func TestNewRequiresBaseURL(t *testing.T) {
	_, err := NewFactory().New(config.Defaults(), nil)
	var perr *provider.Error
	if !errors.As(err, &perr) || perr.Class != provider.ClassURLNotConfigured || perr.Hint != "gitlab.base_url" {
		t.Errorf("New without base = %v", err)
	}
}

// TestRequestShape: every request goes to {base}/api/v4 with the project's
// full path as one escaped segment, carries the token only in
// PRIVATE-TOKEN, asks for JSON and names review-mcp. [canary]
func TestRequestShape(t *testing.T) {
	f := newFake(t)
	f.handle(http.MethodGet, fakeMR, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/json" || !strings.HasPrefix(r.Header.Get("User-Agent"), "review-mcp/") ||
			r.Header.Get("Authorization") != "" {
			t.Errorf("headers: Accept %q, User-Agent %q, Authorization set %v", r.Header.Get("Accept"),
				r.Header.Get("User-Agent"), r.Header.Get("Authorization") != "")
		}
		writeJSON(w, mrJSON(true))
	})
	p, _ := f.provider(f.config(), time.Now())
	if _, err := p.GetPullRequest(t.Context(), testRef()); err != nil {
		t.Fatal(err)
	}
	if got := f.requests(); !slices.Equal(got, []string{fakeAPI + fakeMR}) {
		t.Errorf("requests = %q", got)
	}
	for _, r := range f.requests() {
		if strings.Contains(r, testToken) {
			t.Error("the token is in a URL")
		}
	}
}

// TestRateLimit: a 429, or a 403 with RateLimit-Remaining: 0, is
// rate_limited with the reset time in UTC; the one bounded wait happens
// only within MaxRateLimitWait and before the deadline, and is followed by
// exactly one retry; any other 403 is auth, and so is a 401.
func TestRateLimit(t *testing.T) {
	clock := time.Now().UTC().Truncate(time.Second)
	epoch := func(d time.Duration) string { return strconv.FormatInt(clock.Add(d).Unix(), 10) }
	limited := func(status int, reset time.Duration) string {
		return "the server rate-limited the request (HTTP " + strconv.Itoa(status) + "): " + RateLimitHint(clock.Add(reset))
	}
	cases := []struct {
		name      string
		status    int
		headers   map[string]string
		recovers  bool
		deadline  time.Duration
		wantErr   *provider.Error
		wantText  string
		wantWaits []time.Duration
		wantReqs  int
	}{
		{
			name: "429_reset_far", status: 429,
			headers: map[string]string{"RateLimit-Remaining": "0", "RateLimit-Reset": epoch(30 * time.Minute)},
			wantErr: provider.ErrRateLimited, wantReqs: 1, wantText: limited(429, 30*time.Minute),
		},
		{
			name: "429_reset_soon_waits_once", status: 429, recovers: true,
			headers:   map[string]string{"RateLimit-Reset": epoch(20 * time.Second)},
			wantWaits: []time.Duration{20 * time.Second}, wantReqs: 2,
		},
		{
			name: "429_retry_after_waits_once", status: 429, recovers: true,
			headers:   map[string]string{"Retry-After": "45", "RateLimit-Reset": epoch(50 * time.Second)},
			wantWaits: []time.Duration{45 * time.Second}, wantReqs: 2,
		},
		{
			name: "403_remaining_zero_waits_once", status: 403, recovers: true,
			headers:   map[string]string{"RateLimit-Remaining": "0", "Retry-After": "10"},
			wantWaits: []time.Duration{10 * time.Second}, wantReqs: 2,
		},
		{
			name: "still_limited_after_the_one_retry", status: 429,
			headers: map[string]string{"Retry-After": "60"},
			wantErr: provider.ErrRateLimited, wantWaits: []time.Duration{60 * time.Second}, wantReqs: 2,
			wantText: limited(429, 60*time.Second),
		},
		{
			name: "wait_over_60s_not_taken", status: 429,
			headers: map[string]string{"Retry-After": "61"},
			wantErr: provider.ErrRateLimited, wantReqs: 1, wantText: limited(429, 61*time.Second),
		},
		{
			name: "wait_past_deadline_not_taken", status: 429, deadline: 10 * time.Second,
			headers: map[string]string{"Retry-After": "30"},
			wantErr: provider.ErrRateLimited, wantReqs: 1, wantText: limited(429, 30*time.Second),
		},
		{
			name: "429_without_reset", status: 429,
			wantErr: provider.ErrRateLimited, wantReqs: 1, wantText: "the server rate-limited the request (HTTP 429)",
		},
		{
			name: "unusable_reset_not_shown", status: 429,
			headers: map[string]string{"RateLimit-Reset": "soon"},
			wantErr: provider.ErrRateLimited, wantReqs: 1, wantText: "the server rate-limited the request (HTTP 429)",
		},
		{
			name: "403_without_rate_limit_is_auth", status: 403,
			headers: map[string]string{"Retry-After": "5"},
			wantErr: provider.ErrAuth, wantReqs: 1,
			wantText: "authentication failed: check the token and its scopes (HTTP 403)",
		},
		{
			name: "403_with_remaining_quota_is_auth", status: 403,
			headers: map[string]string{"RateLimit-Remaining": "12", "RateLimit-Reset": epoch(time.Minute)},
			wantErr: provider.ErrAuth, wantReqs: 1,
			wantText: "authentication failed: check the token and its scopes (HTTP 403)",
		},
		{
			name: "401_is_auth", status: 401,
			wantErr: provider.ErrAuth, wantReqs: 1,
			wantText: "authentication failed: check the token and its scopes (HTTP 401)",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFake(t)
			var n atomic.Int32
			f.handle(http.MethodGet, "/user", func(w http.ResponseWriter, _ *http.Request) {
				if n.Add(1) == 2 && c.recovers {
					writeJSON(w, map[string]any{"username": "review-bot", "id": 900})
					return
				}
				for k, v := range c.headers {
					w.Header().Set(k, v)
				}
				fakeError(w, c.status)
			})
			p, slept := f.provider(f.config(), clock)
			ctx := context.Background()
			if c.deadline > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, clock.Add(c.deadline))
				defer cancel()
			}
			u, err := p.CurrentUser(ctx)
			switch {
			case c.wantErr == nil && (err != nil || u.Name != "review-bot" || u.ID != "900"):
				t.Fatalf("CurrentUser = %+v, %v; want review-bot after the retry", u, err)
			case c.wantErr != nil && !errors.Is(err, c.wantErr):
				t.Fatalf("err = %v, want class %s", err, c.wantErr.Class)
			}
			if c.wantErr != nil && err.Error() != c.wantText {
				t.Errorf("error text = %q, want %q", err.Error(), c.wantText)
			}
			if err != nil && (strings.Contains(err.Error(), testSentinel) || strings.Contains(err.Error(), testToken)) {
				t.Errorf("error text %q carries the body or the token", err)
			}
			if !slices.Equal(*slept, c.wantWaits) {
				t.Errorf("waits = %v, want %v", *slept, c.wantWaits)
			}
			if got := int(n.Load()); got != c.wantReqs {
				t.Errorf("%d requests, want %d", got, c.wantReqs)
			}
		})
	}
}

// TestDiffRefsRetry: empty diff_refs, or a sha that differs from their
// head (a push GitLab is still processing), are read again once after 2 s,
// inside the deadline; still not ready, or no room for the wait, is
// upstream with the fixed hint. The head revision is always diff_refs'
// head_sha.
func TestDiffRefsRetry(t *testing.T) {
	clock := time.Now().UTC().Truncate(time.Second)
	for _, c := range []struct {
		name   string
		filled int // the read from which diff_refs are filled; 0 never
		// matched is the read from which sha equals diff_refs' head; before
		// it sha is another revision. 0 means sha matches on every read.
		matched   int
		noSHA     bool // sha absent: diff_refs' head alone is the head
		deadline  time.Duration
		wantErr   bool
		wantWaits []time.Duration
		wantReads int32
	}{
		{name: "filled_at_once", filled: 1, wantReads: 1},
		{name: "filled_on_the_retry", filled: 2, wantWaits: []time.Duration{2 * time.Second}, wantReads: 2},
		{name: "never_filled", wantErr: true, wantWaits: []time.Duration{2 * time.Second}, wantReads: 2},
		{name: "filled_too_late", filled: 3, wantErr: true, wantWaits: []time.Duration{2 * time.Second}, wantReads: 2},
		{name: "no_room_before_the_deadline", filled: 2, deadline: 1500 * time.Millisecond, wantErr: true, wantReads: 1},
		{name: "head_matched_on_the_retry", filled: 1, matched: 2, wantWaits: []time.Duration{2 * time.Second}, wantReads: 2},
		{name: "head_never_matched", filled: 1, matched: 3, wantErr: true, wantWaits: []time.Duration{2 * time.Second}, wantReads: 2},
		{name: "no_sha", filled: 1, noSHA: true, wantReads: 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFake(t)
			var n atomic.Int32
			f.handle(http.MethodGet, fakeMR, func(w http.ResponseWriter, _ *http.Request) {
				i := n.Add(1)
				m := mrJSON(c.filled != 0 && int(i) >= c.filled)
				if i == 1 && c.filled != 1 {
					m["diff_refs"] = nil // GitLab may send null as well as empty values
				}
				switch {
				case c.noSHA:
					delete(m, "sha")
				case int(i) < c.matched:
					m["sha"] = testPushed
				}
				writeJSON(w, m)
			})
			p, slept := f.provider(f.config(), clock)
			ctx := context.Background()
			if c.deadline > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, clock.Add(c.deadline))
				defer cancel()
			}
			pr, err := p.GetPullRequest(ctx, testRef())
			switch {
			case c.wantErr:
				want := "the server reported an internal error: the merge request's diff is not ready yet; try again in a moment"
				if !errors.Is(err, provider.ErrUpstream) || err.Error() != want {
					t.Errorf("err = %v, want %q", err, want)
				}
			case err != nil:
				t.Fatalf("GetPullRequest: %v", err)
			case pr.BaseSHA != testBase || pr.HeadSHA != testHead || pr.BaseStrategy != provider.BaseGitLabDiffRefs:
				t.Errorf("base %q head %q strategy %q", pr.BaseSHA, pr.HeadSHA, pr.BaseStrategy)
			}
			if !slices.Equal(*slept, c.wantWaits) || n.Load() != c.wantReads {
				t.Errorf("waits %v, %d reads; want %v, %d", *slept, n.Load(), c.wantWaits, c.wantReads)
			}
		})
	}
}

// TestGetPullRequestFields: the merge request's fields, its state words
// and its changes_count.
func TestGetPullRequestFields(t *testing.T) {
	for _, c := range []struct {
		state, count  string
		draft         bool
		wantState     string
		wantMerged    bool
		wantChanged   int
		wantAtLeast   bool
		nilDesc       bool
		wantDescEmpty bool
	}{
		{state: "opened", count: "12", wantState: "open", wantChanged: 12},
		{state: "merged", count: "1000+", wantState: "merged", wantMerged: true, wantChanged: 1000, wantAtLeast: true},
		{state: "closed", count: "3", draft: true, wantState: "closed", wantChanged: 3},
		{state: "locked", count: "", wantState: "closed", nilDesc: true, wantDescEmpty: true},
	} {
		t.Run(c.state, func(t *testing.T) {
			f := newFake(t)
			m := mrJSON(true)
			m["state"], m["changes_count"], m["draft"] = c.state, c.count, c.draft
			m["detailed_merge_status"] = "not_approved"
			if c.nilDesc {
				m["description"] = nil
			}
			f.json(fakeMR, m)
			p, _ := f.provider(f.config(), time.Now())
			pr, err := p.GetPullRequest(t.Context(), testRef())
			if err != nil {
				t.Fatal(err)
			}
			if pr.State != c.wantState || pr.Merged != c.wantMerged || pr.Draft == nil || *pr.Draft != c.draft ||
				pr.ChangedFiles != c.wantChanged || pr.ChangedFilesAtLeast != c.wantAtLeast {
				t.Errorf("state %q merged %v draft %v changed %d (at least %v)", pr.State, pr.Merged, pr.Draft,
					pr.ChangedFiles, pr.ChangedFilesAtLeast)
			}
			if pr.Title != "Add feature" || pr.Author != "alice" || pr.SourceBranch != "feature" || pr.TargetBranch != "main" ||
				pr.HeadSHA != testHead || pr.WebURL != "https://gitlab.example.com/group/sub/project/-/merge_requests/7" ||
				pr.MergeableState != "not_approved" || pr.Version != "" || pr.Mergeable != nil {
				t.Errorf("fields: %+v", pr)
			}
			if (pr.Description == "") != c.wantDescEmpty {
				t.Errorf("description %q", pr.Description)
			}
		})
	}
}

func TestParseChangesCount(t *testing.T) {
	for in, want := range map[string]changesCount{
		"0": {}, "7": {n: 7}, "1000+": {n: 1000, atLeast: true}, " 12 ": {n: 12},
		"": {}, "+": {}, "x": {}, "-1": {}, "12++": {},
	} {
		if got := parseChangesCount(in); got != want {
			t.Errorf("parseChangesCount(%q) = %+v, want %+v", in, got, want)
		}
	}
	var c changesCount
	if err := c.UnmarshalJSON([]byte("42")); err != nil || c != (changesCount{n: 42}) {
		t.Errorf("number: %+v %v", c, err)
	}
	c = changesCount{n: 9}
	if err := c.UnmarshalJSON([]byte("null")); err != nil || c != (changesCount{}) {
		t.Errorf("null: %+v %v, want unknown", c, err)
	}
	c = changesCount{n: 9}
	if err := c.UnmarshalJSON([]byte("{}")); err != nil || c != (changesCount{n: 9}) {
		t.Errorf("object: %+v %v, want unchanged", c, err)
	}
}

// diffEntry is a /diffs entry of a modified file.
func diffEntry(path, diff string) map[string]any {
	return map[string]any{"old_path": path, "new_path": path, "a_mode": "100644", "b_mode": "100644", "diff": diff,
		"new_file": false, "renamed_file": false, "deleted_file": false, "too_large": false, "collapsed": false}
}

// TestDiffEmptyShapes: what an entry without hunks becomes.
func TestDiffEmptyShapes(t *testing.T) {
	with := func(m, extra map[string]any) map[string]any {
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	entries := []any{
		with(diffEntry("big.txt", ""), map[string]any{"too_large": true}),
		with(diffEntry("folded.txt", ""), map[string]any{"collapsed": true}),
		diffEntry("logo.png", ""),
		diffEntry("blob.dat", "Binary files a/blob.dat and b/blob.dat differ\n"),
		diffEntry("odd.txt", "no hunk here\n"),
		with(diffEntry("empty.txt", ""), map[string]any{"new_file": true, "a_mode": "0"}),
		with(diffEntry("tool.sh", ""), map[string]any{"b_mode": "100755"}),
		with(diffEntry("moved.sh", ""), map[string]any{"old_path": "was.sh", "renamed_file": true, "b_mode": "100755"}),
		with(diffEntry("moved.png", ""), map[string]any{"old_path": "was.png", "renamed_file": true}),
		with(diffEntry("same.go", ""), map[string]any{"renamed_file": true}), // a rename to the same path is a modification
		with(diffEntry("big2.txt", "@@ -1 +1 @@\n-a\n+b"), map[string]any{"collapsed": true}),
	}
	f := newFake(t)
	f.json(fakeMR+"/diffs", entries)
	p, _ := f.provider(f.config(), time.Now())
	p.maxFiles = 0 // no contents: only the shapes matter here
	d, err := p.GetDiff(t.Context(), testRef(), &provider.PullRequest{BaseSHA: testBase, HeadSHA: testHead}, provider.DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	wantSkipped := []provider.SkippedFile{
		{Path: "big.txt", Reason: provider.SkipSizeLimit}, {Path: "folded.txt", Reason: provider.SkipSizeLimit},
		{Path: "logo.png", Reason: provider.SkipBinary}, {Path: "blob.dat", Reason: provider.SkipBinary},
		{Path: "odd.txt", Reason: provider.SkipFetchFailed},
	}
	if !slices.Equal(d.Skipped, wantSkipped) {
		t.Errorf("skipped = %+v, want %+v", d.Skipped, wantSkipped)
	}
	type shape struct {
		path, old, patch string
		typ              provider.ChangeType
		add, del         int
	}
	var got []shape
	for _, fp := range d.Files {
		got = append(got, shape{fp.Path, fp.OldPath, fp.Patch, fp.Type, fp.Additions, fp.Deletions})
	}
	want := []shape{
		{"empty.txt", "", "", provider.ChangeAdded, 0, 0},
		{"tool.sh", "", "", provider.ChangeModified, 0, 0},
		{"moved.sh", "was.sh", "", provider.ChangeRenamed, 0, 0},
		{"moved.png", "was.png", "", provider.ChangeRenamed, 0, 0},
		{"same.go", "", "", provider.ChangeModified, 0, 0},
		{"big2.txt", "", "@@ -1 +1 @@\n-a\n+b\n", provider.ChangeModified, 1, 1},
	}
	if !slices.Equal(got, want) {
		t.Errorf("files =\n%+v\nwant\n%+v", got, want)
	}
	if d.Notes != nil {
		t.Errorf("notes = %q", d.Notes)
	}
}

// TestDiffPageCap: a listing that goes on past diffsPageCap pages stops
// there with the fixed note.
func TestDiffPageCap(t *testing.T) {
	f := newFake(t)
	var n atomic.Int32
	f.handle(http.MethodGet, fakeMR+"/diffs", func(w http.ResponseWriter, r *http.Request) {
		i := n.Add(1)
		w.Header().Set("Link", fmt.Sprintf(`<http://%s%s%s/diffs?per_page=100&page=%d>; rel="next"`, r.Host, fakeAPI, fakeMR, i+1))
		writeJSON(w, []any{diffEntry(fmt.Sprintf("f%03d.txt", i), "@@ -1 +1 @@\n-a\n+b\n")})
	})
	p, _ := f.provider(f.config(), time.Now())
	p.maxFiles = 0
	d, err := p.GetDiff(t.Context(), testRef(), &provider.PullRequest{BaseSHA: testBase, HeadSHA: testHead}, provider.DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Files) != diffsPageCap || !slices.Equal(d.Notes, []string{NoteDiffPageCap}) || !d.Incomplete || n.Load() != diffsPageCap {
		t.Errorf("%d files, %d pages, notes %q, incomplete %v", len(d.Files), n.Load(), d.Notes, d.Incomplete)
	}
	if want := strconv.Itoa(diffsPageCap * perPage); !strings.Contains(NoteDiffPageCap, want) {
		t.Errorf("the note does not name the cap %s", want)
	}
}

// TestDiffUnsupportedVersion: /diffs answering 404 while the merge request
// can be read means a GitLab older than 16.0; when the merge request is
// missing too, it stays not_found.
func TestDiffUnsupportedVersion(t *testing.T) {
	for _, mrThere := range []bool{true, false} {
		t.Run("merge_request_"+strconv.FormatBool(mrThere), func(t *testing.T) {
			f := newFake(t)
			f.handle(http.MethodGet, fakeMR+"/diffs", func(w http.ResponseWriter, _ *http.Request) { fakeError(w, 404) })
			f.handle(http.MethodGet, fakeMR, func(w http.ResponseWriter, _ *http.Request) {
				if mrThere {
					writeJSON(w, mrJSON(true))
					return
				}
				fakeError(w, 404)
			})
			p, _ := f.provider(f.config(), time.Now())
			_, err := p.GetDiff(t.Context(), testRef(), &provider.PullRequest{BaseSHA: testBase, HeadSHA: testHead}, provider.DiffOptions{})
			if mrThere {
				want := "the server version is not supported: GitLab 16.0 or newer is required"
				if !errors.Is(err, provider.ErrUnsupportedVersion) || err.Error() != want {
					t.Errorf("err = %v, want %q", err, want)
				}
				return
			}
			if !errors.Is(err, provider.ErrNotFound) {
				t.Errorf("err = %v, want not_found", err)
			}
		})
	}
}

// TestFileRequests: a file path is one escaped segment of the raw request,
// the revision a query value; the tree is asked for with keyset pagination
// and a submodule is not a directory.
func TestFileRequests(t *testing.T) {
	f := newFake(t)
	f.handle(http.MethodGet, fakeProject+"/repository/files/src%2Fa%20b%2Fc+d.go/raw", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ref") != testBase {
			t.Errorf("ref = %q", r.URL.Query().Get("ref"))
		}
		if got := r.Header.Get("Accept"); got != "*/*" {
			t.Errorf("raw file request: Accept %q, want */*", got)
		}
		_, _ = w.Write([]byte("package c\n"))
	})
	f.json(fakeProject+"/repository/tree", []any{
		map[string]any{"name": "lib", "type": "tree"},
		map[string]any{"name": "vendor-mod", "type": "commit"},
		map[string]any{"name": "a.go", "type": "blob"},
	})
	p, _ := f.provider(f.config(), time.Now())
	got, err := p.GetFileAt(t.Context(), testRef(), "src/a b/c+d.go", testBase)
	if err != nil || string(got) != "package c\n" {
		t.Fatalf("GetFileAt = %q, %v", got, err)
	}
	l, err := p.ListDir(t.Context(), testRef(), "src/a b", testBase)
	if err != nil {
		t.Fatal(err)
	}
	want := []provider.DirEntry{{Name: "a.go"}, {Name: "lib", Dir: true}, {Name: "vendor-mod"}}
	if !slices.Equal(l.Entries, want) || l.Truncated {
		t.Errorf("ListDir = %+v", l)
	}
	reqs := f.requests()
	if len(reqs) != 2 || reqs[1] != fakeAPI+fakeProject+"/repository/tree?ref="+testBase+"&per_page=100&pagination=keyset&path=src%2Fa+b" {
		t.Errorf("requests = %q", reqs)
	}
}

func TestFileLineURL(t *testing.T) {
	cfg := config.Defaults()
	cfg.GitLab.BaseURL = "https://example.com/gitlab"
	cfg.Secrets.GitLabToken = config.NewSecret(testToken)
	pp, err := NewFactory().New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	p := pp.(*Provider)
	pr := &provider.PullRequest{HeadSHA: testHead}
	got := p.FileLineURL(testRef(), pr, "src/a b#c.go", 12)
	want := "https://example.com/gitlab/group/sub/project/-/blob/" + testHead + "/src/a%20b%23c.go#L12"
	if got != want {
		t.Errorf("FileLineURL = %q, want %q", got, want)
	}
	if got := p.FileLineURL(testRef(), &provider.PullRequest{}, "a.go", 1); got != "" {
		t.Errorf("without a head: %q", got)
	}
}

// TestInvalidReferenceSendsNothing: a namespace or project that cannot be
// a GitLab path is refused before any request.
func TestInvalidReferenceSendsNothing(t *testing.T) {
	f := newFake(t)
	p, _ := f.provider(f.config(), time.Now())
	for _, ref := range []provider.PRRef{
		{Namespace: "", Repo: "project", Number: 7},
		{Namespace: "group//sub", Repo: "project", Number: 7},
		{Namespace: "group/..", Repo: "project", Number: 7},
		{Namespace: "group", Repo: "a/b", Number: 7},
		{Namespace: "group", Repo: "project", Number: 0},
	} {
		if _, err := p.GetPullRequest(t.Context(), ref); !errors.Is(err, provider.ErrProtocol) {
			t.Errorf("GetPullRequest(%+v) = %v, want protocol", ref, err)
		}
	}
	if r := f.requests(); len(r) != 0 {
		t.Errorf("requests sent: %q", r)
	}
}
