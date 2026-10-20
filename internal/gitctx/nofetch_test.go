package gitctx

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/cgi" //nolint:gosec // G504: serves git http-backend to the test only
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

// The rule "a read never fetches" (WP-15 §1.2) on every supported git. git
// 2.44 added GIT_NO_LAZY_FETCH (backported to some 2.39-2.43 security
// releases and distribution packages); an older git ignores it and starts a
// lazy fetch from the promisor remote, which only protocol.allow=never then
// stops. This test does not care which guard holds: the promisor remote of
// the checkout is a fixture that counts every connection and could serve
// the missing blobs, and every offline read must leave it untouched and add
// no object.

// noFetchFiles: one present file, one file over the filter, and enough
// missing files with long names that their exclude pathspecs pass
// maxPathspecBytes, so git grep itself meets missing blobs.
func noFetchFiles() map[string]string {
	files := map[string]string{
		"small.go": "package p\n\nfunc Needle() {}\n",
		"big.txt":  "Needle\n" + strings.Repeat("x", 3000) + "\n",
	}
	long := strings.Repeat("n", 90)
	for i := range 200 {
		files[fmt.Sprintf("missing/%s-%03d.txt", long, i)] = fmt.Sprintf("Needle %d\n%s\n", i, strings.Repeat("y", 2000))
	}
	return files
}

// promisorFixture is the upstream of a partial clone, reachable over one
// transport, and the number of connections made to it.
type promisorFixture struct {
	url         string
	uploadPack  string // remote.origin.uploadpack, "" for http
	connections func() int64
}

// fileFixture serves upstream over file://; remote.origin.uploadpack points
// at a wrapper that counts every upload-pack git starts.
func fileFixture(t *testing.T, gitPath, upstream string) promisorFixture {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "connections")
	wrapper := filepath.Join(dir, "upload-pack")
	script := "#!/bin/sh\necho connect >> '" + log + "'\nexec '" + gitPath + "' upload-pack \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil { //nolint:gosec // G306: the wrapper must be executable
		t.Fatal(err)
	}
	return promisorFixture{
		url:        "file://" + filepath.ToSlash(upstream),
		uploadPack: wrapper,
		connections: func() int64 {
			b, err := os.ReadFile(log) //nolint:gosec // G304: the test's own file
			if err != nil {
				return 0
			}
			return int64(strings.Count(string(b), "connect"))
		},
	}
}

// httpFixture serves upstream through git http-backend on 127.0.0.1 and
// counts every request.
func httpFixture(t *testing.T, backend, upstream string) promisorFixture {
	t.Helper()
	h := &cgi.Handler{Path: backend, Env: []string{"GIT_PROJECT_ROOT=" + filepath.Dir(upstream), "GIT_HTTP_EXPORT_ALL=1"}, Stderr: io.Discard}
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return promisorFixture{url: srv.URL + "/" + filepath.Base(upstream), connections: n.Load}
}

// partialClone makes a bare partial clone (blob:limit=1k) of upstream at
// gitDir whose promisor remote is fx.
func partialClone(t *testing.T, gitPath, upstream, gitDir string, fx promisorFixture) {
	t.Helper()
	gitOut(t, "", gitPath, "clone", "-q", "--bare", "--filter=blob:limit=1k", "file://"+filepath.ToSlash(upstream), gitDir)
	gitOut(t, "", gitPath, "--git-dir="+gitDir, "config", "remote.origin.url", fx.url)
	if fx.uploadPack != "" {
		gitOut(t, "", gitPath, "--git-dir="+gitDir, "config", "remote.origin.uploadpack", fx.uploadPack)
	}
}

// objectFiles lists the files under gitDir/objects.
func objectFiles(t *testing.T, gitDir string) []string {
	t.Helper()
	var out []string
	root := filepath.Join(gitDir, "objects")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(out)
	return out
}

func TestReadsNeverFetch(t *testing.T) {
	for _, kind := range []string{"file", "http"} {
		t.Run(kind, func(t *testing.T) {
			gitPath, backend := realGit(t)
			isolateHome(t)
			tmp := t.TempDir()
			src := filepath.Join(tmp, "src")
			if err := os.MkdirAll(src, 0o700); err != nil {
				t.Fatal(err)
			}
			gitOut(t, src, gitPath, "init", "-q")
			writeFiles(t, src, noFetchFiles())
			gitOut(t, src, gitPath, "add", "-A")
			gitOut(t, src, gitPath, "commit", "-q", "-m", "first")
			sha := strings.TrimSpace(gitOut(t, src, gitPath, "rev-parse", "HEAD"))
			bigOID := strings.TrimSpace(gitOut(t, src, gitPath, "rev-parse", "HEAD:big.txt"))
			upstream := filepath.Join(tmp, "srv", "upstream.git")
			gitOut(t, tmp, gitPath, "init", "-q", "--bare", upstream)
			gitOut(t, tmp, gitPath, "--git-dir="+upstream, "config", "uploadpack.allowFilter", "true")
			// git 2.31's http-backend does not take protocol v2 from the CGI
			// request, and protocol v0 serves a lazy fetch (a want by object
			// id) only with this.
			gitOut(t, tmp, gitPath, "--git-dir="+upstream, "config", "uploadpack.allowAnySHA1InWant", "true")
			gitOut(t, src, gitPath, "push", "-q", upstream, "HEAD:refs/heads/main")

			var fx promisorFixture
			if kind == "file" {
				fx = fileFixture(t, gitPath, upstream)
			} else {
				fx = httpFixture(t, backend, upstream)
			}

			// The fixture must see a lazy fetch, or the zero below proves
			// nothing: an unguarded read of a second clone fetches.
			probe := filepath.Join(tmp, "probe.git")
			partialClone(t, gitPath, upstream, probe, fx)
			gitOut(t, "", gitPath, "--git-dir="+probe, "cat-file", "-p", bigOID)
			if fx.connections() == 0 {
				t.Fatalf("%s fixture: an unguarded lazy fetch was not seen", kind)
			}
			seen := fx.connections()

			r, _ := realRunner(t, Options{})
			root, err := r.openRoot(true)
			if err != nil {
				t.Fatal(err)
			}
			entry := filepath.Join(root, "host.example", "_", "ns", "repo")
			if err := os.MkdirAll(entry, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := touch(filepath.Join(entry, markerName)); err != nil {
				t.Fatal(err)
			}
			gitDir := filepath.Join(entry, gitDirName)
			partialClone(t, gitPath, upstream, gitDir, fx)
			co := Checkout{GitDir: gitDir, HeadSHA: sha, Ref: "refs/review-mcp/pr/1"}
			objects := objectFiles(t, gitDir)
			ctx := context.Background()

			reads := []struct {
				name string
				read func() error
			}{
				{"FileAt", func() error {
					_, err := r.FileAt(ctx, co, sha, "big.txt", 1<<20)
					return err
				}},
				{"SearchText", func() error {
					_, err := r.SearchText(ctx, co, TextQuery{Text: "Needle", ContextLines: 1})
					return err
				}},
				{"SearchTerms", func() error {
					_, err := r.SearchTerms(ctx, co, TermQuery{Terms: []Term{{Text: "Needle", Word: true}}, ContextLines: 1})
					return err
				}},
				{"Grep", func() error {
					_, err := r.Grep(ctx, co, Query{Symbols: syms("Needle")})
					return err
				}},
			}
			for _, rd := range reads {
				err := rd.read()
				// The outcome is logged for the version matrix; whether it is
				// classified right is not this test's question.
				t.Logf("%s: reason %q", rd.name, ReasonOf(err))
				if n := fx.connections() - seen; n != 0 {
					t.Errorf("%s: %d connection(s) to the %s promisor remote", rd.name, n, kind)
				}
				if got := objectFiles(t, gitDir); !slices.Equal(got, objects) {
					t.Errorf("%s: the checkout's objects changed: %d files, was %d", rd.name, len(got), len(objects))
				}
			}
		})
	}
}
