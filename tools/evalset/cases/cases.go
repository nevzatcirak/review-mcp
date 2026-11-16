// Package cases reads the synthetic evaluation corpus of V-9 (X-49) under
// tools/evalset/corpus and checks it against the rules of the V-9 spec
// (§1.1 to §1.3). Each case is a small repository: base/ is the tree at the
// base commit, head/ holds the files the pull request adds or changes, and
// deleted.txt lists the paths it deletes. The corpus is text for a reviewer
// to read: nothing here builds or runs a case (R-1). Standard library only.
package cases

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// Known values of the case files.
var (
	Languages  = []string{"java", "kotlin", "typescript", "go", "python", "csharp"}
	Categories = []string{"cascade", "local", "clean", "security", "concurrency"}
	Severities = []string{"high", "medium", "low"}
	Kinds      = []string{"cascade", "local"}
	// Tags: "large" marks the stacked pull requests of 30+ files (§1.2
	// row "large"); "body-only" marks a cascade whose change is a method
	// body only (amendment 2026-11-16, item 5).
	Tags = []string{"large", "body-only"}
)

// Issue is one ground-truth issue of a case.
type Issue struct {
	ID          string
	Severity    string
	Kind        string
	Path        string
	Lines       [2]int
	Hops        int
	MustMention []string
	Summary     string
}

// Case is one case of the corpus.
type Case struct {
	// Dir is the directory name under the corpus, equal to ID.
	Dir         string
	ID          string
	Language    string
	Category    string
	Title       string
	Description string
	Tags        []string
	// TwinOf names the cascade case whose clean twin this case is.
	TwinOf string
	Issues []Issue

	// Base maps the slash-separated paths of base/ to their content;
	// Head does the same for head/. Deleted lists the paths of deleted.txt.
	Base    map[string][]byte
	Head    map[string][]byte
	Deleted []string
}

// HasTag reports whether the case carries the tag.
func (c *Case) HasTag(tag string) bool {
	return contains(c.Tags, tag)
}

// AtHead returns the content of p at the head commit and whether it exists
// there.
func (c *Case) AtHead(p string) ([]byte, bool) {
	if b, ok := c.Head[p]; ok {
		return b, true
	}
	if contains(c.Deleted, p) {
		return nil, false
	}
	b, ok := c.Base[p]
	return b, ok
}

// Changed reports whether the pull request adds, changes or deletes p.
func (c *Case) Changed(p string) bool {
	_, inHead := c.Head[p]
	return inHead || contains(c.Deleted, p)
}

// Main returns the issue that is not low, the defect of a defect case.
func (c *Case) Main() (Issue, bool) {
	for _, is := range c.Issues {
		if is.Severity != "low" {
			return is, true
		}
	}
	return Issue{}, false
}

// Load reads every case of the corpus in fsys (the corpus directory, for
// example os.DirFS("tools/evalset/corpus")), sorted by directory name.
// Every directory is a case; files at the top (the README, go.mod,
// .gitattributes) are not.
func Load(fsys fs.FS) ([]*Case, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	var out []*Case
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		c, err := loadCase(fsys, e.Name())
		if err != nil {
			return nil, fmt.Errorf("case %s: %w", e.Name(), err)
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Dir < out[j].Dir })
	return out, nil
}

var (
	topKeys   = []string{"id", "language", "category", "title", "description", "tags", "twin_of"}
	issueKeys = []string{"id", "severity", "kind", "path", "lines", "hops", "must_mention", "summary"}
)

func loadCase(fsys fs.FS, dir string) (*Case, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		switch e.Name() {
		case "case.toml", "deleted.txt", "base", "head":
		default:
			return nil, fmt.Errorf("unexpected entry %s (a case holds case.toml, base/, head/ and deleted.txt only)", e.Name())
		}
	}
	src, err := fs.ReadFile(fsys, dir+"/case.toml")
	if err != nil {
		return nil, err
	}
	doc, err := parseTOML(string(src))
	if err != nil {
		return nil, fmt.Errorf("case.toml: %w", err)
	}
	c := &Case{Dir: dir}
	if err := fill(doc.top, topKeys, map[string]any{
		"id": &c.ID, "language": &c.Language, "category": &c.Category, "title": &c.Title,
		"description": &c.Description, "tags": &c.Tags, "twin_of": &c.TwinOf,
	}); err != nil {
		return nil, fmt.Errorf("case.toml: %w", err)
	}
	for i, t := range doc.issues {
		var is Issue
		var lines []int
		if err := fill(t, issueKeys, map[string]any{
			"id": &is.ID, "severity": &is.Severity, "kind": &is.Kind, "path": &is.Path, "lines": &lines,
			"hops": &is.Hops, "must_mention": &is.MustMention, "summary": &is.Summary,
		}); err != nil {
			return nil, fmt.Errorf("case.toml: issue %d: %w", i+1, err)
		}
		if len(lines) != 2 {
			return nil, fmt.Errorf("case.toml: issue %d: lines must be [first, last]", i+1)
		}
		is.Lines = [2]int{lines[0], lines[1]}
		c.Issues = append(c.Issues, is)
	}
	if c.Base, err = readTree(fsys, dir+"/base"); err != nil {
		return nil, err
	}
	if c.Head, err = readTree(fsys, dir+"/head"); err != nil {
		return nil, err
	}
	if b, err := fs.ReadFile(fsys, dir+"/deleted.txt"); err == nil {
		for _, l := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				c.Deleted = append(c.Deleted, l)
			}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return c, nil
}

// fill copies the table's values into the targets and refuses unknown keys
// and values of the wrong type. Missing keys keep the zero value; Check
// reports the required ones.
func fill(t *table, known []string, targets map[string]any) error {
	keys := make([]string, 0, len(t.values))
	for k := range t.values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !contains(known, k) {
			return fmt.Errorf("line %d: unknown key %s", t.lines[k], k)
		}
		v := t.values[k]
		ok := false
		switch dst := targets[k].(type) {
		case *string:
			var s string
			s, ok = v.(string)
			*dst = s
		case *int:
			var n int
			n, ok = v.(int)
			*dst = n
		case *[]string:
			var s []string
			s, ok = v.([]string)
			*dst = s
		case *[]int:
			var n []int
			n, ok = v.([]int)
			*dst = n
		}
		if !ok {
			return fmt.Errorf("line %d: %s has the wrong type", t.lines[k], k)
		}
	}
	return nil
}

// readTree reads every file under root into a map keyed by the path
// relative to root, with slashes.
func readTree(fsys fs.FS, root string) (map[string][]byte, error) {
	out := map[string][]byte{}
	err := fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s is not a regular file", p)
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		out[strings.TrimPrefix(p, root+"/")] = b
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	return out, err
}

// lineCount is the number of lines of b; a last line without a newline
// counts.
func lineCount(b []byte) int {
	n := bytes.Count(b, []byte("\n"))
	if len(b) > 0 && b[len(b)-1] != '\n' {
		n++
	}
	return n
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// validPath reports whether p is a clean, relative, slash-separated path.
func validPath(p string) bool {
	return p != "" && !strings.HasPrefix(p, "/") && !strings.Contains(p, "\\") && path.Clean(p) == p && !strings.HasPrefix(p, "../") && p != ".."
}
