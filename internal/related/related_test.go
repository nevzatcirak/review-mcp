package related

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

func TestNormalize(t *testing.T) {
	ok := map[string]string{
		"https://git.example/o/r/pulls/7":                "https://git.example/o/r/pulls/7",
		" https://git.example/o/r/pulls/7?x=1#frag ":     "https://git.example/o/r/pulls/7",
		"HTTP://gitlab.example/g/p/-/merge_requests/3?":  "http://gitlab.example/g/p/-/merge_requests/3",
		"https://git.example:8443/o/r/pull/7/files#diff": "https://git.example:8443/o/r/pull/7/files",
	}
	for in, want := range ok {
		if got, good := Normalize(in); !good || got != want {
			t.Errorf("Normalize(%q) = %q, %v; want %q", in, got, good, want)
		}
	}
	for _, in := range []string{"", "   ", "git.example/o/r/pulls/7", "ftp://git.example/x", "https://user:pw@git.example/x",
		"https://", "mailto:x@git.example", "https://git.example/" + strings.Repeat("a", MaxURLBytes)} {
		if got, good := Normalize(in); good {
			t.Errorf("Normalize(%q) = %q, want refused", in, got)
		}
	}
}

func TestValidate(t *testing.T) {
	marker := "VALUE-NEVER-REPEATED"
	quoted := "https://user:" + marker + "@git.example/o/r/pulls/1"
	cases := []struct {
		name     string
		items    []string
		combined bool
		want     string
	}{
		{"five", []string{"https://a.example/1", "https://a.example/2", "https://a.example/3", "https://a.example/4", "https://a.example/5"}, false,
			"related must have at most 4 items"},
		{"credentials", []string{"https://git.example/o/r/pulls/1", quoted}, false,
			"related[1] must be an http or https pull request URL without credentials"},
		{"empty item", []string{""}, false, "related[0] must be an http or https pull request URL without credentials"},
		{"with focus or findings", []string{"https://git.example/o/r/pulls/1"}, true,
			"related cannot be combined with focus or findings"},
		{"count before form", []string{"x", "x", "x", "x", "x"}, true, "related must have at most 4 items"},
		{"form before combination", []string{"x"}, true, "related[0] must be an http or https pull request URL without credentials"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Validate(c.items, c.combined)
			var ae *ArgumentError
			if !errors.As(err, &ae) || err.Error() != c.want {
				t.Fatalf("error %v, want %q", err, c.want)
			}
			if strings.Contains(err.Error(), "VALUE-NEVER-REPEATED") {
				t.Error("the error repeats a value")
			}
		})
	}
	for _, items := range [][]string{nil, {}} {
		if got, err := Validate(items, true); got != nil || err != nil {
			t.Errorf("Validate(%v) = %v, %v; want not given", items, got, err)
		}
	}
	got, err := Validate([]string{"https://git.example/o/r/pulls/2?tab=files", "https://gitlab.example/g/p/-/merge_requests/3"}, false)
	if err != nil || !slices.Equal(got, []string{"https://git.example/o/r/pulls/2", "https://gitlab.example/g/p/-/merge_requests/3"}) {
		t.Errorf("Validate = %q, %v", got, err)
	}
}

// fakeProvider serves one pull request, or fails with err.
type fakeProvider struct {
	provider.Provider
	kind  provider.Kind
	err   error
	reads []string
}

func (f *fakeProvider) GetPullRequest(_ context.Context, ref provider.PRRef) (*provider.PullRequest, error) {
	f.reads = append(f.reads, "pr "+strconv.FormatInt(ref.Number, 10))
	if f.err != nil {
		return nil, f.err
	}
	return &provider.PullRequest{Title: "TITLE-MARKER-related", HeadSHA: "abc", TargetBranch: "main", State: "open"}, nil
}

func (f *fakeProvider) GetDiff(_ context.Context, ref provider.PRRef, _ *provider.PullRequest, opts provider.DiffOptions) (*provider.Diff, error) {
	f.reads = append(f.reads, "diff "+strconv.FormatInt(ref.Number, 10))
	d := &provider.Diff{}
	for _, p := range []string{"api/openapi.yaml", "vendor/x.go"} {
		if opts.Include != nil && !opts.Include(p) {
			d.Skipped = append(d.Skipped, provider.SkippedFile{Path: p, Reason: provider.SkipFiltered})
			continue
		}
		d.Files = append(d.Files, provider.FilePatch{Path: p, Patch: "@@ -0,0 +1 @@\n+DIFF-MARKER\n"})
	}
	return d, nil
}

// fakeResolver resolves "https://<host>/<kind>/<ns>/<repo>/<n>" to the
// provider of host; an unknown host is url_not_configured; a host in errs
// fails resolution with that error.
type fakeResolver struct {
	providers map[string]*fakeProvider
	errs      map[string]error
	calls     []string
}

func (r *fakeResolver) Resolve(raw string) (provider.PRRef, provider.Provider, error) {
	r.calls = append(r.calls, raw)
	rest := strings.TrimPrefix(raw, "https://")
	parts := strings.Split(rest, "/")
	host := parts[0]
	if err := r.errs[host]; err != nil {
		return provider.PRRef{}, nil, err
	}
	p := r.providers[host]
	if p == nil || len(parts) != 5 {
		return provider.PRRef{}, nil, &provider.Error{Class: provider.ClassURLNotConfigured}
	}
	n, _ := strconv.ParseInt(parts[4], 10, 64)
	return provider.PRRef{Kind: p.kind, Namespace: parts[2], Repo: parts[3], Number: n, URL: raw}, p, nil
}

// credentialError is tools.RequestError's shape (internal/related cannot
// import internal/tools).
type credentialError struct{}

func (credentialError) Error() string      { return "no token in this request" }
func (credentialError) ErrorClass() string { return "credentials_missing" }

func TestRead(t *testing.T) {
	gitea := &fakeProvider{kind: provider.KindGitea}
	gitlab := &fakeProvider{kind: provider.KindGitLab}
	r := &fakeResolver{providers: map[string]*fakeProvider{
		"git.example": gitea, "gitlab.example": gitlab,
		"forbidden.example": {kind: provider.KindGitHub, err: &provider.Error{Class: provider.ClassAuth, Status: 403}},
		"unauth.example":    {kind: provider.KindGitHub, err: &provider.Error{Class: provider.ClassAuth, Status: 401}},
		"missing.example":   {kind: provider.KindGitHub, err: &provider.Error{Class: provider.ClassNotFound, Status: 404}},
		"broken.example":    {kind: provider.KindGitHub, err: &provider.Error{Class: provider.ClassUpstream, Status: 500}},
		"other.example":     {kind: provider.KindGitHub, err: errors.New("x")},
	}, errs: map[string]error{
		"serve.example":     credentialError{},
		"malformed.example": &provider.Error{Class: provider.ClassURLMalformed},
	}}
	primary := provider.PRRef{Kind: provider.KindGitea, Namespace: "o", Repo: "r", Number: 7}
	urls := []string{
		"https://gitlab.example/gitlab/g/p/3",     // read
		"https://git.example/gitea/o/r/7",         // the primary by its reference
		"https://git.example/gitea/O/R/7",         // ... and by another URL form
		"https://gitlab.example/gitlab/g/p/3",     // a duplicate URL
		"https://unknown.example/x/y/z/1",         // url_not_configured
		"https://serve.example/x/y/z/1",           // credentials_missing
		"https://malformed.example/x/y/z/1",       // url_malformed
		"https://forbidden.example/github/o/r/1",  // forbidden
		"https://unauth.example/github/o/r/2",     // unauthorized
		"https://missing.example/github/o/r/3",    // not_found
		"https://broken.example/github/o/r/4",     // unreadable
		"https://other.example/github/o/r/5",      // unreadable
		"https://git.example/gitea/o/r/8",         // read: another pull request on the primary's provider
		"https://git.example/gitea/o/r/8",         // duplicate
		"https://git.example/gitea/primary-url/7", // the primary by its URL
	}
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	items := Read(context.Background(), r, "https://git.example/gitea/primary-url/7", primary, urls,
		func(p string) bool { return !strings.HasPrefix(p, "vendor/") }, false, log)

	want := []struct{ status, reason string }{
		{"", ""}, {StatusIgnored, ReasonPrimary}, {StatusIgnored, ReasonPrimary}, {StatusIgnored, ReasonDuplicate},
		{StatusFailed, ReasonURLNotConfigured}, {StatusFailed, ReasonCredentialsMissing}, {StatusFailed, ReasonURLMalformed},
		{StatusFailed, ReasonForbidden}, {StatusFailed, ReasonUnauthorized}, {StatusFailed, ReasonNotFound},
		{StatusFailed, ReasonUnreadable}, {StatusFailed, ReasonUnreadable}, {"", ""}, {StatusIgnored, ReasonDuplicate},
		{StatusIgnored, ReasonPrimary},
	}
	if len(items) != len(urls) {
		t.Fatalf("%d items for %d URLs", len(items), len(urls))
	}
	for i, w := range want {
		if items[i].Status != w.status || items[i].Reason != w.reason || items[i].URL != urls[i] {
			t.Errorf("item %d: %+v, want %q %q", i, items[i], w.status, w.reason)
		}
	}
	read := ReadItems(items)
	if len(read) != 2 || read[0] != items[0] || read[1] != items[12] || read[0].Ref.Kind != provider.KindGitLab ||
		len(read[0].Diff.Files) != 1 || read[0].Diff.Files[0].Path != "api/openapi.yaml" || len(read[0].Diff.Skipped) != 1 {
		t.Errorf("read items %+v", read)
	}
	if !slices.Equal(gitlab.reads, []string{"pr 3", "diff 3"}) || !slices.Equal(gitea.reads, []string{"pr 8", "diff 8"}) {
		t.Errorf("reads: gitlab %q, gitea %q", gitlab.reads, gitea.reads)
	}
	for _, leak := range []string{"gitlab/g/p", "TITLE-MARKER", "DIFF-MARKER", "unknown.example", "/o/r/"} {
		if strings.Contains(logs.String(), leak) {
			t.Errorf("the logs carry %q", leak)
		}
	}
	if !strings.Contains(logs.String(), "reason=url_not_configured") || !strings.Contains(logs.String(), "kind=gitlab") {
		t.Errorf("the logs lack the outcome:\n%s", logs.String())
	}
}

// TestReadOff: with related.max_tokens 0 nothing is resolved or read; the
// items are not_placed with reason off, the primary and duplicates ignored.
func TestReadOff(t *testing.T) {
	gitlab := &fakeProvider{kind: provider.KindGitLab}
	r := &fakeResolver{providers: map[string]*fakeProvider{"gitlab.example": gitlab}}
	items := Read(context.Background(), r, "https://git.example/p/7", provider.PRRef{}, []string{
		"https://gitlab.example/gitlab/g/p/3", "https://git.example/p/7", "https://gitlab.example/gitlab/g/p/3",
	}, nil, true, nil)
	if len(r.calls) != 0 || len(gitlab.reads) != 0 {
		t.Errorf("resolved %q, read %q", r.calls, gitlab.reads)
	}
	for i, w := range [][2]string{{StatusNotPlaced, ReasonOff}, {StatusIgnored, ReasonPrimary}, {StatusIgnored, ReasonDuplicate}} {
		if items[i].Status != w[0] || items[i].Reason != w[1] {
			t.Errorf("item %d: %+v, want %v", i, items[i], w)
		}
	}
}
