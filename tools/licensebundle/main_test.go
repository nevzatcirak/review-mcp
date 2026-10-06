package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeModule creates a module directory with the given files and returns the
// module value pointing at it.
func fakeModule(t *testing.T, path, version string, files map[string]string) module {
	t.Helper()
	dir := t.TempDir()
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return module{Path: path, Version: version, Dir: dir}
}

func TestParseAllowlist(t *testing.T) {
	in := "# comment\n\nexample.com/a MIT\nexample.com/b   Apache-2.0 AND MIT  \n"
	got, err := parseAllowlist(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["example.com/a"] != "MIT" || got["example.com/b"] != "Apache-2.0 AND MIT" {
		t.Fatalf("unexpected allowlist: %v", got)
	}
}

func TestParseAllowlistErrors(t *testing.T) {
	cases := map[string]string{
		"missing spdx": "example.com/a\n",
		"duplicate":    "example.com/a MIT\nexample.com/a MIT\n",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseAllowlist(strings.NewReader(in)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestCheckSPDX(t *testing.T) {
	ok := []string{"MIT", "BSD-2-Clause", "BSD-3-Clause", "Apache-2.0", "ISC", "MIT AND Apache-2.0"}
	for _, s := range ok {
		if err := checkSPDX(s); err != nil {
			t.Errorf("%q: unexpected error %v", s, err)
		}
	}
	bad := []string{"GPL-3.0-only", "MPL-2.0", "mit", "MIT AND GPL-2.0-only", "MIT OR Apache-2.0", ""}
	for _, s := range bad {
		if err := checkSPDX(s); err == nil {
			t.Errorf("%q: expected an error", s)
		}
	}
}

func TestParseGoList(t *testing.T) {
	out := "example.com/b\tv1.0.0\t/m/b\nexample.com/a\tv2.0.0\t/m/a\nexample.com/b\tv1.0.0\t/m/b\n\n"
	mods, err := parseGoList(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(mods) != 2 || mods[0].Path != "example.com/a" || mods[1].Path != "example.com/b" {
		t.Fatalf("want deduplicated modules sorted by path, got %+v", mods)
	}
	if _, err := parseGoList("example.com/a\tv1\n"); err == nil {
		t.Fatal("expected an error for a malformed line")
	}
	if _, err := parseGoList("example.com/a\tv1\t/x\nexample.com/a\tv2\t/y\n"); err == nil {
		t.Fatal("expected an error for conflicting data")
	}
}

func TestBuildSortsAndFormats(t *testing.T) {
	zeta := fakeModule(t, "example.com/zeta", "v1.2.3", map[string]string{
		"LICENSE": "Zeta license text\r\n\r\n",
		"NOTICE":  "Zeta notice text\n",
		"README":  "not a license",
	})
	alpha := fakeModule(t, "example.com/alpha", "v0.1.0", map[string]string{
		"COPYING.md": "Alpha copying text\n",
		"LICENCE":    "Alpha licence text\n",
	})
	allow := map[string]string{"example.com/zeta": "MIT", "example.com/alpha": "ISC"}

	got, err := build([]module{zeta, alpha}, allow)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Index(got, "Module:  example.com/alpha") > strings.Index(got, "Module:  example.com/zeta") {
		t.Error("modules must be sorted by path")
	}
	for _, want := range []string{
		"Modules (2):\n  example.com/alpha v0.1.0 (ISC)\n  example.com/zeta v1.2.3 (MIT)\n",
		"Module:  example.com/zeta\nVersion: v1.2.3\nLicense: MIT\n",
		"--- LICENSE ---\n\nZeta license text\n",
		"--- NOTICE ---\n\nZeta notice text\n",
		"--- COPYING.md ---\n\nAlpha copying text\n",
		"--- LICENCE ---\n\nAlpha licence text\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q", want)
		}
	}
	if strings.Contains(got, "not a license") || strings.Contains(got, "\r") {
		t.Error("README must be excluded and line endings normalized")
	}
	again, _ := build([]module{alpha, zeta}, allow)
	if again != got {
		t.Error("output must not depend on input order")
	}
}

func TestBuildFailures(t *testing.T) {
	good := fakeModule(t, "example.com/good", "v1.0.0", map[string]string{"LICENSE": "x\n"})
	noLicense := fakeModule(t, "example.com/nolicense", "v1.0.0", map[string]string{"README": "x\n"})
	noticeOnly := fakeModule(t, "example.com/noticeonly", "v1.0.0", map[string]string{"NOTICE": "x\n"})

	cases := []struct {
		name  string
		mods  []module
		allow map[string]string
		want  string
	}{
		{"no allowlist entry", []module{good}, map[string]string{}, "has no allowlist entry"},
		{"missing license file", []module{noLicense}, map[string]string{"example.com/nolicense": "MIT"}, "has no LICENSE"},
		{"notice is not a license", []module{noticeOnly}, map[string]string{"example.com/noticeonly": "MIT"}, "has no LICENSE"},
		{"spdx outside the set", []module{good}, map[string]string{"example.com/good": "GPL-3.0-only"}, "outside the permitted set"},
		{"stale allowlist entry", []module{good}, map[string]string{"example.com/good": "MIT", "example.com/fake": "MIT"}, "example.com/fake is not linked"},
		{"no source directory", []module{{Path: "example.com/x", Version: "v1"}}, map[string]string{"example.com/x": "MIT"}, "no source directory"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := build(tc.mods, tc.allow)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want an error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestBuildReportsAllViolations(t *testing.T) {
	a := fakeModule(t, "example.com/a", "v1", map[string]string{"README": "x"})
	b := fakeModule(t, "example.com/b", "v1", map[string]string{"LICENSE": "x"})
	_, err := build([]module{a, b}, map[string]string{"example.com/a": "MIT"})
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"example.com/a has no LICENSE", "example.com/b v1 is linked but has no allowlist entry"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
}
