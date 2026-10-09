package contract

import (
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// applyHunks applies hunk-only unified diff hunks to base. It understands
// "\ No newline at end of file" markers and checks every removed and
// context line against base.
func applyHunks(base, hunks string) (string, error) {
	baseLines := strings.SplitAfter(base, "\n")
	if baseLines[len(baseLines)-1] == "" {
		baseLines = baseLines[:len(baseLines)-1]
	}
	var out []string
	cur, at := 0, -1
	var oldSeg, newSeg []string
	var last byte
	flush := func() error {
		if at < 0 {
			return nil
		}
		if at < cur || at+len(oldSeg) > len(baseLines) {
			return fmt.Errorf("hunk at %d is out of range", at)
		}
		for i, l := range oldSeg {
			if baseLines[at+i] != l {
				return fmt.Errorf("base line %d is %q, the hunk says %q", at+i+1, baseLines[at+i], l)
			}
		}
		out = append(out, baseLines[cur:at]...)
		out = append(out, newSeg...)
		cur, at, oldSeg, newSeg = at+len(oldSeg), -1, nil, nil
		return nil
	}
	strip := func(seg []string) {
		if n := len(seg); n > 0 {
			seg[n-1] = strings.TrimSuffix(seg[n-1], "\n")
		}
	}
	for _, l := range strings.Split(strings.TrimSuffix(hunks, "\n"), "\n") {
		switch {
		case strings.HasPrefix(l, "@@"):
			if err := flush(); err != nil {
				return "", err
			}
			var a, b int
			old, _, _ := strings.Cut(strings.TrimPrefix(l, "@@ -"), " ")
			as, bs, hasLen := strings.Cut(old, ",")
			a, _ = strconv.Atoi(as)
			b = 1
			if hasLen {
				b, _ = strconv.Atoi(bs)
			}
			at = a - 1
			if b == 0 {
				at = a
			}
		case strings.HasPrefix(l, "-"):
			oldSeg, last = append(oldSeg, l[1:]+"\n"), '-'
		case strings.HasPrefix(l, "+"):
			newSeg, last = append(newSeg, l[1:]+"\n"), '+'
		case strings.HasPrefix(l, " "):
			oldSeg, newSeg, last = append(oldSeg, l[1:]+"\n"), append(newSeg, l[1:]+"\n"), ' '
		case l == noNewlineMarker:
			if last != '+' {
				strip(oldSeg)
			}
			if last != '-' {
				strip(newSeg)
			}
		default:
			return "", fmt.Errorf("unexpected hunk line %q", l)
		}
	}
	if err := flush(); err != nil {
		return "", err
	}
	out = append(out, baseLines[cur:]...)
	return strings.Join(out, ""), nil
}

// TestSpecsAreConsistent: the hunks of every spec file turn its base into
// its head, so a provider that diffs contents and one that serves the hunks
// see the same change.
func TestSpecsAreConsistent(t *testing.T) {
	for name, spec := range map[string]Spec{"sample": samplePR(), "file_limit": fileLimitPR()} {
		t.Run(name, func(t *testing.T) {
			ids := map[int64]bool{}
			files := map[string]*File{}
			for i := range spec.Files {
				f := &spec.Files[i]
				files[f.Path] = f
				switch {
				case f.Binary:
					if f.Hunks != "" || !strings.Contains(f.Base+f.Head, "\x00") {
						t.Errorf("%s: a binary file has hunks or no NUL byte", f.Path)
					}
					continue
				case f.TooLarge != (len(f.Head) > spec.Env.MaxFileBytes):
					t.Errorf("%s: TooLarge %v, but head has %d bytes (limit %d)", f.Path, f.TooLarge, len(f.Head), spec.Env.MaxFileBytes)
				case len(f.Base) > spec.Env.MaxFileBytes:
					t.Errorf("%s: base exceeds the file limit", f.Path)
				case (f.Type == provider.ChangeRenamed) != (f.OldPath != ""):
					t.Errorf("%s: OldPath %q for type %s", f.Path, f.OldPath, f.Type)
				}
				got, err := applyHunks(f.Base, f.Hunks)
				if err != nil {
					t.Errorf("%s: %v", f.Path, err)
				} else if got != f.Head {
					t.Errorf("%s: hunks give\n%q\nwant\n%q", f.Path, got, f.Head)
				}
			}
			for _, th := range spec.Threads {
				if len(th.Comments) == 0 || (th.Kind == provider.ThreadGeneral && len(th.Comments) != 1) {
					t.Errorf("thread %+v: want a root, and no replies on a general thread", th)
				}
				if th.Kind == provider.ThreadInline {
					f := files[th.Path]
					if f == nil || f.NewLines()[th.Line] == "" {
						t.Errorf("inline thread on %s:%d is not inside the hunks", th.Path, th.Line)
					}
				}
				for i, c := range th.Comments {
					if ids[c.ID] {
						t.Errorf("comment id %d is used twice", c.ID)
					}
					ids[c.ID] = true
					if i > 0 && !c.Created.After(th.Comments[i-1].Created) {
						t.Errorf("comment %d is not newer than the one before it", c.ID)
					}
				}
			}
		})
	}
}

func TestNewLines(t *testing.T) {
	f := samplePR().Files[0]
	want := map[int]provider.LineType{}
	for _, n := range []int{3, 4, 5, 7, 8, 9} {
		want[n] = provider.LineContext
	}
	want[6] = provider.LineAdded
	if got := f.NewLines(); !maps.Equal(got, want) {
		t.Errorf("NewLines = %v, want %v", got, want)
	}
	if got := (&File{Hunks: "@@ -1 +1 @@\n-a\n+b\n"}).NewLines(); !maps.Equal(got, map[int]provider.LineType{1: provider.LineAdded}) {
		t.Errorf("single-line hunk: NewLines = %v", got)
	}
}

func TestStartServer(t *testing.T) {
	get := func(t *testing.T, u string) (int, string, error) {
		t.Helper()
		resp, err := http.Get(u + "/x") //nolint:gosec // loopback test server
		if err != nil {
			return 0, "", err
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), nil
	}
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })

	t.Run("serves_and_records", func(t *testing.T) {
		pr := Spec{Env: Env{log: &requestLog{}}}
		u := StartServer(t, pr, ok)
		if st, body, err := get(t, u); err != nil || st != 200 || body != "ok" {
			t.Fatalf("GET = %d %q %v", st, body, err)
		}
		if all, w := pr.Env.log.count(); all != 1 || w != 0 || !pr.Env.log.isStarted() {
			t.Errorf("log: %d requests, %d writes, started %v", all, w, pr.Env.log.isStarted())
		}
	})
	t.Run("status_failure_carries_sentinel", func(t *testing.T) {
		pr := Spec{Env: Env{Failure: Failure{Status: 403}}}
		st, body, err := get(t, StartServer(t, pr, ok))
		if err != nil || st != 403 || !strings.Contains(body, Sentinel) {
			t.Errorf("GET = %d %q %v, want 403 with the sentinel", st, body, err)
		}
	})
	t.Run("network_failure", func(t *testing.T) {
		pr := Spec{Env: Env{Failure: Failure{Network: true}}}
		if st, _, err := get(t, StartServer(t, pr, ok)); err == nil {
			t.Errorf("GET = %d, want a transport error", st)
		}
	})
}

func TestUnknownPending(t *testing.T) {
	got := unknownPending(map[string]string{
		"threads": "WP-2k", "errors/ListThreads": "WP-2k", "errors/NoSuchCall": "WP-x", "thread": "WP-x", "errors": "WP-x",
	}, []string{"threads", "errors"})
	if want := []string{"errors/NoSuchCall", "thread"}; !slices.Equal(got, want) {
		t.Errorf("unknownPending = %q, want %q", got, want)
	}
	if got := unknownPending(nil, []string{"threads"}); got != nil {
		t.Errorf("unknownPending(nil) = %q", got)
	}
}
