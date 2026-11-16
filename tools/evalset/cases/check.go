package cases

import (
	"bytes"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Base sizes of §1.2: "each base repository is small (5–40 files)".
const (
	MinBaseFiles = 5
	MaxBaseFiles = 40
	// MinLargeFiles is the size of a large pull request (§1.2 row "large"):
	// files added, changed or deleted.
	MinLargeFiles = 30
)

// giveaway matches words that would tell the reviewer a pull request has a
// defect (§0 item 3): titles and descriptions say what a change does.
var giveaway = regexp.MustCompile(`(?i)\b(bugs?|buggy|defects?|flaws?|vulnerab\w*|insecure|broken|wrong|incorrect|mistakes?|regressions?|issues?)\b`)

// marker matches comments that point at a problem in the code itself.
var marker = regexp.MustCompile(`\b(TODO|FIXME|XXX|HACK|BUG)\b`)

// hostRE finds the host of every URL in a file.
var hostRE = regexp.MustCompile(`https?://([A-Za-z0-9.-]+)`)

// placeholderHost reports whether a URL host is a placeholder (clean-room
// rule: generic domains only) or one of the XML schema hosts that a Maven
// pom names by convention.
func placeholderHost(h string) bool {
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	for _, suffix := range []string{"example.com", "example.org", "example.net", ".example", ".invalid", ".test"} {
		if h == strings.TrimPrefix(suffix, ".") || strings.HasSuffix(h, "."+strings.TrimPrefix(suffix, ".")) {
			return true
		}
	}
	switch h {
	case "localhost", "127.0.0.1", "maven.apache.org", "www.w3.org":
		return true
	}
	return false
}

// CheckCase returns every problem of one case. all maps the ids of the whole
// corpus to their cases, for twin_of.
func CheckCase(c *Case, all map[string]*Case) []error {
	var errs []error
	bad := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("case %s: %s", c.Dir, fmt.Sprintf(format, args...)))
	}

	if c.ID != c.Dir {
		bad("id %q differs from the directory name", c.ID)
	}
	if !contains(Languages, c.Language) {
		bad("unknown language %q", c.Language)
	}
	if !contains(Categories, c.Category) {
		bad("unknown category %q", c.Category)
	}
	for _, f := range []struct{ name, text string }{{"title", c.Title}, {"description", c.Description}} {
		if strings.TrimSpace(f.text) == "" {
			bad("%s is empty", f.name)
		}
		if w := giveaway.FindString(f.text); w != "" {
			bad("%s says %q; pull request text must stay neutral (§0 item 3)", f.name, w)
		}
	}
	for _, t := range c.Tags {
		if !contains(Tags, t) {
			bad("unknown tag %q", t)
		}
	}
	if c.HasTag("body-only") && c.Category != "cascade" {
		bad("tag body-only is for cascade cases")
	}

	// The trees.
	if n := len(c.Base); n < MinBaseFiles || n > MaxBaseFiles {
		bad("base has %d files, want %d to %d", n, MinBaseFiles, MaxBaseFiles)
	}
	if len(c.Head) == 0 && len(c.Deleted) == 0 {
		bad("the pull request changes nothing (head/ is empty and there is no deleted.txt)")
	}
	for p, b := range c.Head {
		if old, ok := c.Base[p]; ok && bytes.Equal(old, b) {
			bad("head/%s equals base/%s; the overlay holds changed files only", p, p)
		}
	}
	seen := map[string]bool{}
	for _, p := range c.Deleted {
		switch {
		case seen[p]:
			bad("deleted.txt lists %s twice", p)
		case !validPath(p):
			bad("deleted.txt: %q is not a clean relative path", p)
		case c.Base[p] == nil:
			bad("deleted.txt: %s is not in base", p)
		case c.Head[p] != nil:
			bad("deleted.txt: %s is also in head", p)
		}
		seen[p] = true
	}
	checkFiles(c, bad)

	// Ground truth.
	if c.Category == "clean" {
		if len(c.Issues) > 0 {
			bad("a clean case has %d issues, want none", len(c.Issues))
		}
	} else {
		main := 0
		for _, is := range c.Issues {
			if is.Severity != "low" {
				main++
			}
		}
		if main != 1 {
			bad("a %s case has %d issues that are not low, want exactly one (§1.2)", c.Category, main)
		}
		if m, ok := c.Main(); ok && c.Category == "cascade" && m.Kind != "cascade" {
			bad("the defect of a cascade case must be of kind cascade, not %q", m.Kind)
		}
	}
	ids := map[string]bool{}
	for _, is := range c.Issues {
		checkIssue(c, is, bad)
		if ids[is.ID] {
			bad("issue id %q is used twice", is.ID)
		}
		ids[is.ID] = true
	}

	// Large pull requests.
	if c.HasTag("large") {
		changed := len(c.Head) + len(c.Deleted)
		if changed < MinLargeFiles {
			bad("a large case changes %d files, want at least %d", changed, MinLargeFiles)
		}
		if n := len(topDirs(c)); n < 2 {
			bad("a large case changes %d top-level modules, want at least 2", n)
		}
	}

	// Clean twins.
	if c.TwinOf != "" {
		t := all[c.TwinOf]
		switch {
		case c.Category != "clean":
			bad("twin_of is for clean cases")
		case t == nil:
			bad("twin_of names %q, which is not a case", c.TwinOf)
		case t.Category != "cascade":
			bad("twin_of names %s, a %s case; a twin belongs to a cascade case", t.ID, t.Category)
		case !sameTree(c.Base, t.Base):
			bad("the base differs from the base of its cascade case %s", t.ID)
		}
	}
	return errs
}

func checkIssue(c *Case, is Issue, bad func(string, ...any)) {
	name := is.ID
	if strings.TrimSpace(name) == "" {
		bad("an issue has no id")
		name = "?"
	}
	if !contains(Severities, is.Severity) {
		bad("issue %s: unknown severity %q", name, is.Severity)
	}
	if !contains(Kinds, is.Kind) {
		bad("issue %s: unknown kind %q", name, is.Kind)
	}
	if strings.TrimSpace(is.Summary) == "" {
		bad("issue %s: summary is empty", name)
	}
	n := 0
	for _, m := range is.MustMention {
		if strings.TrimSpace(m) != "" {
			n++
		}
	}
	if n < 2 || n != len(is.MustMention) {
		bad("issue %s: must_mention needs at least two non-empty entries (the mention rule matches two)", name)
	}
	if !validPath(is.Path) {
		bad("issue %s: path %q is not a clean relative path", name, is.Path)
		return
	}
	content, ok := c.AtHead(is.Path)
	if !ok {
		bad("issue %s: %s does not exist at head", name, is.Path)
	} else if lc := lineCount(content); is.Lines[0] < 1 || is.Lines[0] > is.Lines[1] || is.Lines[1] > lc {
		bad("issue %s: lines %v are not inside %s (%d lines)", name, is.Lines, is.Path, lc)
	}
	switch is.Kind {
	case "cascade":
		if c.Changed(is.Path) {
			bad("issue %s: a cascade issue's path %s is changed by the case; it must be an unchanged file", name, is.Path)
		}
		if is.Hops != 1 && is.Hops != 2 {
			bad("issue %s: a cascade issue needs hops 1 or 2, not %d", name, is.Hops)
		}
	case "local":
		if _, inHead := c.Head[is.Path]; !inHead {
			bad("issue %s: a local issue's path %s must be added or changed by the case", name, is.Path)
		}
		if is.Hops != 0 {
			bad("issue %s: hops is for cascade issues", name)
		}
	}
}

// checkFiles applies the clean-room rules to every file of both trees.
func checkFiles(c *Case, bad func(string, ...any)) {
	for _, tree := range []struct {
		name  string
		files map[string][]byte
	}{{"base", c.Base}, {"head", c.Head}} {
		for _, p := range sortedKeys(tree.files) {
			if !validPath(p) {
				bad("%s: %q is not a clean relative path", tree.name, p)
			}
			b := tree.files[p]
			if m := marker.Find(b); m != nil {
				bad("%s/%s has a %s marker; comments must not point at problems", tree.name, p, m)
			}
			for _, h := range hostRE.FindAllSubmatch(b, -1) {
				if !placeholderHost(string(h[1])) {
					bad("%s/%s names the host %s; use placeholder hosts only (example.com, .invalid)", tree.name, p, h[1])
				}
			}
		}
	}
}

// CheckCorpus checks the whole corpus: each case, unique ids, and the
// counts of §1.2 and of the amendment's items 4 and 5.
func CheckCorpus(cases []*Case) []error {
	var errs []error
	all := map[string]*Case{}
	for _, c := range cases {
		if all[c.ID] != nil {
			errs = append(errs, fmt.Errorf("id %s is used by two cases", c.ID))
		}
		all[c.ID] = c
	}
	for _, c := range cases {
		errs = append(errs, CheckCase(c, all)...)
	}
	return append(errs, checkCounts(cases)...)
}

func checkCounts(cases []*Case) []error {
	var errs []error
	need := func(ok bool, format string, args ...any) {
		if !ok {
			errs = append(errs, fmt.Errorf("corpus: "+format, args...))
		}
	}
	count := func(keep func(*Case) bool) int {
		n := 0
		for _, c := range cases {
			if keep(c) {
				n++
			}
		}
		return n
	}
	cascade := func(c *Case) bool { return c.Category == "cascade" && !c.HasTag("large") }
	mainHops := func(c *Case) int { m, _ := c.Main(); return m.Hops }

	total := len(cases)
	need(total >= 24, "%d cases, want at least 24", total)
	need(count(cascade) >= 8, "%d cascade cases, want at least 8", count(cascade))
	jvm := count(func(c *Case) bool {
		return cascade(c) && (c.Language == "java" || c.Language == "kotlin" || c.Language == "csharp")
	})
	need(jvm >= 3, "%d cascade cases in Java, Kotlin or C#, want at least 3", jvm)
	for _, lang := range []string{"typescript", "go", "python"} {
		n := count(func(c *Case) bool { return cascade(c) && c.Language == lang })
		need(n >= 1, "no cascade case in %s", lang)
	}
	hops2 := count(func(c *Case) bool { return cascade(c) && mainHops(c) == 2 })
	need(hops2 >= 4, "%d cascade cases with hops = 2, want at least 4", hops2)
	bodyOnly := count(func(c *Case) bool { return cascade(c) && c.HasTag("body-only") })
	need(bodyOnly >= 2, "%d body-only cascade cases, want at least 2 (amendment item 5)", bodyOnly)

	local := count(func(c *Case) bool { return c.Category == "local" })
	need(local >= 6, "%d local cases, want at least 6", local)
	clean := count(func(c *Case) bool { return c.Category == "clean" })
	need(clean >= 6, "%d clean cases, want at least 6", clean)
	need(4*clean >= total, "%d clean cases of %d, want at least a quarter (§0 item 5)", clean, total)
	secConc := count(func(c *Case) bool { return c.Category == "security" || c.Category == "concurrency" })
	need(secConc >= 4, "%d security and concurrency cases, want at least 4", secConc)

	largeCascade := count(func(c *Case) bool { return c.HasTag("large") && c.Category == "cascade" })
	largeClean := count(func(c *Case) bool { return c.HasTag("large") && c.Category == "clean" && c.TwinOf != "" })
	need(largeCascade >= 1 && largeClean >= 1, "large cases: %d cascade and %d clean twins, want one of each", largeCascade, largeClean)

	defects := count(func(c *Case) bool { return c.Category != "clean" })
	unchanged := count(func(c *Case) bool { m, ok := c.Main(); return ok && m.Kind == "cascade" })
	need(3*unchanged >= defects, "%d of %d defect cases have the defect in an unchanged file, want at least a third (§0 item 4)", unchanged, defects)
	return errs
}

// CheckReadme checks that the corpus README has one table row per case,
// starting with the case id in backticks, and no row for an unknown id.
func CheckReadme(readme string, cases []*Case) []error {
	var errs []error
	rows := map[string]bool{}
	row := regexp.MustCompile("(?m)^\\| `([a-z0-9-]+)` \\|")
	for _, m := range row.FindAllStringSubmatch(readme, -1) {
		rows[m[1]] = true
	}
	for _, c := range cases {
		if !rows[c.ID] {
			errs = append(errs, fmt.Errorf("README.md has no row for %s", c.ID))
		}
		delete(rows, c.ID)
	}
	for _, id := range sortedKeys(rows) {
		errs = append(errs, fmt.Errorf("README.md has a row for %s, which is not a case", id))
	}
	return errs
}

func topDirs(c *Case) map[string]bool {
	dirs := map[string]bool{}
	add := func(p string) {
		if i := strings.IndexByte(p, '/'); i > 0 {
			dirs[p[:i]] = true
		}
	}
	for p := range c.Head {
		add(p)
	}
	for _, p := range c.Deleted {
		add(p)
	}
	return dirs
}

func sameTree(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for p, x := range a {
		if y, ok := b[p]; !ok || !bytes.Equal(x, y) {
			return false
		}
	}
	return true
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
