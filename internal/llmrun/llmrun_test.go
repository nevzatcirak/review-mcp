package llmrun

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

// TestClassValues pins the user-visible class values (X-6). The
// does-not-fit class is shared by pr_review and pr_ask and names the diff,
// not a tool.
func TestClassValues(t *testing.T) {
	for got, want := range map[ErrorClass]string{
		ClassConfigInvalid: "config_invalid",
		ClassDoesNotFit:    "diff_does_not_fit",
	} {
		if string(got) != want {
			t.Errorf("class = %q, want %q", got, want)
		}
	}
}

func TestErrorClasses(t *testing.T) {
	for _, e := range []*Error{ErrConfigInvalid, ErrDoesNotFit} {
		if e.UserMessage() == "" || e.UserMessage() != e.Error() {
			t.Errorf("%s: no fixed sentence", e.Class)
		}
		if !errors.Is(fmt.Errorf("x: %w", &Error{Class: e.Class}), e) {
			t.Errorf("%s: errors.Is by class failed", e.Class)
		}
	}
	if errors.Is(ErrConfigInvalid, ErrFallbackEligible) || !errors.Is(ErrDoesNotFit, ErrFallbackEligible) {
		t.Error("fallback eligibility wrong")
	}
	cause := errors.New("cause")
	if err := DoesNotFit(cause); !errors.Is(err, cause) || !errors.Is(err, ErrDoesNotFit) || err.Error() != DoesNotFitSentence {
		t.Errorf("DoesNotFit = %v", err)
	}
	if ErrDoesNotFit.Unwrap() != nil {
		t.Error("WithCause modified the sentinel")
	}
}

// lineRender renders a prompt whose size follows the diff.
func lineRender(d string) (Rendered, error) {
	return Rendered{System: "sys", User: "user\n" + d}, nil
}

func TestFit(t *testing.T) {
	diff := strings.Repeat("some diff line with words in it\n", 400)
	diff = strings.TrimSuffix(diff, "\n")
	b := tokens.Budget{ContextWindow: 100000, Factor: 0.3}

	f, err := Fit(diff, b, lineRender)
	if err != nil || f.KeptLines != -1 || f.Diff != diff {
		t.Fatalf("no-trim: %+v, %v", f, err)
	}

	b.ContextWindow = f.RequestTokens/2 + b.HardReserve()
	f, err = Fit(diff, b, lineRender)
	if err != nil {
		t.Fatal(err)
	}
	if f.KeptLines <= 0 || !strings.HasSuffix(f.Diff, tokens.TruncationMarker) {
		t.Errorf("not trimmed: kept %d", f.KeptLines)
	}
	if got := tokens.RequestTokens(f.Rendered.System, f.Rendered.User, 0.3) + b.HardReserve(); got > b.ContextWindow {
		t.Errorf("request %d exceeds the window %d", got, b.ContextWindow)
	}

	b.ContextWindow = b.HardReserve() + 5
	if _, err := Fit(diff, b, lineRender); !errors.Is(err, ErrDoesNotFit) || !errors.Is(err, tokens.ErrDoesNotFit) {
		t.Errorf("nothing fits: err = %v", err)
	}
}

type postProvider struct {
	provider.Provider
	err  error
	body string
}

func (p *postProvider) Capabilities() provider.Capabilities { return provider.Capabilities{GFM: true} }
func (p *postProvider) PostComment(_ context.Context, _ provider.PRRef, body string) (*provider.Comment, error) {
	p.body = body
	if p.err != nil {
		return nil, p.err
	}
	return &provider.Comment{ID: "7", URL: "https://your-gitea.example/c/7"}, nil
}

func TestPostResult(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	render := func(caps provider.Capabilities) string { return fmt.Sprintf("gfm=%v", caps.GFM) }

	p := &postProvider{}
	out := &PublishResult{}
	PostResult(context.Background(), log, provider.PRRef{}, p, out, "failed", render)
	if !out.Published || out.CommentID != "7" || p.body != "gfm=true" {
		t.Errorf("out = %+v body %q", out, p.body)
	}

	out = &PublishResult{}
	PostResult(context.Background(), log, provider.PRRef{}, &postProvider{err: errors.New("SECRET-TEXT")}, out, "failed", render)
	if out.Published || out.Error != "failed" {
		t.Errorf("generic failure = %+v", out)
	}

	out = &PublishResult{}
	PostResult(context.Background(), log, provider.PRRef{}, p, out, "failed", nil)
	if out.Published || out.Error != "failed" {
		t.Errorf("no renderer = %+v", out)
	}
}
