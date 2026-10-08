package gitctx

import (
	"regexp"
	"sort"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/filter"
	"github.com/nevzatcirak/review-mcp/internal/patch"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// Symbol extraction (RC-7).
//
// It lives here, next to Grep, because Symbol is Grep's input and the two
// share the language grouping; it depends only on internal/patch (the hunk
// model), internal/filter (the language table) and internal/provider, never
// on internal/review.

// Default and minimum values of the symbol search.
const (
	DefaultMaxSymbols       = 20
	DefaultMaxHitsPerSymbol = 5
	// MinSymbolLen is the shortest symbol kept: shorter names ("id", "get",
	// "run") match too much to say anything.
	MinSymbolLen = 4
	// maxDefLine bounds the lines looked at: a longer one is minified or
	// generated.
	maxDefLine = 400
)

// Rank orders the symbols of a pull request by how likely a change breaks
// code outside it (RC-7). Lower is more important.
type Rank int

const (
	// RankRemoved: a definition on a removed line whose name is not defined
	// on any added line of the pull request. That is a removal or a rename
	// of the old name, the change most likely to break callers.
	RankRemoved Rank = iota
	// RankSignature: the name is defined on a removed line and on an added
	// line, and the two definitions differ once whitespace is normalised.
	RankSignature
	// RankChanged: every other changed definition: the function or class
	// named by a hunk header (its body changed), a definition that moved
	// unchanged, and a definition that is new.
	RankChanged
)

func (r Rank) String() string {
	switch r {
	case RankRemoved:
		return "removed"
	case RankSignature:
		return "signature"
	case RankChanged:
		return "changed"
	}
	return "unknown"
}

// Symbol is a name taken from the diff whose uses are searched for.
type Symbol struct {
	Name string
	// Path is the changed file the symbol was taken from (the removed
	// definition when there is one); its language group steers the choice of
	// hits.
	Path string
	Rank Rank
	// New: the name is defined on an added line only (a new definition, not
	// one that existed before). Within a rank, existing definitions come
	// first because only they can have callers.
	New bool
	// Test: Path is a test file (IsTestPath). Test symbols rank after every
	// other symbol, whatever their Rank: a test is a caller, not a thing
	// callers depend on.
	Test bool
}

// IsTestPath reports whether path names a test file: by file name
// (*_test.go, test_*.py, *_test.py, *Test.java, *Tests.java, *Test.kt,
// *.test.* and *.spec.* of ts, tsx, js and jsx, *Test.cs, *Tests.cs) or by a
// directory segment equal to test, tests or __tests__. Names are
// case-sensitive and a segment is compared whole ("latest/" is not "test/").
// The repository has no other test-file rule to reuse.
func IsTestPath(path string) bool {
	segs := strings.Split(strings.ReplaceAll(path, "\\", "/"), "/")
	for _, d := range segs[:len(segs)-1] {
		if d == "test" || d == "tests" || d == "__tests__" {
			return true
		}
	}
	base := segs[len(segs)-1]
	for _, suf := range testSuffixes {
		if len(base) > len(suf) && strings.HasSuffix(base, suf) {
			return true
		}
	}
	return (strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py") && len(base) > len("test_.py"))
}

var testSuffixes = []string{
	"_test.go", "_test.py", "Test.java", "Tests.java", "Test.kt", "Test.cs", "Tests.cs",
	".test.ts", ".test.tsx", ".test.js", ".test.jsx",
	".spec.ts", ".spec.tsx", ".spec.js", ".spec.jsx",
}

// ChangedPaths returns the paths of the pull request's own files: the new
// path and, for a rename, the old one. They are what Query.Exclude takes.
func ChangedPaths(files []provider.FilePatch) []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range files {
		for _, p := range []string{f.Path, f.OldPath} {
			if p != "" && !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}

// ExtractSymbols returns up to max symbols (DefaultMaxSymbols when max <= 0)
// from the patches of a pull request, most important first:
//
//   - names from the hunk section headers (the function or class that
//     contains the change);
//   - names defined on added or removed lines, by language-agnostic
//     patterns for func, fun, fn, def, function, class, interface, type,
//     struct, enum, trait, method signatures and exported constants.
//
// Ranking: removed or renamed, changed signature, other changed (existing)
// definitions, new definitions, then the symbols of test files (Symbol.Test,
// in the same sub-order, a new test definition included); inside each the
// order of the diff (files in the order given, hunks and lines in order). A
// name is kept once, with its best rank. The cap applies after the whole
// ordering, so test symbols are the first to be cut.
//
// A "changed signature" is precise: the name is defined on at least one
// removed line and at least one added line of the pull request, and the set
// of removed definition lines differs from the set of added ones after
// whitespace is collapsed and a trailing "{" is dropped. A definition moved
// to another file, or re-indented, is not a changed signature.
//
// Names shorter than MinSymbolLen characters, keywords and a few ubiquitous
// names (see noiseNames) are dropped. The function is pure.
func ExtractSymbols(files []provider.FilePatch, max int) []Symbol {
	if max <= 0 {
		max = DefaultMaxSymbols
	}
	aggs := map[string]*symAgg{}
	var order []*symAgg
	note := func(name, path string, side byte, text string) {
		a, ok := aggs[name]
		if !ok {
			a = &symAgg{name: name, seq: len(order), removed: map[string]bool{}, added: map[string]bool{}}
			aggs[name] = a
			order = append(order, a)
		}
		if a.anyPath == "" {
			a.anyPath = path
		}
		switch side {
		case '-':
			a.removed[text] = true
			if a.removedPath == "" {
				a.removedPath = path
			}
		case '+':
			a.added[text] = true
		default:
			a.header = true
		}
	}
	for _, fp := range files {
		if fp.Binary || fp.Patch == "" {
			continue
		}
		hunks, err := patch.ParseHunks(fp.Patch)
		if err != nil {
			continue
		}
		lang := filter.Language(fp.Path)
		for _, h := range hunks {
			if h.Malformed() {
				continue
			}
			if name, ok := definitionName(h.Section, lang, true); ok {
				note(name, fp.Path, 0, "")
			}
			for _, l := range h.Lines {
				if l.Op != '+' && l.Op != '-' {
					continue
				}
				text := strings.TrimRight(l.Text, "\r\n")
				if name, ok := definitionName(text, lang, false); ok {
					note(name, fp.Path, l.Op, normalizeDef(text))
				}
			}
		}
	}

	out := make([]Symbol, 0, len(order))
	for _, a := range order {
		out = append(out, a.symbol())
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Test != out[j].Test {
			return !out[i].Test
		}
		if out[i].Rank != out[j].Rank {
			return out[i].Rank < out[j].Rank
		}
		return !out[i].New && out[j].New
	})
	if len(out) > max {
		out = out[:max]
	}
	return out
}

// symAgg collects everything seen about one name.
type symAgg struct {
	name        string
	seq         int
	removed     map[string]bool // normalised removed definition lines
	added       map[string]bool
	header      bool
	removedPath string
	anyPath     string
}

func (a *symAgg) symbol() Symbol {
	s := Symbol{Name: a.name, Path: a.anyPath, Rank: RankChanged}
	switch {
	case len(a.removed) > 0 && len(a.added) == 0:
		s.Rank = RankRemoved
	case len(a.removed) > 0 && !sameSet(a.removed, a.added):
		s.Rank = RankSignature
	}
	if a.removedPath != "" {
		s.Path = a.removedPath
	}
	s.New = len(a.removed) == 0 && len(a.added) > 0 && !a.header
	s.Test = IsTestPath(s.Path)
	return s
}

func sameSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

// normalizeDef collapses whitespace and drops a trailing "{" or "{" plus
// spaces, so that moving a brace to the next line is not a changed
// signature.
func normalizeDef(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimSpace(strings.TrimSuffix(s, "{"))
}

// ---- definition patterns ----

// mods is a run of modifier words and annotations in front of a definition
// keyword. It is language-agnostic on purpose: a word that one language does
// not have is simply never there.
const modWord = `(?:public|private|protected|internal|fileprivate|open|static|final|abstract|override|virtual|sealed|async|suspend|inline|infix|operator|tailrec|external|expect|actual|lateinit|synchronized|native|extern|unsafe|readonly|partial|export|default|declare|mutating|nonisolated|pub(?:\([^)]*\))?|@\w+(?:\([^)]*\))?)`

var (
	mods        = `(?:` + modWord + `\s+)*`
	modsAtLeast = `(?:` + modWord + `\s+)+`
	typeKinds   = `(?:class|interface|struct|enum|trait|protocol|record|object|typealias|union|module|extension)`
)

// defRule is one definition pattern. The name is in group name; guard, when
// set, rejects a match from its groups.
type defRule struct {
	re    *regexp.Regexp
	name  int
	guard func(m []string) bool
	// langs, when set, limits the rule to these languages (a rule that is
	// only safe where the syntax is C-like).
	langs map[string]bool
	// section: a langs-limited rule that also applies to hunk headers of
	// every language (git cuts a header at a function line of its own).
	section bool
}

var cLike = map[string]bool{"Java": true, "C": true, "C++": true, "C#": true, "Objective-C": true, "Dart": true, "Scala": true, "PHP": true, "Kotlin": true}

var defRules = []defRule{
	// Go and Swift: func, with a Go receiver.
	{re: regexp.MustCompile(`^` + mods + `func\s+(?:\([^)]*\)\s*)?([A-Za-z_]\w*)\s*[(\[<]`), name: 1, section: true},
	// Kotlin.
	{re: regexp.MustCompile(`^` + mods + `fun\s+(?:<[^>]*>\s*)?(?:[\w.<>?]+\.)?(\w+)\s*\(`), name: 1, section: true},
	// Rust.
	{re: regexp.MustCompile(`^` + mods + `(?:(?:const|unsafe|async|extern(?:\s+"[^"]*")?)\s+)*fn\s+(\w+)`), name: 1, section: true},
	// Python, Ruby.
	{re: regexp.MustCompile(`^` + mods + `def\s+(?:self\.)?(\w+[?!]?)`), name: 1, section: true},
	// JavaScript, TypeScript, PHP.
	{re: regexp.MustCompile(`^` + mods + `function\s*\*?\s*(\w+)`), name: 1, section: true},
	// class, interface, struct, enum, ... (Kotlin "data class", C++ "enum class").
	{re: regexp.MustCompile(`^` + mods + `(?:const\s+)?(?:(?:data|enum|sealed|annotation|value|inner|case|abstract)\s+)*` + typeKinds + `\s+(\w+)`), name: 1, section: true},
	// type: Go, TypeScript, Python 3.12, Rust.
	{re: regexp.MustCompile(`^` + mods + `type\s+(\w+)`), name: 1, section: true},
	// Go exported const and var.
	{re: regexp.MustCompile(`^(?:const|var)\s+([A-Z]\w*)`), name: 1},
	// JavaScript, TypeScript exported const, let, var.
	{re: regexp.MustCompile(`^export\s+(?:declare\s+)?(?:const|let|var)\s+(\w+)`), name: 1},
	// A function-valued const (arrow function).
	{re: regexp.MustCompile(`^(?:const|let|var)\s+(\w+)\s*(?::[^=]+)?=\s*(?:async\s*)?(?:function\b|\([^()]*\)\s*(?::[^=]+)?=>|\w+\s*=>)`), name: 1},
	// Java, C#, Rust, Kotlin, Python constants spelt in capitals.
	{re: regexp.MustCompile(`^` + mods + `(?:(?:const|val|var|let|static|final|readonly)\s+)*(?:[\w.<>\[\]?,]+\s+){0,2}([A-Z][A-Z0-9_]{3,})\s*(?::[^=]+)?=(?:[^=>]|$)`), name: 1},
	// C#, Java: a visible const with any name.
	{re: regexp.MustCompile(`^(?:(?:public|internal|protected)\s+|pub(?:\([^)]*\))?\s+)(?:static\s+|readonly\s+)*const\s+(?:[\w.<>\[\]?]+\s+)?(\w+)\s*(?::\s*[^=]+)?=`), name: 1},
	// A method signature with at least one modifier or annotation:
	// "public static void main(", "private foo(", "@Test void foo(".
	{re: regexp.MustCompile(`^` + modsAtLeast + `(?:<[^>]*>\s*)?(?:[^\s(=]+\s+)*?(\w+)\s*\(`), name: 1, section: true},
	// A bare method of a JavaScript or TypeScript class: "foo(a, b) {".
	{re: regexp.MustCompile(`^(?:async\s+)?(?:get\s+|set\s+)?\*?(\w+)\s*(?:<[^>]*>)?\([^()'"` + "`" + `]*\)\s*(?::\s*[^{=]+)?\{\s*$`), name: 1,
		langs: map[string]bool{"JavaScript": true, "TypeScript": true, "Java": true, "C#": true, "Kotlin": true, "PHP": true, "Dart": true}},
	// A typed signature without modifiers: "int main(int argc) {",
	// "Foo bar(x) throws X {", "void run();".
	{re: regexp.MustCompile(`^([A-Za-z_][\w.<>\[\]?,*&:]*)\s+[*&]*(\w+)\s*\(`), name: 2, langs: cLike, section: true,
		guard: func(m []string) bool { return !statementWords[strings.ToLower(m[1])] }},
}

// statementWords are words that start a statement, not a type: "return
// foo(x)" is a call, not a definition of foo.
var statementWords = setOf(
	"return", "new", "throw", "else", "yield", "await", "delete", "typeof", "case", "goto", "break",
	"continue", "go", "defer", "select", "in", "is", "as", "not", "and", "or", "raise", "assert",
	"print", "puts", "echo", "elif", "do", "if", "for", "while", "switch", "catch", "using", "import",
	"package", "from", "with", "extends", "implements", "throws", "var", "val", "let", "const",
)

// keywords are not symbols, whatever they follow.
var keywords = setOf(
	"abstract", "async", "await", "break", "case", "catch", "class", "const", "continue", "default",
	"defer", "delete", "else", "elif", "enum", "export", "extends", "false", "final", "finally", "from",
	"func", "function", "import", "interface", "internal", "lambda", "long", "loop", "match", "move",
	"none", "null", "package", "private", "protected", "public", "raise", "return", "self", "short",
	"static", "struct", "super", "switch", "then", "this", "throw", "throws", "true", "type", "typeof",
	"union", "unsafe", "void", "volatile", "when", "where", "while", "with", "yield", "bool", "byte",
	"char", "float", "double", "object", "string", "trait", "impl", "mod", "enum", "else", "fun",
	"abstract", "override", "virtual", "sealed", "record", "using", "namespace", "module",
)

// noiseNames are ubiquitous names whose uses say nothing: every program has
// a main, every test a setup.
var noiseNames = setOf(
	"main", "init", "test", "tests", "setup", "teardown", "constructor", "tostring", "equals", "hashcode",
	"__init__", "__new__", "__str__", "__repr__", "__eq__", "__hash__", "__call__", "__del__", "__enter__",
	"__exit__", "__len__", "__iter__", "__next__", "__getitem__", "__setitem__", "initialize",
)

func setOf(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

// proseLanguages hold no definitions worth a symbol: a line of prose that
// starts with "module" or "object" must not become one.
var proseLanguages = setOf("Markdown", "Text", "reStructuredText", "AsciiDoc", "JSON", "YAML", "TOML", "XML", "HTML", "CSS", "SCSS", "Less", "INI", "CSV", "Org", "Gettext Catalog", "Diff", "Properties")

var nameRE = regexp.MustCompile(`^[A-Za-z_$][\w$]*$`)

// validSymbol reports whether name can be a symbol: long enough, an
// identifier, not a keyword or a ubiquitous name.
func validSymbol(name string) bool {
	if len(name) < MinSymbolLen || len(name) > 120 || !nameRE.MatchString(name) {
		return false
	}
	if strings.Trim(name, "_$") == "" {
		return false
	}
	low := strings.ToLower(name)
	return !keywords[low] && !noiseNames[low]
}

// definitionName returns the name a source line (or a hunk header section)
// defines. lang is the language group of the file. section is true for a
// hunk header, which git cuts at a function line of its own choosing, so
// every rule that is safe without a language applies.
func definitionName(line, lang string, section bool) (string, bool) {
	line = strings.TrimSpace(line)
	if line == "" || len(line) > maxDefLine {
		return "", false
	}
	if proseLanguages[lang] {
		return "", false
	}
	for _, r := range defRules {
		if r.langs != nil && !r.langs[lang] && (!section || !r.section) {
			continue
		}
		m := r.re.FindStringSubmatch(line)
		if m == nil || (r.guard != nil && !r.guard(m)) {
			continue
		}
		name := m[r.name]
		if validSymbol(name) {
			return name, true
		}
		// A keyword or a short name in the name slot ("if (x) {") ends the
		// search for the line: a later, looser rule must not read it again.
		return "", false
	}
	return "", false
}
