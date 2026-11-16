package cases

import (
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

// TestCorpus is the self-check of §1.3 on the committed corpus: every
// case.toml parses, values are known, issue paths exist at head and their
// lines are inside the file, cascade issues sit in unchanged files, clean
// cases have no issue, ids are unique, and the counts of §1.2 hold. It only
// reads the corpus; nothing is built or run (R-1).
func TestCorpus(t *testing.T) {
	corpus := os.DirFS("../corpus")
	cases, err := Load(corpus)
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range CheckCorpus(cases) {
		t.Error(err)
	}
	readme, err := fs.ReadFile(corpus, "README.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range CheckReadme(string(readme), cases) {
		t.Error(err)
	}
	t.Logf("%d cases checked", len(cases))
}

// validCase returns a small case that passes CheckCase: a cascade change in
// src/a.go whose defect is in the unchanged src/b.go.
func validCase() *Case {
	return &Case{
		Dir: "go-cascade-sample", ID: "go-cascade-sample", Language: "go", Category: "cascade",
		Title: "Return early for an empty cart", Description: "Checkout stops before pricing when the cart is empty.",
		Base: map[string][]byte{
			"README.md": []byte("# sample\n"), "go.mod": []byte("module example.com/sample\n"),
			"src/a.go": []byte("package src\n\nfunc A() int { return 1 }\n"),
			"src/b.go": []byte("package src\n\nfunc B() int {\n\treturn A() + 1\n}\n"),
			"src/c.go": []byte("package src\n"),
		},
		Head: map[string][]byte{"src/a.go": []byte("package src\n\nfunc A() int { return 2 }\n")},
		Issues: []Issue{{
			ID: "b-relies-on-a", Severity: "high", Kind: "cascade", Path: "src/b.go", Lines: [2]int{3, 5}, Hops: 1,
			MustMention: []string{"B", "A"}, Summary: "B relies on the old result of A.",
		}},
	}
}

func TestValidCaseHasNoErrors(t *testing.T) {
	c := validCase()
	for _, err := range CheckCase(c, map[string]*Case{c.ID: c}) {
		t.Error(err)
	}
}

// TestCheckCaseRejects proves that each rule catches what it is for. The
// first two rows are the canaries of WP-21a: (c1) a case whose cascade
// issue path is changed by its own head overlay, and (c2) a clean case with
// an issue.
func TestCheckCaseRejects(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(c *Case, all map[string]*Case)
		want   string
	}{
		{"c1 cascade issue path changed by the head overlay", func(c *Case, _ map[string]*Case) {
			c.Head["src/b.go"] = []byte("package src\n\nfunc B() int {\n\treturn A() + 2\n}\n")
		}, "is changed by the case"},
		{"c2 clean case with an issue", func(c *Case, _ map[string]*Case) {
			c.Category = "clean"
		}, "a clean case has 1 issues"},
		{"cascade issue path deleted by the case", func(c *Case, _ map[string]*Case) {
			c.Issues[0].Path = "src/c.go"
			c.Issues[0].Lines = [2]int{1, 1}
			c.Deleted = []string{"src/c.go"}
		}, "does not exist at head"},
		{"lines past the end of the file", func(c *Case, _ map[string]*Case) {
			c.Issues[0].Lines = [2]int{3, 6}
		}, "are not inside src/b.go (5 lines)"},
		{"issue path missing at head", func(c *Case, _ map[string]*Case) {
			c.Issues[0].Path = "src/d.go"
		}, "does not exist at head"},
		{"local issue in an unchanged file", func(c *Case, _ map[string]*Case) {
			c.Category, c.Issues[0].Kind, c.Issues[0].Hops = "local", "local", 0
		}, "must be added or changed by the case"},
		{"unknown category", func(c *Case, _ map[string]*Case) { c.Category = "style" }, `unknown category "style"`},
		{"unknown severity", func(c *Case, _ map[string]*Case) { c.Issues[0].Severity = "critical" }, `unknown severity "critical"`},
		{"title names a defect", func(c *Case, _ map[string]*Case) { c.Title = "Bug: return early" }, "must stay neutral"},
		{"two issues that are not low", func(c *Case, _ map[string]*Case) {
			second := c.Issues[0]
			second.ID = "second"
			c.Issues = append(c.Issues, second)
		}, "want exactly one"},
		{"duplicate issue id", func(c *Case, _ map[string]*Case) {
			second := c.Issues[0]
			second.Severity = "low"
			c.Issues = append(c.Issues, second)
		}, `issue id "b-relies-on-a" is used twice`},
		{"one must_mention entry", func(c *Case, _ map[string]*Case) { c.Issues[0].MustMention = []string{"B"} }, "at least two"},
		{"cascade hops missing", func(c *Case, _ map[string]*Case) { c.Issues[0].Hops = 0 }, "needs hops 1 or 2"},
		{"head file equal to base", func(c *Case, _ map[string]*Case) {
			c.Head["src/c.go"] = c.Base["src/c.go"]
		}, "the overlay holds changed files only"},
		{"base too small", func(c *Case, _ map[string]*Case) { delete(c.Base, "src/c.go") }, "base has 4 files"},
		{"real host", func(c *Case, _ map[string]*Case) {
			c.Base["README.md"] = []byte("# sample\nSee https://git.corp-internal.net/sample.\n")
		}, "use placeholder hosts only"},
		{"marker comment", func(c *Case, _ map[string]*Case) {
			c.Head["src/a.go"] = []byte("package src\n\n// TODO: callers\nfunc A() int { return 2 }\n")
		}, "has a TODO marker"},
		{"twin with another base", func(c *Case, all map[string]*Case) {
			twin := validCase()
			twin.Dir, twin.ID, twin.Category, twin.TwinOf, twin.Issues = "go-clean-sample-twin", "go-clean-sample-twin", "clean", c.ID, nil
			twin.Base["src/c.go"] = []byte("package src\n\n// C is new.\n")
			all[twin.ID] = twin
			*c = *twin
		}, "the base differs from the base of its cascade case"},
		{"id differs from directory", func(c *Case, _ map[string]*Case) { c.ID = "other" }, "differs from the directory name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := validCase()
			all := map[string]*Case{c.ID: validCase()}
			tt.mutate(c, all)
			errs := CheckCase(c, all)
			for _, err := range errs {
				if strings.Contains(err.Error(), tt.want) {
					return
				}
			}
			t.Errorf("CheckCase accepted the case (%s); want an error containing %q, got %v", tt.name, tt.want, errs)
		})
	}
}

func TestCheckCorpusRejectsDuplicateIDs(t *testing.T) {
	a, b := validCase(), validCase()
	b.Dir = "go-cascade-copy"
	for _, err := range CheckCorpus([]*Case{a, b}) {
		if strings.Contains(err.Error(), "is used by two cases") {
			return
		}
	}
	t.Error("CheckCorpus accepted two cases with the same id")
}

func TestCheckReadme(t *testing.T) {
	c := validCase()
	errs := CheckReadme("| id |\n|---|\n| `go-cascade-sample` | go |\n| `gone` | go |\n", []*Case{c})
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "gone") {
		t.Fatalf("CheckReadme = %v, want one error for the unknown row", errs)
	}
	if errs := CheckReadme("", []*Case{c}); len(errs) != 1 {
		t.Fatalf("CheckReadme of an empty README = %v, want one missing row", errs)
	}
}

func TestLoad(t *testing.T) {
	fsys := fstest.MapFS{
		"README.md": {Data: []byte("# corpus\n")},
		"sample/case.toml": {Data: []byte(`id = "sample" # the directory name
language = "go"
category = "local"
title = "Say \"hi\"!"
description = 'literal \n stays'

[[issue]]
id = "one"
severity = "medium"
kind = "local"
path = "a.go"
lines = [1, 2]
must_mention = ["A", "hi",]
summary = "x"
`)},
		"sample/base/a.go":    {Data: []byte("package a\n")},
		"sample/head/a.go":    {Data: []byte("package a\n\nvar x = 1\n")},
		"sample/deleted.txt":  {Data: []byte("b.go\r\n\r\n")},
		"sample/base/b.go":    {Data: []byte("package a\n")},
		"sample/base/sub/c.t": {Data: []byte("c")},
	}
	cases, err := Load(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 1 {
		t.Fatalf("loaded %d cases, want 1", len(cases))
	}
	c := cases[0]
	if c.Title != `Say "hi"!` || c.Description != `literal \n stays` {
		t.Errorf("strings: %q, %q", c.Title, c.Description)
	}
	if is := c.Issues[0]; is.Lines != [2]int{1, 2} || len(is.MustMention) != 2 || is.Path != "a.go" {
		t.Errorf("issue = %+v", is)
	}
	if len(c.Base) != 3 || c.Base["sub/c.t"] == nil || len(c.Head) != 1 {
		t.Errorf("trees: base %d files, head %d", len(c.Base), len(c.Head))
	}
	if len(c.Deleted) != 1 || c.Deleted[0] != "b.go" || c.Changed("a.go") != true {
		t.Errorf("deleted = %q", c.Deleted)
	}
	if _, ok := c.AtHead("b.go"); ok {
		t.Error("a deleted file exists at head")
	}
}

func TestLoadRejects(t *testing.T) {
	for name, toml := range map[string]string{
		"unknown key":       "id = \"s\"\ncolour = \"red\"\n",
		"unknown table":     "[meta]\n",
		"duplicate key":     "id = \"s\"\nid = \"t\"\n",
		"wrong type":        "id = 3\n",
		"unterminated":      "id = \"s\n",
		"mixed array":       "tags = [\"a\", 1]\n",
		"trailing garbage":  "id = \"s\" x\n",
		"lines not a pair":  "[[issue]]\nlines = [1]\n",
		"unsupported value": "id = 1.5\n",
	} {
		fsys := fstest.MapFS{"s/case.toml": {Data: []byte(toml)}}
		if _, err := Load(fsys); err == nil {
			t.Errorf("%s: Load accepted %q", name, toml)
		}
	}
	fsys := fstest.MapFS{"s/case.toml": {Data: []byte("id = \"s\"\n")}, "s/notes.md": {Data: []byte("x")}}
	if _, err := Load(fsys); err == nil {
		t.Error("Load accepted an unexpected file in a case directory")
	}
}

func TestLineCount(t *testing.T) {
	for in, want := range map[string]int{"": 0, "a": 1, "a\n": 1, "a\nb": 2, "a\nb\n": 2, "a\r\nb\r\n": 2} {
		if got := lineCount([]byte(in)); got != want {
			t.Errorf("lineCount(%q) = %d, want %d", in, got, want)
		}
	}
}
