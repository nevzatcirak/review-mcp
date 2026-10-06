package filter

import (
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
)

func cfgWith(globs, regexes, frameworks []string) *config.Config {
	c := &config.Config{}
	c.Ignore.Glob = globs
	c.Ignore.Regex = regexes
	c.Diff.IgnoreGeneratedFrameworks = frameworks
	return c
}

func TestExplain(t *testing.T) {
	f, err := New(cfgWith(
		[]string{"vendor/**", "**/*.approved"},
		[]string{`docs/old`, `.*_test\.go`},
		[]string{"protobuf", "go_gen"},
	))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		path   string
		want   bool
		reason string
	}{
		{"src/main.go", true, ""},
		{"README.md", true, ""},
		{"package-lock.json", false, ReasonLockfileOrMinified},
		{"web/yarn.lock", false, ReasonLockfileOrMinified},
		{"a/b/.terraform.lock.hcl", false, ReasonLockfileOrMinified},
		{"a/bun.lockb", false, ReasonLockfileOrMinified},
		{"Package-Lock.json", true, ""}, // lockfile names are exact
		{"static/app.min.js", false, ReasonLockfileOrMinified},
		{"static/APP.MIN.JS", false, ReasonLockfileOrMinified},
		{"static/app.css.map", false, ReasonLockfileOrMinified},
		{"static/app.ts.map", false, ReasonLockfileOrMinified},
		{"static/app.js", true, ""},
		{"img/logo.png", false, ReasonBadExtension},
		{"img/LOGO.PNG", false, ReasonBadExtension},
		{".gitignore", false, ReasonBadExtension},
		{"dir.png/Makefile", true, ""}, // extension comes from the basename
		{"png", true, ""},              // no dot, no extension
		{"trailing.", true, ""},
		{"api/x.pb.go", false, "generated:protobuf"},
		{"x.pb.go", false, "generated:protobuf"},
		{"a/zz_gen.go", false, "generated:go_gen"},
		{"vendor/lib/a.go", false, ReasonIgnoreGlob},
		{"src/vendor/a.go", true, ""}, // vendor/** is anchored at the root
		{"src/deep/a.approved", false, ReasonIgnoreGlob},
		{"docs/old/readme.txt", false, ReasonIgnoreRegex},
		{"docs/older.txt", false, ReasonIgnoreRegex}, // match, not full match
		{"src/docs/old/readme.txt", true, ""},        // anchored at the start
		{"pkg/a_test.go", false, ReasonIgnoreRegex},
		{"", false, ReasonEmptyPath},
		// order: lockfile beats bad extension beats generated beats glob beats regex
		{"vendor/x.pb.go", false, "generated:protobuf"},
		{"vendor/img.png", false, ReasonBadExtension},
		{"vendor/yarn.lock", false, ReasonLockfileOrMinified},
		{"vendor/a_test.go", false, ReasonIgnoreGlob},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got, reason := f.Explain(tt.path)
			if got != tt.want || reason != tt.reason {
				t.Errorf("Explain(%q) = %v, %q; want %v, %q", tt.path, got, reason, tt.want, tt.reason)
			}
			if f.Include(tt.path) != tt.want {
				t.Errorf("Include(%q) disagrees with Explain", tt.path)
			}
		})
	}
}

// TestGlobRootVariant checks the root-variant behaviour of upstream: a glob
// starting with "**/" also matches a file at the repository root.
func TestGlobRootVariant(t *testing.T) {
	f, err := New(cfgWith([]string{"**/x.pb.go"}, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"x.pb.go", "a/x.pb.go", "a/b/x.pb.go"} {
		if f.Include(p) {
			t.Errorf("%q should be excluded by **/x.pb.go", p)
		}
	}
	for _, p := range []string{"xx.pb.go", "a/b/x.pb.go.bak"} {
		if !f.Include(p) {
			t.Errorf("%q should be included", p)
		}
	}
}

func TestGlobIsPathSeparatorAware(t *testing.T) {
	f, err := New(cfgWith([]string{"*.foo", "src/*.tmp"}, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	if f.Include("a.foo") {
		t.Error("a.foo should be excluded")
	}
	if !f.Include("dir/a.foo") {
		t.Error("* must not cross a path separator")
	}
	if !f.Include("src/deep/a.tmp") {
		t.Error("src/*.tmp must not match nested paths")
	}
}

func TestNewErrors(t *testing.T) {
	tests := []struct {
		name string
		cfg  *config.Config
		want string
	}{
		{"nil", nil, "nil config"},
		{"bad glob", cfgWith([]string{"ok", "a/[b"}, nil, nil), "ignore.glob[1]"},
		{"bad regex", cfgWith(nil, []string{"(unclosed"}, nil), "ignore.regex[0]"},
		{"unknown framework", cfgWith(nil, nil, []string{"protobuf", "nope"}), "diff.ignore_generated_frameworks[1]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := New(tt.cfg)
			if err == nil || f != nil {
				t.Fatalf("want error, got filter=%v err=%v", f, err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q lacks %q", err, tt.want)
			}
			if strings.Contains(err.Error(), "a/[b") || strings.Contains(err.Error(), "unclosed") {
				t.Errorf("error echoes the raw pattern: %q", err)
			}
		})
	}
}

func TestUnknownFrameworkListsValidNames(t *testing.T) {
	_, err := New(cfgWith(nil, nil, []string{"nope"}))
	if err == nil || !strings.Contains(err.Error(), "go_gen, graphql, grpc_csharp") {
		t.Fatalf("error should list sorted valid names, got %v", err)
	}
}
