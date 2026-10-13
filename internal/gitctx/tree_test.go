package gitctx

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// treeFiles is the commit of the tree-read tests: a guidelines file, nested
// directories, a hostile entry name and a blob above the partial-clone
// limit of TestTreeReadsRunOffline.
func treeFiles() map[string]string {
	return map[string]string{
		".review-mcp/guidelines.md": "# Rules\n\nPut handlers in api/.\n",
		"CONTRIBUTING.md":           "Be kind.\n",
		"api/users.go":              "package api\n",
		"api/v2/orders.go":          "package v2\n",
		"api/evil\nname.go":         "package api\n",
		"big.md":                    strings.Repeat("x", 3000) + "\n",
	}
}

func entryNames(l *provider.DirListing) []string {
	var out []string
	for _, e := range l.Entries {
		n := e.Name
		if e.Dir {
			n += "/"
		}
		out = append(out, n)
	}
	return out
}

// TestTreeReads: HasCommit, FileAt and ListDir read the commit of a cache
// entry; a path that is not a file or a directory there is the provider's
// not_found, a blob over the cap its too_large.
func TestTreeReads(t *testing.T) {
	r, co := localCheckout(t, treeFiles(), "")
	ctx := context.Background()

	if !r.HasCommit(ctx, co, co.HeadSHA) || !r.HasCommit(ctx, co, strings.ToUpper(co.HeadSHA)) {
		t.Error("HasCommit(head) = false")
	}
	for _, rev := range []string{strings.Repeat("ab", 20), "HEAD", "", co.HeadSHA[:12]} {
		if r.HasCommit(ctx, co, rev) {
			t.Errorf("HasCommit(%q) = true", rev)
		}
	}

	got, err := r.FileAt(ctx, co, co.HeadSHA, ".review-mcp/guidelines.md", 1<<20)
	if err != nil || string(got) != "# Rules\n\nPut handlers in api/.\n" {
		t.Errorf("FileAt(nested) = %q, %v", got, err)
	}
	if got, err := r.FileAt(ctx, co, co.HeadSHA, "CONTRIBUTING.md", 1<<20); err != nil || string(got) != "Be kind.\n" {
		t.Errorf("FileAt(root file) = %q, %v", got, err)
	}
	for _, p := range []string{"missing.md", "api", "api/v2", "nope/x.md", "api/users.go/x"} {
		if _, err := r.FileAt(ctx, co, co.HeadSHA, p, 1<<20); !errors.Is(err, provider.ErrNotFound) {
			t.Errorf("FileAt(%q) error = %v, want not_found", p, err)
		}
	}
	if _, err := r.FileAt(ctx, co, co.HeadSHA, "big.md", 1000); !errors.Is(err, provider.ErrTooLarge) {
		t.Errorf("FileAt(big) error = %v, want too_large", err)
	}
	for _, p := range []string{"", "/CONTRIBUTING.md", "a//b", "a/", "a\nb"} {
		if _, err := r.FileAt(ctx, co, co.HeadSHA, p, 1<<20); ReasonOf(err) != ReasonUnsupported {
			t.Errorf("FileAt(%q) error = %v, want unsupported", p, err)
		}
	}

	root, err := r.ListDir(ctx, co, co.HeadSHA, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{".review-mcp/", "CONTRIBUTING.md", "api/", "big.md"}; !slices.Equal(entryNames(root), want) {
		t.Errorf("ListDir(root) = %q, want %q", entryNames(root), want)
	}
	api, err := r.ListDir(ctx, co, co.HeadSHA, "api")
	if err != nil {
		t.Fatal(err)
	}
	// The hostile name comes back as it is stored: cleaning it is the
	// renderer's job.
	if want := []string{"evil\nname.go", "users.go", "v2/"}; !slices.Equal(entryNames(api), want) || api.Truncated {
		t.Errorf("ListDir(api) = %q (truncated %v), want %q", entryNames(api), api.Truncated, want)
	}
	for _, d := range []string{"web", "api/users.go", "CONTRIBUTING.md"} {
		if _, err := r.ListDir(ctx, co, co.HeadSHA, d); !errors.Is(err, provider.ErrNotFound) {
			t.Errorf("ListDir(%q) error = %v, want not_found", d, err)
		}
	}
	if _, err := r.ListDir(ctx, co, strings.Repeat("ab", 20), "api"); !errors.Is(err, provider.ErrNotFound) {
		t.Errorf("ListDir at an unknown commit: error = %v, want not_found", err)
	}
}

// TestTreeReadsRunOffline: a blob the partial clone lacks is a gitctx error
// (the caller then reads through the provider), never fetched: the origin
// is a promisor remote a lazy fetch would reach. The directory listing
// still works, from the tree objects alone.
func TestTreeReadsRunOffline(t *testing.T) {
	r, co := localCheckout(t, treeFiles(), "1k")
	ctx := context.Background()
	_, err := r.FileAt(ctx, co, co.HeadSHA, "big.md", 1<<20)
	var ge *Error
	if !errors.As(err, &ge) {
		t.Fatalf("FileAt(missing blob) error = %v, want a gitctx error", err)
	}
	if got, err := r.FileAt(ctx, co, co.HeadSHA, "CONTRIBUTING.md", 1<<20); err != nil || string(got) != "Be kind.\n" {
		t.Errorf("FileAt(present blob) = %q, %v", got, err)
	}
	if l, err := r.ListDir(ctx, co, co.HeadSHA, ""); err != nil || len(l.Entries) != 4 {
		t.Errorf("ListDir(root) = %v, %v", l, err)
	}
}

// TestTreeReadsRejectUnusableCheckout: a checkout outside the cache is never
// read.
func TestTreeReadsRejectUnusableCheckout(t *testing.T) {
	r, co := localCheckout(t, treeFiles(), "")
	ctx := context.Background()
	other := Checkout{GitDir: t.TempDir(), HeadSHA: co.HeadSHA}
	if r.HasCommit(ctx, other, co.HeadSHA) {
		t.Error("HasCommit outside the cache = true")
	}
	if _, err := r.FileAt(ctx, other, co.HeadSHA, "CONTRIBUTING.md", 1<<20); ReasonOf(err) != ReasonCache {
		t.Errorf("FileAt outside the cache: error = %v, want cache_unusable", err)
	}
	if _, err := r.ListDir(ctx, other, co.HeadSHA, ""); ReasonOf(err) != ReasonCache {
		t.Errorf("ListDir outside the cache: error = %v, want cache_unusable", err)
	}
}
