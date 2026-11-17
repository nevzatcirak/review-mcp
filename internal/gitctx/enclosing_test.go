package gitctx

import (
	"strings"
	"testing"
)

// lineOf returns the 1-based line of the first line of src that contains
// needle, or 0.
func lineOf(src, needle string) int {
	for i, l := range strings.Split(src, "\n") {
		if strings.Contains(l, needle) {
			return i + 1
		}
	}
	return 0
}

func TestEnclosingDefinition(t *testing.T) {
	const javaSrc = `package example.app;

import example.app.Repo;

public class OrderService {
    private final Repo repository;
    private final Handler fallback = repository.fallbackHandler();

    public OrderService(Repo repository) {
        this.repository = repository;
    }

    public void persist(Order order) {
        Runnable task = new Runnable() {
            @Override
            public void execute() {
                repository.audit(order);
            }
        };
        orders.forEach(o -> {
            repository.save(o);
        });
        // a comment that mentions repository.flush() by name
        String label = "repository.close()";
        repository.save(order);
    }

    static class Batch {
        void flushAll() {
            repository.flush();
        }
    }

    int twice(int x) { return repository.count(x); }
}
`
	const kotlinSrc = `package example.app

class Outer(private val repo: Repo) : Base(repo) {
    inner class Inner {
        fun persist(order: Order) {
            listOf(order).forEach { repo.save(it) }
        }
    }

    fun submit(order: Order) = repo.store(order)

    companion object {
        val helper = Helper()
    }
}
`
	const csharpSrc = `namespace Example.App
{
    public class Resolver : IResolver
    {
        private readonly IStore store;

        public Item Resolve(string key)
        {
            foreach (var part in key.Split('.'))
            {
                store.Touch(part);
            }
            lock (store)
            {
                store.Lookup(key);
            }
            return store.Find(key);
        }

        private class Cache
        {
            public void Warm() { store.Preload(); }
        }
    }
}
`
	const tsSrc = `import { repo } from "./repo";

export class Checkout {
  submit(cart: Cart): void {
    const items = cart.items.map((i) => repo.price(i));
    repo.store(items);
  }

  async refresh(): Promise<void> {
    await repo.reload();
  }
}

export function total(xs: number[]): number {
  return xs.reduce((a, b) => a + repo.round(b), 0);
}

const quote = (x: number) => repo.quote(x);

repo.connect();
`
	const goSrc = `package app

type Service struct {
	store Store
}

func (s *Service) Persist(o Order) error {
	if err := s.store.Save(o); err != nil {
		return err
	}
	defer func() {
		s.store.Flush()
	}()
	return nil
}

func Total(xs []int) int { return sum(xs) }

var registry = newRegistry()
`
	const pySrc = `import functools


class OrderService:
    """Uses repo.save in a docstring."""

    def __init__(self, repo):
        self.repo = repo

    @functools.lru_cache(maxsize=None)
    def persist(self, order):
        handler = lambda o: self.repo.save(o)
        self.repo.store(order)

    class Nested:
        def flush_all(self):
            repo.flush()


def listing(repo):
    return sorted(repo.items())

repo_default = make_repo()
`
	tests := []struct {
		name, lang, src, use string
		want, wantDef        string // want "" means ok=false
	}{
		// Java.
		{"java method body", "Java", javaSrc, "repository.save(order);", "persist", "public void persist(Order order) {"},
		{"java anonymous class method", "Java", javaSrc, "repository.audit(order);", "execute", "public void execute() {"},
		{"java lambda is not a definition", "Java", javaSrc, "repository.save(o);", "persist", "public void persist(Order order) {"},
		{"java use in a comment", "Java", javaSrc, "repository.flush() by name", "persist", "public void persist(Order order) {"},
		{"java use in a string", "Java", javaSrc, `"repository.close()"`, "persist", "public void persist(Order order) {"},
		{"java nested class method", "Java", javaSrc, "repository.flush();", "flushAll", "void flushAll() {"},
		{"java one-line method", "Java", javaSrc, "repository.count(x)", "twice", "int twice(int x) { return repository.count(x); }"},
		{"java field initializer is the class", "Java", javaSrc, "repository.fallbackHandler()", "OrderService", "public class OrderService {"},
		{"java use on the class line", "Java", javaSrc, "public class OrderService {", "OrderService", "public class OrderService {"},
		{"java import is file level", "Java", javaSrc, "import example.app.Repo;", "", ""},
		// Kotlin.
		{"kotlin inner class method with a lambda", "Kotlin", kotlinSrc, "repo.save(it)", "persist", "fun persist(order: Order) {"},
		{"kotlin expression function", "Kotlin", kotlinSrc, "repo.store(order)", "submit", "fun submit(order: Order) = repo.store(order)"},
		{"kotlin class header", "Kotlin", kotlinSrc, ": Base(repo)", "Outer", "class Outer(private val repo: Repo) : Base(repo) {"},
		// A known limit of the indentation rule (spec §1.1): an unnamed block
		// (companion object, init, static {}) is not a definition, so a use in
		// it goes to the nearest member above with a smaller indentation.
		{"kotlin unnamed companion object goes to the member above", "Kotlin", kotlinSrc, "val helper = Helper()", "submit", "fun submit(order: Order) = repo.store(order)"},
		// C#.
		{"csharp foreach is not a definition", "C#", csharpSrc, "store.Touch(part);", "Resolve", "public Item Resolve(string key)"},
		{"csharp lock is not a definition", "C#", csharpSrc, "store.Lookup(key);", "Resolve", "public Item Resolve(string key)"},
		{"csharp method body", "C#", csharpSrc, "return store.Find(key);", "Resolve", "public Item Resolve(string key)"},
		{"csharp nested class one-line method", "C#", csharpSrc, "store.Preload();", "Warm", "public void Warm() { store.Preload(); }"},
		{"csharp class header", "C#", csharpSrc, "public class Resolver : IResolver", "Resolver", "public class Resolver : IResolver"},
		// TypeScript.
		{"ts method with an arrow callback", "TypeScript", tsSrc, "repo.price(i)", "submit", "submit(cart: Cart): void {"},
		{"ts async method", "TypeScript", tsSrc, "await repo.reload();", "refresh", "async refresh(): Promise<void> {"},
		{"ts exported function", "TypeScript", tsSrc, "repo.round(b)", "total", "export function total(xs: number[]): number {"},
		{"ts arrow const on one line", "TypeScript", tsSrc, "repo.quote(x)", "quote", "const quote = (x: number) => repo.quote(x);"},
		{"ts top level call is file level", "TypeScript", tsSrc, "repo.connect();", "", ""},
		// Go.
		{"go method with an if", "Go", goSrc, "s.store.Save(o)", "Persist", "func (s *Service) Persist(o Order) error {"},
		{"go closure is not a definition", "Go", goSrc, "s.store.Flush()", "Persist", "func (s *Service) Persist(o Order) error {"},
		{"go one-line function", "Go", goSrc, "return sum(xs)", "Total", "func Total(xs []int) int { return sum(xs) }"},
		{"go struct field is the type", "Go", goSrc, "store Store", "Service", "type Service struct {"},
		{"go package var is file level", "Go", goSrc, "newRegistry()", "", ""},
		// Python.
		{"python decorated method", "Python", pySrc, "self.repo.store(order)", "persist", "def persist(self, order):"},
		{"python lambda is not a definition", "Python", pySrc, "self.repo.save(o)", "persist", "def persist(self, order):"},
		{"python docstring is the class", "Python", pySrc, "Uses repo.save in a docstring", "OrderService", "class OrderService:"},
		{"python nested class method", "Python", pySrc, "repo.flush()", "flush_all", "def flush_all(self):"},
		{"python decorator line is the class", "Python", pySrc, "@functools.lru_cache", "OrderService", "class OrderService:"},
		{"python function", "Python", pySrc, "sorted(repo.items())", "listing", "def listing(repo):"},
		{"python module level is file level", "Python", pySrc, "repo_default = make_repo()", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			line := lineOf(tc.src, tc.use)
			if line == 0 {
				t.Fatalf("fixture has no line with %q", tc.use)
			}
			d, ok := EnclosingDefinition([]byte(tc.src), tc.lang, line)
			if tc.want == "" {
				if ok {
					t.Fatalf("line %d: got %+v, want none", line, d)
				}
				return
			}
			if !ok {
				t.Fatalf("line %d: no definition, want %s", line, tc.want)
			}
			if d.Name != tc.want || d.Text != tc.wantDef || d.Line != lineOf(tc.src, tc.wantDef) {
				t.Fatalf("line %d: got %q at %d (%q), want %q at %d (%q)", line, d.Name, d.Line, d.Text, tc.want, lineOf(tc.src, tc.wantDef), tc.wantDef)
			}
		})
	}
}

// TestEnclosingDefinitionMethodNotClass is canary (b): the parent of a use
// inside a method is the method, even when a deeper definition (an
// anonymous class's method) stands between the two.
func TestEnclosingDefinitionMethodNotClass(t *testing.T) {
	const src = `public class Service {
    void persist(Order o) {
        Runnable r = new Runnable() {
            public void execute() { audit(); }
        };
        repo.save(o);
    }
}
`
	d, ok := EnclosingDefinition([]byte(src), "Java", lineOf(src, "repo.save(o);"))
	if !ok || d.Name != "persist" || d.Line != 2 {
		t.Fatalf("got %+v ok=%v, want persist at line 2 (not execute, not Service)", d, ok)
	}
}

func TestEnclosingDefinitionLimits(t *testing.T) {
	var b strings.Builder
	b.WriteString("class Far {\n")
	for range maxEnclosingLookBack {
		b.WriteString("    x = 1\n")
	}
	b.WriteString("    use()\n")
	src := b.String()
	if d, ok := EnclosingDefinition([]byte(src), "Python", lineOf(src, "use()")); ok {
		t.Fatalf("a definition %d lines up was found: %+v", maxEnclosingLookBack+1, d)
	}
	src = "def near():\n    use()\n\n"
	for _, line := range []int{0, -1, 4, 99} {
		if d, ok := EnclosingDefinition([]byte(src), "Python", line); ok {
			t.Fatalf("line %d: got %+v", line, d)
		}
	}
	if d, ok := EnclosingDefinition([]byte(src), "Python", 3); ok {
		t.Fatalf("blank line: got %+v", d)
	}
	if d, ok := EnclosingDefinition(nil, "Python", 1); ok {
		t.Fatalf("empty content: got %+v", d)
	}
	// CRLF content and a tab-indented body.
	src = "func Persist() {\r\n\tsave()\r\n}\r\n"
	if d, ok := EnclosingDefinition([]byte(src), "Go", 2); !ok || d.Name != "Persist" || d.Text != "func Persist() {" {
		t.Fatalf("crlf: got %+v ok=%v", d, ok)
	}
	// A method named Lock is still a definition.
	src = "class Gate {\n    public void Lock() {\n        Close();\n    }\n}\n"
	if d, ok := EnclosingDefinition([]byte(src), "C#", 3); !ok || d.Name != "Lock" {
		t.Fatalf("method Lock: got %+v ok=%v", d, ok)
	}
}
