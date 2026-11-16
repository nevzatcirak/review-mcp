package gitctx

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// ---- fixtures ----

// The Java chain of canary (a): Repo.save <- OrderService.persist <-
// CheckoutController.submit.
var javaChain = map[string]string{
	"src/main/java/example/app/Repo.java": `package example.app;

public class Repo {
    public void save(Order order) {
        store(order);
    }
}
`,
	"src/main/java/example/app/OrderService.java": `package example.app;

public class OrderService {
    private final Repo repository;

    public OrderService(Repo repository) {
        this.repository = repository;
    }

    public void persist(Order order) {
        validate(order);
        repository.save(order);
    }
}
`,
	"src/main/java/example/web/CheckoutController.java": `package example.web;

public class CheckoutController {
    private final OrderService orderService;

    public void submit(Cart cart) {
        orderService.persist(cart.toOrder());
    }
}
`,
}

const (
	repoPath       = "src/main/java/example/app/Repo.java"
	servicePath    = "src/main/java/example/app/OrderService.java"
	controllerPath = "src/main/java/example/web/CheckoutController.java"
)

func seed(name, path string, rank Rank) Symbol {
	return Symbol{Name: name, Path: path, Rank: rank, Paths: []string{path}}
}

// nodeNamed returns the index of the first node named name, or -1.
func nodeNamed(res ImpactResult, name string) int {
	return slices.IndexFunc(res.Nodes, func(n ImpactNode) bool { return n.Name == name })
}

func describe(res ImpactResult) string {
	var b strings.Builder
	for i, n := range res.Nodes {
		fmt.Fprintf(&b, "\n  %d %s hop=%d parent=%d path=%s def=%d use=%d kind=%s nf=%s more=%d inpr=%v entry=%v",
			i, n.Name, n.Hop, n.Parent, n.Path, n.DefLine, n.UseLine, n.Kind, n.NotFollowed, n.MoreUses, n.InPR, n.Entry)
	}
	return b.String()
}

func trace(t *testing.T, r *Runner, c Checkout, q ImpactQuery) ImpactResult {
	t.Helper()
	res, err := r.Trace(context.Background(), c, q)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// ---- canaries ----

// TestTraceTwoHops is canary (a): a use two hops away is found.
func TestTraceTwoHops(t *testing.T) {
	r, c := localCheckout(t, javaChain, "")
	res := trace(t, r, c, ImpactQuery{Seeds: []Symbol{seed("save", repoPath, RankSignature)}, Depth: 2})
	p := nodeNamed(res, "persist")
	s := nodeNamed(res, "submit")
	if p < 0 || s < 0 {
		t.Fatalf("chain incomplete:%s", describe(res))
	}
	pn, sn := res.Nodes[p], res.Nodes[s]
	if pn.Hop != 1 || pn.Parent != 0 || pn.Path != servicePath || pn.DefLine != 10 || pn.UseLine != 12 || pn.Kind != KindCall {
		t.Fatalf("hop 1 node wrong:%s", describe(res))
	}
	if !strings.Contains(pn.UseText, "repository.save(order);") || pn.DefText != "public void persist(Order order) {" {
		t.Fatalf("hop 1 texts: def %q use %q", pn.DefText, pn.UseText)
	}
	if sn.Hop != 2 || sn.Parent != p || sn.Path != controllerPath || sn.UseLine != 7 || sn.NotFollowed != NotFollowedDepth {
		t.Fatalf("hop 2 node wrong:%s", describe(res))
	}
	if res.Seeds != 1 || res.Links != 2 || res.DepthReached != 2 || res.NotFollowedBy[NotFollowedDepth] != 1 {
		t.Fatalf("counts: %+v", res)
	}
}

// TestTraceCycle is canary (c): alpha <-> bravo terminates with each node
// once. It runs under a timeout.
func TestTraceCycle(t *testing.T) {
	r, c := localCheckout(t, map[string]string{
		"pkg/start.py": "def entry():\n    return 1\n",
		"pkg/one.py":   "def alpha():\n    entry()\n    bravo()\n",
		"pkg/two.py":   "def bravo():\n    alpha()\n",
	}, "")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	type out struct {
		res ImpactResult
		err error
	}
	done := make(chan out, 1)
	go func() {
		res, err := r.Trace(ctx, c, ImpactQuery{Seeds: []Symbol{seed("entry", "pkg/start.py", RankSignature)}, Depth: 3})
		done <- out{res, err}
	}()
	var o out
	select {
	case o = <-done:
	case <-time.After(40 * time.Second):
		t.Fatal("the trace of a cycle did not terminate")
	}
	if o.err != nil {
		t.Fatal(o.err)
	}
	count := map[string]int{}
	for _, n := range o.res.Nodes {
		count[n.Name]++
	}
	if count["entry"] != 1 || count["alpha"] != 1 || count["bravo"] != 1 || len(o.res.Nodes) != 3 {
		t.Fatalf("each node once wanted:%s", describe(o.res))
	}
}

// TestTraceWidelyUsed is canary (d): a name with 30 uses and a cap of 5 is
// not followed.
func TestTraceWidelyUsed(t *testing.T) {
	files := map[string]string{"pkg/core.py": "def helper():\n    return 1\n"}
	var main strings.Builder
	for i := range 30 {
		files[fmt.Sprintf("pkg/mod%02d.py", i)] = fmt.Sprintf("def user_%02d():\n    helper()\n", i)
		fmt.Fprintf(&main, "def caller_%02d():\n    user_%02d()\n", i, i)
	}
	files["pkg/main.py"] = main.String()
	r, c := localCheckout(t, files, "")
	res := trace(t, r, c, ImpactQuery{Seeds: []Symbol{seed("helper", "pkg/core.py", RankSignature)}, Depth: 3, MaxCallersPerNode: 5})
	if res.Nodes[0].NotFollowed != NotFollowedWidelyUsed || res.Nodes[0].MoreUses != 25 {
		t.Fatalf("seed not marked widely used:%s", describe(res))
	}
	if res.Links != 5 || res.DepthReached != 1 {
		t.Fatalf("children of a widely used name were followed:%s", describe(res))
	}
	for _, n := range res.Nodes[1:] {
		if n.NotFollowed != NotFollowedWidelyUsed {
			t.Fatalf("child not marked:%s", describe(res))
		}
	}
}

// TestTraceExcludePaths is canary (e): ExcludePaths drops a hit.
func TestTraceExcludePaths(t *testing.T) {
	files := map[string]string{
		"src/core/Store.java":  "public class Store {\n    public void flush() {\n    }\n}\n",
		"src/app/Service.java": "public class Service {\n    void persist() {\n        store.flush();\n    }\n}\n",
		"src/legacy/Old.java":  "public class Old {\n    void migrate() {\n        store.flush();\n    }\n}\n",
	}
	r, c := localCheckout(t, files, "")
	q := ImpactQuery{Seeds: []Symbol{seed("flush", "src/core/Store.java", RankSignature)}, Depth: 1, Scope: ImpactScope{ExcludePaths: []string{"src/legacy/"}}}
	res := trace(t, r, c, q)
	if nodeNamed(res, "migrate") >= 0 || nodeNamed(res, "persist") < 0 || res.NotFollowedBy[NotFollowedOutOfScope] != 1 {
		t.Fatalf("exclusion not applied:%s\n%+v", describe(res), res.NotFollowedBy)
	}
	// Paths confines; an empty prefix is the whole repository.
	q.Scope = ImpactScope{Paths: []string{"src/legacy"}}
	res = trace(t, r, c, q)
	if nodeNamed(res, "persist") >= 0 || nodeNamed(res, "migrate") < 0 {
		t.Fatalf("paths not applied:%s", describe(res))
	}
	q.Scope = ImpactScope{Paths: []string{"src/legacy", ""}}
	res = trace(t, r, c, q)
	if nodeNamed(res, "persist") < 0 || nodeNamed(res, "migrate") < 0 {
		t.Fatalf("empty prefix confined:%s", describe(res))
	}
}

// TestTraceInheritsKind is canary (f): an implements line is "inherits".
func TestTraceInheritsKind(t *testing.T) {
	files := map[string]string{
		"src/Store.java":   "public interface Store {\n    void save(Order o);\n}\n",
		"src/DbStore.java": "public class DbStore implements Store {\n    public void save(Order o) {\n    }\n}\n",
		"src/Use.java":     "public class Use {\n    void persist(Store store) {\n        store.save(null);\n    }\n}\n",
	}
	r, c := localCheckout(t, files, "")
	res := trace(t, r, c, ImpactQuery{Seeds: []Symbol{seed("Store", "src/Store.java", RankSignature)}, Depth: 1})
	i := nodeNamed(res, "DbStore")
	j := nodeNamed(res, "persist")
	if i < 0 || j < 0 {
		t.Fatalf("nodes missing:%s", describe(res))
	}
	if res.Nodes[i].Kind != KindInherits || res.Nodes[j].Kind != KindCall {
		t.Fatalf("kinds wrong:%s", describe(res))
	}
}

// TestTraceThroughChangedFile is canary (f2): a use in a changed file
// outside its hunks is a node marked InPR and followed; a use inside a
// hunk is not a node.
func TestTraceThroughChangedFile(t *testing.T) {
	files := map[string]string{
		"src/Repo.java": "public class Repo {\n    public void save(Order o) {\n    }\n}\n",
		// Lines 2-4 are the hunk; line 7 uses save outside it.
		"src/Service.java": "public class Service {\n    void archive(Order o) {\n        repo.save(o);\n    }\n\n    void persist(Order o) {\n        repo.save(o);\n    }\n}\n",
		"src/Web.java":     "public class Web {\n    void submit(Order o) {\n        service.persist(o);\n    }\n}\n",
	}
	r, c := localCheckout(t, files, "")
	res := trace(t, r, c, ImpactQuery{
		Seeds: []Symbol{seed("save", "src/Repo.java", RankSignature)},
		Hunks: map[string][]LineRange{"src/Repo.java": {{From: 2, To: 3}}, "src/Service.java": {{From: 2, To: 4}}},
		Depth: 2,
	})
	if nodeNamed(res, "archive") >= 0 {
		t.Fatalf("a use inside a hunk became a node:%s", describe(res))
	}
	p := nodeNamed(res, "persist")
	if p < 0 || !res.Nodes[p].InPR || res.Nodes[p].UseLine != 7 {
		t.Fatalf("use outside the hunks missing or unmarked:%s", describe(res))
	}
	s := nodeNamed(res, "submit")
	if s < 0 || res.Nodes[s].Parent != p || res.Nodes[s].InPR {
		t.Fatalf("changed-file node not followed:%s", describe(res))
	}
}

// bodyOnlyPatch changes one line inside OrderService.persist; git's hunk
// header names the class, as it does for an indented Java method.
const bodyOnlyPatch = "@@ -9,5 +9,5 @@ public class OrderService {\n \n     public void persist(Order order) {\n-        check(order);\n+        validate(order);\n         repository.save(order);\n"

// TestTraceBodyOnlySeed is canary (f3): a change only inside persist's body
// seeds persist and finds its caller.
func TestTraceBodyOnlySeed(t *testing.T) {
	r, c := localCheckout(t, javaChain, "")
	files := []provider.FilePatch{{Path: servicePath, Type: provider.ChangeModified, Patch: bodyOnlyPatch}}
	seeds := ExtractSymbols(files, 0)
	if slices.ContainsFunc(seeds, func(s Symbol) bool { return s.Name == "persist" }) {
		t.Fatalf("fixture: ExtractSymbols already names persist: %+v", seeds)
	}
	body := BodyHunkLines(files)
	if !slices.Equal(body[servicePath], []int{11}) {
		t.Fatalf("body lines: %v", body)
	}
	res := trace(t, r, c, ImpactQuery{Seeds: seeds, BodyLines: body, Hunks: map[string][]LineRange{servicePath: {{From: 9, To: 13}}}, Depth: 2})
	p := nodeNamed(res, "persist")
	if p < 0 || res.Nodes[p].Hop != 0 || res.Nodes[p].DefLine != 10 || res.Nodes[p].Rank != RankChanged || res.BodySeeds != 1 {
		t.Fatalf("body-only change gave no persist seed:%s", describe(res))
	}
	s := nodeNamed(res, "submit")
	if s < 0 || res.Nodes[s].Parent != p || res.Nodes[s].Hop != 1 {
		t.Fatalf("caller of the body-only seed missing:%s", describe(res))
	}
}

// ---- more tests ----

func TestTraceSeedOrder(t *testing.T) {
	r, c := localCheckout(t, javaChain, "")
	files := []provider.FilePatch{{Path: servicePath, Type: provider.ChangeModified, Patch: bodyOnlyPatch}}
	// In rank order, as ExtractSymbols gives them.
	seeds := []Symbol{
		seed("store", repoPath, RankRemoved),
		seed("Repo", repoPath, RankSignature),
		seed("CheckoutController", controllerPath, RankChanged),
		{Name: "brandNew", Path: repoPath, Rank: RankChanged, New: true},
		{Name: "testOnly", Path: "src/test/java/RepoTest.java", Rank: RankRemoved, Test: true},
		seed("persist", servicePath, RankChanged),
	}
	res := trace(t, r, c, ImpactQuery{Seeds: seeds[:5], BodyLines: BodyHunkLines(files), Depth: 1})
	var got []string
	for _, n := range res.Nodes[:res.Seeds] {
		got = append(got, n.Name)
	}
	if want := []string{"store", "Repo", "persist", "CheckoutController"}; !slices.Equal(got, want) {
		t.Fatalf("seed order %v, want %v", got, want)
	}
	// A body definition that is a seed already is not added again.
	res = trace(t, r, c, ImpactQuery{Seeds: seeds, BodyLines: BodyHunkLines(files), Depth: 1})
	if res.BodySeeds != 0 || res.Seeds != 4 {
		t.Fatalf("duplicate body seed: %d body, %d seeds", res.BodySeeds, res.Seeds)
	}
}

func TestTraceBudgetEntryAndLeaves(t *testing.T) {
	files := map[string]string{
		"lib/core.py":  "def compute():\n    return 1\n",
		"app/a.py":     "def first():\n    compute()\n",
		"app/b.py":     "def second():\n    compute()\n",
		"app/main.py":  "compute()\n",
		"api/views.py": "def view():\n    first()\n",
	}
	r, c := localCheckout(t, files, "")
	q := ImpactQuery{Seeds: []Symbol{seed("compute", "lib/core.py", RankSignature)}, Depth: 2, Scope: ImpactScope{EntryPoints: []string{"api/**"}}}
	res := trace(t, r, c, q)
	leaf := slices.IndexFunc(res.Nodes, func(n ImpactNode) bool { return n.Path == "app/main.py" })
	if leaf < 0 || res.Nodes[leaf].Name != "main.py" || res.Nodes[leaf].NotFollowed != NotFollowedDepth || res.Nodes[leaf].DefLine != 0 {
		t.Fatalf("file-level use is not a leaf:%s", describe(res))
	}
	v := nodeNamed(res, "view")
	if v < 0 || !res.Nodes[v].Entry || res.EntryPointsReached != 1 {
		t.Fatalf("entry point not marked:%s", describe(res))
	}
	q.MaxLinks = 2
	res = trace(t, r, c, q)
	if res.Links != 2 || res.NotFollowedBy[NotFollowedBudget] == 0 || res.DepthReached != 1 {
		t.Fatalf("budget not applied:%s\n%+v", describe(res), res.NotFollowedBy)
	}
	for _, n := range res.Nodes[1:] {
		if n.NotFollowed == "" {
			t.Fatalf("unsearched node left unmarked:%s", describe(res))
		}
	}
}

func TestTraceRecursionIsNotACaller(t *testing.T) {
	r, c := localCheckout(t, map[string]string{
		"pkg/walk.go": "package pkg\n\nfunc Walk(n int) int {\n\tif n == 0 {\n\t\treturn 0\n\t}\n\treturn Walk(n - 1)\n}\n",
		"pkg/use.go":  "package pkg\n\nfunc Count() int {\n\treturn Walk(3)\n}\n",
	}, "")
	res := trace(t, r, c, ImpactQuery{Seeds: []Symbol{seed("Walk", "pkg/walk.go", RankSignature)}, Depth: 2})
	if len(res.Nodes) != 2 || res.Nodes[1].Name != "Count" {
		t.Fatalf("recursion became a node:%s", describe(res))
	}
}

func TestTraceInputs(t *testing.T) {
	r, c := localCheckout(t, javaChain, "")
	// No seed: no git work and no error.
	if res, err := r.Trace(context.Background(), c, ImpactQuery{}); err != nil || len(res.Nodes) != 0 {
		t.Fatalf("empty query: %+v %v", res, err)
	}
	// Only test and new symbols: nothing to trace.
	q := ImpactQuery{Seeds: []Symbol{{Name: "save", Path: repoPath, New: true}, {Name: "persist", Path: "src/test/X.java", Test: true}}}
	if res, err := r.Trace(context.Background(), c, q); err != nil || len(res.Nodes) != 0 {
		t.Fatalf("test and new seeds traced: %+v %v", res, err)
	}
	for _, sc := range []ImpactScope{{Paths: []string{"/abs"}}, {ExcludePaths: []string{"a//b"}}, {Paths: []string{"a\nb"}}} {
		if _, err := r.Trace(context.Background(), c, ImpactQuery{Seeds: []Symbol{seed("save", repoPath, RankRemoved)}, Scope: sc}); err == nil {
			t.Fatalf("scope %+v accepted", sc)
		}
	}
	if _, err := r.Trace(context.Background(), Checkout{GitDir: c.GitDir, HeadSHA: "nope"}, ImpactQuery{Seeds: []Symbol{seed("save", repoPath, RankRemoved)}}); err == nil {
		t.Fatal("bad head accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Trace(ctx, c, ImpactQuery{Seeds: []Symbol{seed("save", repoPath, RankRemoved)}}); err == nil {
		t.Fatal("canceled context accepted")
	}
	// Bounds are clamped: depth 9 is 3.
	res := trace(t, r, c, ImpactQuery{Seeds: []Symbol{seed("save", repoPath, RankRemoved)}, Depth: 9, MaxCallersPerNode: 99, MaxLinks: 999})
	if res.DepthReached != 2 || nodeNamed(res, "submit") < 0 {
		t.Fatalf("clamped trace:%s", describe(res))
	}
}

// TestTraceLogsCountsOnly: the debug record carries no name, path or text.
func TestTraceLogsCountsOnly(t *testing.T) {
	r, c := localCheckout(t, javaChain, "")
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	res := trace(t, r, c, ImpactQuery{Seeds: []Symbol{seed("save", repoPath, RankSignature)}, Depth: 2, Log: log})
	out := buf.String()
	if !strings.Contains(out, "links=2") || !strings.Contains(out, "seeds=1") {
		t.Fatalf("log lacks counts: %s", out)
	}
	for _, n := range res.Nodes {
		for _, s := range []string{n.Name, n.Path, n.DefText, n.UseText} {
			if s != "" && strings.Contains(out, s) {
				t.Fatalf("log leaks %q: %s", s, out)
			}
		}
	}
	for _, s := range []string{"example", "java", "Repo", "repository"} {
		if strings.Contains(out, s) {
			t.Fatalf("log leaks %q: %s", s, out)
		}
	}
}

func TestBodyHunkLines(t *testing.T) {
	fp := func(path, p string) provider.FilePatch {
		return provider.FilePatch{Path: path, Type: provider.ChangeModified, Patch: p}
	}
	files := []provider.FilePatch{
		fp("a/Service.java", bodyOnlyPatch),
		// A hunk that defines a symbol is not body-only; the second hunk is.
		fp("a/Two.java", "@@ -1,3 +1,3 @@\n class Two {\n-    void old() {}\n+    void renamed() {}\n }\n@@ -20,4 +20,4 @@ class Two {\n     void run() {\n \n-        a();\n+        b();\n"),
		// Removal only: the head line just before it.
		fp("b/svc.go", "@@ -5,4 +5,3 @@ func Persist() error {\n \tx := 1\n-\tcheck(x)\n \treturn nil\n }\n"),
		// Blank added line first: the first non-blank one.
		fp("c/mod.py", "@@ -3,3 +3,5 @@ def persist(order):\n     a = 1\n+\n+    audit(a)\n     return a\n"),
		fp("c/test_mod.py", "@@ -1,2 +1,2 @@\n def check():\n-    a()\n+    b()\n"),
		{Path: "d/Gone.java", Type: provider.ChangeDeleted, Patch: "@@ -1,2 +0,0 @@\n-a();\n-b();\n"},
		fp("docs/guide.md", "@@ -1,1 +1,1 @@\n-old\n+new\n"),
		{Path: "e/blob.bin", Type: provider.ChangeModified, Binary: true},
	}
	got := BodyHunkLines(files)
	want := map[string][]int{"a/Service.java": {11}, "a/Two.java": {22}, "b/svc.go": {5}, "c/mod.py": {5}}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if !slices.Equal(got[k], v) {
			t.Fatalf("%s: got %v, want %v (all %v)", k, got[k], v, got)
		}
	}
}

func TestLinkKind(t *testing.T) {
	tests := []struct {
		line, name, lang, want string
	}{
		{"public class DbStore implements Store {", "Store", "Java", KindInherits},
		{"public class Cache extends Base {", "Base", "Java", KindInherits},
		{"public override void Save(Order o)", "Save", "C#", KindInherits},
		{"override fun save(o: Order) {", "save", "Kotlin", KindInherits},
		{"public class Resolver : IResolver", "IResolver", "C#", KindInherits},
		{"class Outer(private val repo: Repo) : Base(repo) {", "Base", "Kotlin", KindInherits},
		{"class Outer(private val repo: Repo) : Base(repo) {", "Repo", "Kotlin", KindCall},
		{"class Child(Parent):", "Parent", "Python", KindInherits},
		{"public void save(Order o) {", "save", "Java", KindInherits},
		{"repository.save(order);", "save", "Java", KindCall},
		{"Store store = new DbStore();", "DbStore", "Java", KindCall},
		{"label: \"Store\"", "Store", "TypeScript", KindCall},
	}
	for _, tc := range tests {
		if got := linkKind(tc.line, tc.name, tc.lang); got != tc.want {
			t.Errorf("linkKind(%q, %q) = %s, want %s", tc.line, tc.name, got, tc.want)
		}
	}
}

func TestHasWordAndSkipParens(t *testing.T) {
	if !hasWord("a Store b", "Store") || hasWord("Stores", "Store") || hasWord("myStore", "Store") || !hasWord("x,Store", "Store") {
		t.Fatal("hasWord")
	}
	if got := skipParens("<T>(a: A, f: (B) -> C) : Base"); got != " : Base" {
		t.Fatalf("skipParens = %q", got)
	}
	if got := skipParens("(unbalanced"); got != "(unbalanced" {
		t.Fatalf("skipParens unbalanced = %q", got)
	}
}
