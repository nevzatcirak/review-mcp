package main

import (
	"fmt"
	"net/http"
	"strings"
)

// Fixture pull requests for diag diff, served by newFakeGitea next to PR 7:
//
//	PR 8: several files in two languages (Go, TypeScript), an added, a
//	      deleted and a renamed file, plus a lockfile, a vendor/ file and an
//	      image that the file filter must drop.
//	PR 9: one Go file far larger than a 4096-token window.
//
// Every file has real base and head content so that context extension runs
// on the fast path. The content never contains diagMarker.
const diffBodyMarker = "FAKE-DIFF-BODY-MARKER-QX42-stdout-only"

type fxFile struct {
	path, oldPath, status string // gitea status: added, changed, deleted, renamed
	diff                  string // one "diff --git" section
	base, head            string // contents; empty when not applicable
}

type fxPR struct{ files []fxFile }

func fxLine(path string, i int, bulk int) string {
	return fmt.Sprintf("// %s line %d %s", path, i, strings.Repeat("alpha beta gamma ", bulk))
}

func fxLines(path string, n, bulk int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fxLine(path, i+1, bulk)
	}
	return out
}

func joinLines(ls []string) string { return strings.Join(ls, "\n") + "\n" }

func prefixed(p string, ls []string) string {
	var b strings.Builder
	for _, l := range ls {
		b.WriteString(p + l + "\n")
	}
	return b.String()
}

// fxModified builds a modified file of n lines in which line `at` is replaced
// and `extra` lines are added after it. marker, when set, is the text of the
// replacement line.
func fxModified(path string, n, at, extra, bulk int, marker string) fxFile {
	base := fxLines(path, n, bulk)
	head := append([]string(nil), base[:at-1]...)
	repl := "// CHANGED " + path
	if marker != "" {
		repl += " " + marker
	}
	head = append(head, repl)
	for k := 0; k < extra; k++ {
		head = append(head, fxLine(path+"+", k+1, bulk))
	}
	head = append(head, base[at:]...)
	// Hunk: 3 lines of context on each side.
	first := at - 3
	oldLen, newLen := 7, 7+extra
	var hunk strings.Builder
	fmt.Fprintf(&hunk, "@@ -%d,%d +%d,%d @@\n", first, oldLen, first, newLen)
	hunk.WriteString(prefixed(" ", base[first-1:at-1]))
	hunk.WriteString("-" + base[at-1] + "\n")
	hunk.WriteString("+" + repl + "\n")
	for k := 0; k < extra; k++ {
		hunk.WriteString("+" + fxLine(path+"+", k+1, bulk) + "\n")
	}
	hunk.WriteString(prefixed(" ", base[at:at+3]))
	d := fmt.Sprintf("diff --git a/%[1]s b/%[1]s\nindex 1111111..2222222 100644\n--- a/%[1]s\n+++ b/%[1]s\n%s", path, hunk.String())
	return fxFile{path: path, status: "changed", diff: d, base: joinLines(base), head: joinLines(head)}
}

func fxAdded(path string, n, bulk int) fxFile {
	ls := fxLines(path, n, bulk)
	d := fmt.Sprintf("diff --git a/%[1]s b/%[1]s\nnew file mode 100644\nindex 0000000..3333333\n--- /dev/null\n+++ b/%[1]s\n@@ -0,0 +1,%d @@\n%s", path, n, prefixed("+", ls))
	return fxFile{path: path, status: "added", diff: d, head: joinLines(ls)}
}

func fxDeleted(path string, n, bulk int) fxFile {
	ls := fxLines(path, n, bulk)
	d := fmt.Sprintf("diff --git a/%[1]s b/%[1]s\ndeleted file mode 100644\nindex 4444444..0000000\n--- a/%[1]s\n+++ /dev/null\n@@ -1,%d +0,0 @@\n%s", path, n, prefixed("-", ls))
	return fxFile{path: path, status: "deleted", diff: d, base: joinLines(ls)}
}

func fxRenamed(oldPath, path string, n, bulk int) fxFile {
	f := fxModified(path, n, 5, 0, bulk, "")
	base := fxLines(path, n, bulk)
	f.base = joinLines(base)
	f.oldPath = oldPath
	f.status = "renamed"
	hunk := f.diff[strings.Index(f.diff, "@@"):]
	f.diff = fmt.Sprintf("diff --git a/%s b/%s\nsimilarity index 90%%\nrename from %s\nrename to %s\nindex 5555555..6666666 100644\n--- a/%s\n+++ b/%s\n%s",
		oldPath, path, oldPath, path, oldPath, path, hunk)
	return f
}

func fxFiltered(path string) fxFile {
	f := fxModified(path, 12, 6, 0, 1, "")
	return f
}

func fxImage(path string) fxFile {
	return fxFile{path: path, status: "changed",
		diff: fmt.Sprintf("diff --git a/%[1]s b/%[1]s\nindex 7777777..8888888 100644\nBinary files a/%[1]s and b/%[1]s differ\n", path)}
}

// diffFixtures maps the PR number to its fixture.
func diffFixtures() map[int]fxPR {
	return map[int]fxPR{
		8: {files: []fxFile{
			fxModified("server/app.go", 40, 20, 20, 3, diffBodyMarker),
			fxModified("server/util.go", 30, 15, 6, 3, ""),
			fxAdded("server/fresh.go", 14, 3),
			fxRenamed("server/old_name.go", "server/renamed.go", 20, 2),
			fxDeleted("server/gone.go", 6, 2),
			fxModified("web/index.ts", 30, 15, 8, 3, ""),
			fxFiltered("package-lock.json"),
			fxFiltered("vendor/lib/dep.go"),
			fxImage("assets/logo.png"),
		}},
		9: {files: []fxFile{fxModified("server/huge.go", 300, 150, 150, 3, "")}},
	}
}

// handleDiffFixture serves the fixture PRs (and their raw contents) of the
// fake Gitea host. It reports whether it handled the request.
func handleDiffFixture(w http.ResponseWriter, r *http.Request, api string) bool {
	fx := diffFixtures()
	p := r.URL.Path
	if r.Method == "GET" && strings.HasPrefix(p, api+"/raw/") {
		ref := r.URL.Query().Get("ref")
		for n, pr := range fx {
			if ref != fmt.Sprintf("headsha%d", n) && ref != fmt.Sprintf("mergesha%d", n) {
				continue
			}
			rel := strings.TrimPrefix(p, api+"/raw/")
			for _, f := range pr.files {
				switch {
				case ref == fmt.Sprintf("headsha%d", n) && f.path == rel && f.head != "":
					_, _ = fmt.Fprint(w, f.head)
					return true
				case ref == fmt.Sprintf("mergesha%d", n) && f.status == "renamed" && f.oldPath == rel:
					_, _ = fmt.Fprint(w, f.base)
					return true
				case ref == fmt.Sprintf("mergesha%d", n) && f.status != "renamed" && f.path == rel && f.base != "":
					_, _ = fmt.Fprint(w, f.base)
					return true
				}
			}
			http.NotFound(w, r)
			return true
		}
		return false
	}
	for n, pr := range fx {
		prp := fmt.Sprintf("%s/pulls/%d", api, n)
		switch {
		case r.Method == "GET" && p == prp:
			writeJSONResp(w, map[string]any{
				"title": "Fixture PR", "body": "Description", "state": "open",
				"html_url":   fmt.Sprintf("https://your-gitea.example/octo/demo/pulls/%d", n),
				"merge_base": fmt.Sprintf("mergesha%d", n),
				"user":       map[string]any{"login": "alice"},
				"head":       map[string]any{"ref": "feature", "sha": fmt.Sprintf("headsha%d", n)},
				"base":       map[string]any{"ref": "main", "sha": "basesha"},
			})
		case r.Method == "GET" && p == prp+".diff":
			for _, f := range pr.files {
				_, _ = fmt.Fprint(w, f.diff)
			}
		case r.Method == "GET" && p == prp+"/files":
			if r.URL.Query().Get("page") != "1" {
				_, _ = fmt.Fprint(w, "[]")
				return true
			}
			metas := []any{}
			for _, f := range pr.files {
				metas = append(metas, giteaFileMeta(f.path, f.oldPath, f.status, 1, 1))
			}
			writeJSONResp(w, metas)
		case r.Method == "GET" && p == prp+"/commits":
			writeJSONResp(w, []any{})
		default:
			continue
		}
		return true
	}
	return false
}
