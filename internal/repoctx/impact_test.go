package repoctx

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nevzatcirak/review-mcp/internal/gitctx"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

// threeTrees is a trace with three seeds, each with two links.
func threeTrees() gitctx.ImpactResult {
	var nodes []gitctx.ImpactNode
	for s := range 3 {
		seed := len(nodes)
		nodes = append(nodes, gitctx.ImpactNode{Name: fmt.Sprintf("Seed%d", s), Path: fmt.Sprintf("pkg/s%d.go", s), Parent: -1, Rank: gitctx.RankSignature})
		for c := range 2 {
			nodes = append(nodes, gitctx.ImpactNode{Name: fmt.Sprintf("Caller%d_%d", s, c), Path: fmt.Sprintf("app/c%d_%d.go", s, c),
				DefLine: 10, DefText: fmt.Sprintf("func Caller%d_%d() {", s, c), UseLine: 12,
				UseText: "a()\nb()\n" + fmt.Sprintf("Seed%d()", s) + strings.Repeat(" // padding", 8) + "\nc()\nd()", Hop: 1, Parent: seed})
		}
	}
	return gitctx.ImpactResult{Nodes: nodes, Seeds: 3, Links: 6}
}

// TestRenderImpactCutsAtTreeBoundary is canary (k): a trace over max_tokens
// is cut at a tree boundary, and the left-out links are counted.
func TestRenderImpactCutsAtTreeBoundary(t *testing.T) {
	res := threeTrees()
	full := RenderImpact(res, nil, 100000, 1)
	two := RenderImpact(res, func(n gitctx.ImpactNode) bool { return n.Name != "Seed2" }, 100000, 1)
	limit := tokens.Estimate(two.Text, 1)
	if tokens.Estimate(full.Text, 1) <= limit {
		t.Fatal("fixture: the full block is not larger than two trees")
	}
	b := RenderImpact(res, nil, limit, 1)
	if b.Text != two.Text {
		t.Fatalf("the cut block is not the first two trees whole:\n%s\n---want---\n%s", b.Text, two.Text)
	}
	if b.Trees != 2 || b.TreesOmitted != 1 || b.NodesOmitted != 2 {
		t.Fatalf("counts: trees %d, omitted %d, links omitted %d", b.Trees, b.TreesOmitted, b.NodesOmitted)
	}
	if strings.Contains(b.Text, "Seed2") || strings.Contains(b.Text, "Caller2_") {
		t.Fatalf("part of the third tree is in the block:\n%s", b.Text)
	}
	if tokens.Estimate(b.Text, 1) > limit {
		t.Fatal("the block is over its budget")
	}
	if none := RenderImpact(res, nil, 10, 1); none.Text != "" || none.TreesOmitted != 3 || none.NodesOmitted != 6 {
		t.Fatalf("a budget too small for one tree: %+v", none)
	}
}

func TestRenderImpactFormat(t *testing.T) {
	res := gitctx.ImpactResult{Nodes: []gitctx.ImpactNode{
		{Name: "save", Path: "src/Repo.java", DefLine: 23, Parent: -1, Rank: gitctx.RankChanged, MoreUses: 7, NotFollowed: gitctx.NotFollowedWidelyUsed},
		{Name: "persist", Path: "src/Service.java", DefLine: 51, DefText: "public void persist(Order o) {", UseLine: 58,
			UseText: "a();\nb();\nrepository.save(order);\nc();\nd();", Hop: 1, Parent: 0, InPR: true},
		{Name: "submit", Path: "web/Controller.java", DefLine: 40, DefText: "void submit() {", UseLine: 43,
			UseText: "x();\ny();\norderService.persist(cart);\nz();\nw();", Hop: 2, Parent: 1, Entry: true},
		{Name: "DbStore", Path: "src/DbStore.java", DefLine: 1, DefText: "class DbStore implements Store {", UseLine: 1,
			UseText: "class DbStore implements Store {\n}", Hop: 1, Parent: 0, Kind: gitctx.KindInherits},
		{Name: "RepoTest.java", Path: "src/test/RepoTest.java", UseLine: 3, UseText: "a\nb\nsave(x)\n\x1b[31mc", Hop: 1, Parent: 0,
			NotFollowed: gitctx.NotFollowedTest},
		{Name: "lonely", Path: "src/Lonely.java", Parent: -1, Rank: gitctx.RankRemoved},
	}}
	b := RenderImpact(res, nil, 100000, 1)
	want := []string{
		ImpactHeader,
		"save  (body changed)  src/Repo.java:23",
		"  ← persist  src/Service.java:51  [changed file]",
		"      public void persist(Order o) {",
		"      uses at :58:",
		"        56: a();",
		"        58: repository.save(order);",
		"    ← submit  web/Controller.java:40  [entry point]",
		"        uses at :43: orderService.persist(cart);",
		"  ← DbStore  src/DbStore.java:1  [extends, implements or overrides]",
		"  ← RepoTest.java  src/test/RepoTest.java  [file level]  [test]",
		"  ⋯ 7 more uses of save not followed (widely used)",
	}
	for _, w := range want {
		if !strings.Contains(b.Text, w) {
			t.Errorf("the block lacks %q:\n%s", w, b.Text)
		}
	}
	if strings.Contains(b.Text, "lonely") || strings.Contains(b.Text, "\x1b") {
		t.Fatalf("an empty tree or a control character:\n%s", b.Text)
	}
	if !slices.Equal(b.Paths, []string{"src/Repo.java", "src/Service.java", "web/Controller.java", "src/DbStore.java", "src/test/RepoTest.java"}) {
		t.Fatalf("paths %v", b.Paths)
	}
	// Repository text cannot close the fence.
	res.Nodes[1].UseText = "a\nb\n```` save\nc\nd"
	b = RenderImpact(res, nil, 100000, 1)
	if !strings.Contains(b.Text, "`````") {
		t.Fatalf("the fence is not longer than the text's backticks:\n%s", b.Text)
	}
}

func TestAddedLines(t *testing.T) {
	files := []provider.FilePatch{
		{Path: "a.go", Type: provider.ChangeModified, Patch: "@@ -1,4 +1,5 @@\n x\n-y\n+y1\n+y2\n z\n@@ -10,2 +11,3 @@\n p\n+q\n r\n"},
		{Path: "gone.go", Type: provider.ChangeDeleted, Patch: "@@ -1 +0,0 @@\n-a\n"},
		{Path: "bad.go", Type: provider.ChangeModified, Patch: "not a patch"},
		{Path: "bin.png", Type: provider.ChangeModified, Binary: true},
	}
	got := AddedLines(files)
	if !slices.Equal(got["a.go"], []gitctx.LineRange{{From: 2, To: 3}, {From: 12, To: 12}}) {
		t.Fatalf("a.go: %v", got["a.go"])
	}
	if _, ok := got["gone.go"]; ok {
		t.Fatal("a deleted file has ranges")
	}
	if r, ok := got["bad.go"]; !ok || len(r) != 0 {
		t.Fatalf("bad.go: %v %v", r, ok)
	}
	if _, ok := got["bin.png"]; ok {
		t.Fatal("a binary file has ranges")
	}
}

// TestRenderImpactKeepsIndentation is canary (s2) of WP-20c (review DQ-2
// of WP-20b): code lines keep their leading whitespace, tabs as they are,
// so a nested use shows its nesting; control characters are still dropped
// and a long line is cut at 400 bytes.
func TestRenderImpactKeepsIndentation(t *testing.T) {
	long := "\t\t" + strings.Repeat("é", 300)
	res := gitctx.ImpactResult{Nodes: []gitctx.ImpactNode{
		{Name: "charge", Path: "pkg/pay.py", DefLine: 3, Parent: -1, Rank: gitctx.RankSignature},
		{Name: "renew", Path: "pkg/job.py", DefLine: 10, DefText: "    def renew(self):", UseLine: 13,
			UseText: "        for s in subs:\n            if s.due:\n                charge(s)\x1b[0m\n            log(s)\n" + long, Hop: 1, Parent: 0},
		{Name: "run", Path: "pkg/main.go", DefLine: 5, DefText: "func run() {", UseLine: 8,
			UseText: "\tif ok {\n\t\tx()\n\t\tjob.renew()\n\t}\nc", Hop: 2, Parent: 1},
	}}
	b := RenderImpact(res, nil, 100000, 1)
	for _, want := range []string{
		"\n          def renew(self):\n",
		"\n        11:         for s in subs:\n",
		"\n        12:             if s.due:\n",
		"\n        13:                 charge(s)[0m\n",
		"\n        uses at :8: \t\tjob.renew()\n",
	} {
		if !strings.Contains(b.Text, want) {
			t.Fatalf("missing %q (indentation lost) in:\n%s", want, b.Text)
		}
	}
	if strings.Contains(b.Text, "\x1b") {
		t.Fatal("a control character reached the block")
	}
	found := false
	for _, l := range strings.Split(b.Text, "\n") {
		if code, ok := strings.CutPrefix(l, "        15: "); ok {
			found = true
			if len(code) > maxImpactLine || !strings.HasPrefix(code, "\t\t") || !utf8.ValidString(code) {
				t.Fatalf("the long line is %d bytes, lost its tabs or was cut inside a rune: %q", len(code), code)
			}
		}
	}
	if !found {
		t.Fatal("the long line is missing")
	}
}
