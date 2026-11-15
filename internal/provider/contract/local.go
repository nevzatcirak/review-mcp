package contract

import (
	"context"
	"errors"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// LocalFixture opens the read-only source of a local review (X-52,
// internal/provider/local) on a synthetic repository that has at least one
// commit after the merge base. gitCalls returns the number of git processes
// the source has started so far.
type LocalFixture interface {
	ServeLocal(t *testing.T) (p provider.Provider, ref provider.PRRef, gitCalls func() int64)
}

// RunLocal is the contract suite's `local` run (spec V-20 §1.3): the local
// source is a provider of kind local whose capabilities are all off, which
// implements none of the optional write or host interfaces, whose reads
// work, and whose every write method returns ClassUnsupported without
// starting git.
func RunLocal(t *testing.T, f LocalFixture) {
	t.Helper()
	t.Run("kind_and_capabilities", func(t *testing.T) {
		p, ref, _ := f.ServeLocal(t)
		if p.Kind() != provider.KindLocal || ref.Kind != provider.KindLocal {
			t.Errorf("Kind %q, ref kind %q; want local", p.Kind(), ref.Kind)
		}
		if p.Capabilities() != (provider.Capabilities{}) {
			t.Errorf("Capabilities %+v; want everything off", p.Capabilities())
		}
		if ref.URL != "" || ref.Number != 0 {
			t.Errorf("the reference has a URL or a number: %+v", ref)
		}
		if u := p.FileLineURL(ref, nil, "README.md", 1); u != "" {
			t.Errorf("FileLineURL = %q; want none", u)
		}
	})
	t.Run("no_optional_interfaces", func(t *testing.T) {
		p, _, _ := f.ServeLocal(t)
		if _, ok := p.(provider.DecisionWriter); ok {
			t.Error("the local source implements DecisionWriter")
		}
		if _, ok := p.(provider.IssueReader); ok {
			t.Error("the local source implements IssueReader")
		}
		if _, ok := p.(provider.PRLister); ok {
			t.Error("the local source implements PRLister")
		}
	})
	t.Run("reads", func(t *testing.T) {
		p, ref, _ := f.ServeLocal(t)
		ctx := t.Context()
		pr, err := p.GetPullRequest(ctx, ref)
		if err != nil || pr == nil {
			t.Fatalf("GetPullRequest = %v, %v", pr, err)
		}
		if pr.HeadSHA == "" || pr.BaseSHA == "" || pr.BaseStrategy != provider.BaseLocalMergeBase || pr.Title == "" {
			t.Errorf("GetPullRequest = %+v", pr)
		}
		if pr.Author != "" || pr.AuthorID != "" || pr.WebURL != "" {
			t.Errorf("the local pull request has an author or a URL: %+v", pr)
		}
		if msgs, err := p.GetCommitMessages(ctx, ref); err != nil || len(msgs) == 0 {
			t.Errorf("GetCommitMessages = %v, %v", msgs, err)
		}
		d, err := p.GetDiff(ctx, ref, pr, provider.DiffOptions{})
		if err != nil || len(d.Files) == 0 || d.BaseStrategy != provider.BaseLocalMergeBase {
			t.Fatalf("GetDiff = %+v, %v", d, err)
		}
		if _, err := p.GetFileAt(ctx, ref, d.Files[0].Path, pr.BaseSHA); err != nil && !errors.Is(err, provider.ErrNotFound) {
			t.Errorf("GetFileAt = %v", err)
		}
		if _, err := p.ListDir(ctx, ref, "", pr.BaseSHA); err != nil {
			t.Errorf("ListDir = %v", err)
		}
		if th, err := p.ListThreads(ctx, ref); err != nil || len(th) != 0 {
			t.Errorf("ListThreads = %v, %v; want none", th, err)
		}
	})
	t.Run("writes_unsupported", func(t *testing.T) {
		p, ref, calls := f.ServeLocal(t)
		ctx := t.Context()
		pr, err := p.GetPullRequest(ctx, ref)
		if err != nil {
			t.Fatal(err)
		}
		body := "a comment"
		writes := []struct {
			name string
			call func(context.Context) (any, error)
		}{
			{"PostComment", func(ctx context.Context) (any, error) { return p.PostComment(ctx, ref, body) }},
			{"ReplyToComment", func(ctx context.Context) (any, error) { return p.ReplyToComment(ctx, ref, "1", body) }},
			{"EditComment", func(ctx context.Context) (any, error) { return nil, p.EditComment(ctx, ref, "1", body) }},
			{"PostInlineComments", func(ctx context.Context) (any, error) {
				return p.PostInlineComments(ctx, ref, pr, []provider.InlineComment{{Path: "README.md", Line: 1, Body: body}})
			}},
			{"UpdatePullRequest", func(ctx context.Context) (any, error) {
				return nil, p.UpdatePullRequest(ctx, ref, provider.UpdatePR{Title: &body})
			}},
			{"CurrentUser", func(ctx context.Context) (any, error) {
				u, err := p.CurrentUser(ctx)
				if u != (provider.User{}) {
					return u, err
				}
				return nil, err
			}},
		}
		for _, w := range writes {
			before := calls()
			got, err := w.call(ctx)
			if !errors.Is(err, provider.ErrUnsupported) {
				t.Errorf("%s = %v; want an unsupported error", w.name, err)
			}
			if got != nil && !isNilPointer(got) {
				t.Errorf("%s returned a result: %+v", w.name, got)
			}
			if n := calls() - before; n != 0 {
				t.Errorf("%s started %d git processes; want none", w.name, n)
			}
		}
	})
}

// isNilPointer reports whether v is a typed nil pointer or slice.
func isNilPointer(v any) bool {
	switch x := v.(type) {
	case *provider.Comment:
		return x == nil
	case *provider.ReplyResult:
		return x == nil
	case []provider.InlineResult:
		return x == nil
	}
	return false
}
