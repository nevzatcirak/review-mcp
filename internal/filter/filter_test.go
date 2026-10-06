package filter

import (
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/filter/data"
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
		{"a.approved", false, ReasonIgnoreGlob},
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

func TestGlobWithoutSlashMatchesAnyDepth(t *testing.T) {
	f, err := New(cfgWith([]string{"*.golden", "*.foo"}, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"x.golden", "a/x.golden", "a/b/x.golden", "a/b/c.foo"} {
		ok, reason := f.Explain(p)
		if ok || reason != ReasonIgnoreGlob {
			t.Errorf("Explain(%q) = %v, %q; want excluded with %q", p, ok, reason, ReasonIgnoreGlob)
		}
	}
	for _, p := range []string{"x.golden.bak", "golden/x.txt", "a.golden/x.go"} {
		if !f.Include(p) {
			t.Errorf("%q should be included", p)
		}
	}
}

func TestGlobWithSlashIsRootAnchored(t *testing.T) {
	f, err := New(cfgWith([]string{"docs/*.md", "vendor/**", "**/gen/*.go", "src/*.tmp"}, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	excluded := []string{"docs/a.md", "vendor/a/b.go", "gen/a.go", "x/y/gen/a.go", "src/a.tmp"}
	for _, p := range excluded {
		if f.Include(p) {
			t.Errorf("%q should be excluded", p)
		}
	}
	included := []string{"x/docs/a.md", "docs/sub/a.md", "src/vendor/a.go", "gen/sub/a.go", "src/deep/a.tmp"}
	for _, p := range included {
		if !f.Include(p) {
			t.Errorf("%q should be included", p)
		}
	}
}

// TestGeneratedGlobsContainSlash pins the precondition for one glob
// semantics: every generated-code glob contains "/", so the slash-free
// basename rule never changes that table.
func TestGeneratedGlobsContainSlash(t *testing.T) {
	for name, globs := range data.GeneratedCode {
		for _, g := range globs {
			if !strings.Contains(g, "/") {
				t.Errorf("framework %q: glob %q has no '/'", name, g)
			}
		}
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
