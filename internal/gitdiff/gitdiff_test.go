package gitdiff

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// diff joins lines with "\n" and terminates the result with one.
func diff(lines ...string) string { return strings.Join(lines, "\n") + "\n" }

func TestParse(t *testing.T) {
	t.Parallel()

	simpleHunk := diff("@@ -1,3 +1,3 @@", " one", "-two", "+TWO", " three")

	tests := []struct {
		name string
		in   string
		want []File
	}{
		{
			name: "empty input",
			in:   "",
			want: nil,
		},
		{
			name: "simple modification",
			in: diff(
				"diff --git a/src/main.go b/src/main.go",
				"index 1111111..2222222 100644",
				"--- a/src/main.go",
				"+++ b/src/main.go",
			) + simpleHunk,
			want: []File{{Path: "src/main.go", Type: Modified, Patch: simpleHunk, Additions: 1, Deletions: 1}},
		},
		{
			name: "path containing space-b-slash with hunks",
			in: diff(
				"diff --git a/dir b/file.txt b/dir b/file.txt",
				"index 1111111..2222222 100644",
				"--- a/dir b/file.txt\t",
				"+++ b/dir b/file.txt\t",
			) + simpleHunk,
			want: []File{{Path: "dir b/file.txt", Type: Modified, Patch: simpleHunk, Additions: 1, Deletions: 1}},
		},
		{
			name: "path containing space-b-slash taken from diff header only (mode-only)",
			in: diff(
				"diff --git a/dir b/file.txt b/dir b/file.txt",
				"old mode 100644",
				"new mode 100755",
			),
			want: []File{{Path: "dir b/file.txt", Type: Modified}},
		},
		{
			name: "path with a space",
			in: diff(
				"diff --git a/my file.txt b/my file.txt",
				"index 1111111..2222222 100644",
				"--- a/my file.txt\t",
				"+++ b/my file.txt\t",
			) + simpleHunk,
			want: []File{{Path: "my file.txt", Type: Modified, Patch: simpleHunk, Additions: 1, Deletions: 1}},
		},
		{
			name: "unicode path with octal escapes",
			in: diff(
				`diff --git "a/\303\244/\303\266.txt" "b/\303\244/\303\266.txt"`,
				"index 1111111..2222222 100644",
				`--- "a/\303\244/\303\266.txt"`,
				`+++ "b/\303\244/\303\266.txt"`,
			) + simpleHunk,
			want: []File{{Path: "ä/ö.txt", Type: Modified, Patch: simpleHunk, Additions: 1, Deletions: 1}},
		},
		{
			name: "unicode path in header only (mode-only)",
			in: diff(
				`diff --git "a/\303\244.txt" "b/\303\244.txt"`,
				"old mode 100644",
				"new mode 100755",
			),
			want: []File{{Path: "ä.txt", Type: Modified}},
		},
		{
			name: "path with tab and quote chars is quoted by git",
			in: diff(
				`diff --git "a/x\ty \"q\".txt" "b/x\ty \"q\".txt"`,
				"index 1111111..2222222 100644",
				`--- "a/x\ty \"q\".txt"`,
				`+++ "b/x\ty \"q\".txt"`,
			) + simpleHunk,
			want: []File{{Path: "x\ty \"q\".txt", Type: Modified, Patch: simpleHunk, Additions: 1, Deletions: 1}},
		},
		{
			name: "rename without content change",
			in: diff(
				"diff --git a/old name.txt b/new name.txt",
				"similarity index 100%",
				"rename from old name.txt",
				"rename to new name.txt",
			),
			want: []File{{Path: "new name.txt", OldPath: "old name.txt", Type: Renamed}},
		},
		{
			name: "rename with content change",
			in: diff(
				"diff --git a/old.go b/pkg/new.go",
				"similarity index 80%",
				"rename from old.go",
				"rename to pkg/new.go",
				"index 1111111..2222222 100644",
				"--- a/old.go",
				"+++ b/pkg/new.go",
			) + simpleHunk,
			want: []File{{Path: "pkg/new.go", OldPath: "old.go", Type: Renamed, Patch: simpleHunk, Additions: 1, Deletions: 1}},
		},
		{
			name: "quoted rename paths",
			in: diff(
				`diff --git "a/\303\244.txt" "b/\303\266.txt"`,
				"similarity index 100%",
				`rename from "\303\244.txt"`,
				`rename to "\303\266.txt"`,
			),
			want: []File{{Path: "ö.txt", OldPath: "ä.txt", Type: Renamed}},
		},
		{
			name: "copy is reported as added",
			in: diff(
				"diff --git a/orig.txt b/copy.txt",
				"similarity index 100%",
				"copy from orig.txt",
				"copy to copy.txt",
			),
			want: []File{{Path: "copy.txt", Type: Added}},
		},
		{
			name: "deleted file keeps the old path",
			in: diff(
				"diff --git a/gone.txt b/gone.txt",
				"deleted file mode 100644",
				"index 1111111..0000000",
				"--- a/gone.txt",
				"+++ /dev/null",
				"@@ -1,2 +0,0 @@",
				"-bye",
				"-bye again",
			),
			want: []File{{
				Path: "gone.txt", Type: Deleted,
				Patch:     diff("@@ -1,2 +0,0 @@", "-bye", "-bye again"),
				Deletions: 2,
			}},
		},
		{
			name: "new file with content",
			in: diff(
				"diff --git a/fresh.txt b/fresh.txt",
				"new file mode 100644",
				"index 0000000..1111111",
				"--- /dev/null",
				"+++ b/fresh.txt",
				"@@ -0,0 +1 @@",
				"+hello",
			),
			want: []File{{
				Path: "fresh.txt", Type: Added,
				Patch:     diff("@@ -0,0 +1 @@", "+hello"),
				Additions: 1,
			}},
		},
		{
			name: "new empty file",
			in: diff(
				"diff --git a/empty.txt b/empty.txt",
				"new file mode 100644",
				"index 0000000..e69de29",
			),
			want: []File{{Path: "empty.txt", Type: Added}},
		},
		{
			name: "binary file",
			in: diff(
				"diff --git a/img.png b/img.png",
				"index 1111111..2222222 100644",
				"Binary files a/img.png and b/img.png differ",
			),
			want: []File{{Path: "img.png", Type: Modified, Binary: true}},
		},
		{
			name: "new binary file",
			in: diff(
				"diff --git a/img.png b/img.png",
				"new file mode 100644",
				"index 0000000..2222222",
				"Binary files /dev/null and b/img.png differ",
			),
			want: []File{{Path: "img.png", Type: Added, Binary: true}},
		},
		{
			name: "git binary patch",
			in: diff(
				"diff --git a/blob.bin b/blob.bin",
				"index 1111111..2222222 100644",
				"GIT binary patch",
				"literal 5",
				"Gc${NkU|@@@@@@@@",
				"",
				"literal 0",
				"HcmV?d00001",
				"",
				"diff --git a/next.txt b/next.txt",
				"index 1111111..2222222 100644",
				"--- a/next.txt",
				"+++ b/next.txt",
			) + simpleHunk,
			want: []File{
				{Path: "blob.bin", Type: Modified, Binary: true},
				{Path: "next.txt", Type: Modified, Patch: simpleHunk, Additions: 1, Deletions: 1},
			},
		},
		{
			name: "mode-only change",
			in: diff(
				"diff --git a/run.sh b/run.sh",
				"old mode 100644",
				"new mode 100755",
			),
			want: []File{{Path: "run.sh", Type: Modified}},
		},
		{
			name: "two files where the first lacks a trailing newline",
			in: diff(
				"diff --git a/a.txt b/a.txt",
				"index 1111111..2222222 100644",
				"--- a/a.txt",
				"+++ b/a.txt",
				"@@ -1 +1 @@",
				"-old",
				`\ No newline at end of file`,
				"+new",
				`\ No newline at end of file`,
				"diff --git a/b.txt b/b.txt",
				"index 3333333..4444444 100644",
				"--- a/b.txt",
				"+++ b/b.txt",
				"@@ -1 +1,2 @@",
				" keep",
				"+more",
			),
			want: []File{
				{
					Path: "a.txt", Type: Modified, Additions: 1, Deletions: 1,
					Patch: diff("@@ -1 +1 @@", "-old", `\ No newline at end of file`, "+new", `\ No newline at end of file`),
				},
				{
					Path: "b.txt", Type: Modified, Additions: 1,
					Patch: diff("@@ -1 +1,2 @@", " keep", "+more"),
				},
			},
		},
		{
			name: "last file without any trailing newline at end of input",
			in:   "diff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n@@ -1 +1 @@\n-x\n+y",
			want: []File{{Path: "a.txt", Type: Modified, Patch: "@@ -1 +1 @@\n-x\n+y", Additions: 1, Deletions: 1}},
		},
		{
			name: "CRLF content lines are preserved byte-exact",
			in: "diff --git a/win.txt b/win.txt\n--- a/win.txt\n+++ b/win.txt\n" +
				"@@ -1,2 +1,2 @@\n keep\r\n-old\r\n+new\r\n",
			want: []File{{
				Path: "win.txt", Type: Modified, Additions: 1, Deletions: 1,
				Patch: "@@ -1,2 +1,2 @@\n keep\r\n-old\r\n+new\r\n",
			}},
		},
		{
			name: "multiple hunks are summed",
			in: diff(
				"diff --git a/m.txt b/m.txt",
				"--- a/m.txt",
				"+++ b/m.txt",
				"@@ -1,2 +1,3 @@",
				" a",
				"+b",
				" c",
				"@@ -10,2 +11,1 @@ func context()",
				"-x",
				" y",
			),
			want: []File{{
				Path: "m.txt", Type: Modified, Additions: 1, Deletions: 1,
				Patch: diff("@@ -1,2 +1,3 @@", " a", "+b", " c", "@@ -10,2 +11,1 @@ func context()", "-x", " y"),
			}},
		},
		{
			name: "added line that looks like a diff header is content",
			in: diff(
				"diff --git a/x b/x",
				"--- a/x",
				"+++ b/x",
				"@@ -0,0 +1,3 @@",
				"+diff --git a/x b/x",
				"+--- a/x",
				"++++ b/x",
				"diff --git a/y b/y",
				"--- a/y",
				"+++ b/y",
				"@@ -1 +1 @@",
				"-a",
				"+b",
			),
			want: []File{
				{
					Path: "x", Type: Modified, Additions: 3,
					Patch: diff("@@ -0,0 +1,3 @@", "+diff --git a/x b/x", "+--- a/x", "++++ b/x"),
				},
				{Path: "y", Type: Modified, Additions: 1, Deletions: 1, Patch: diff("@@ -1 +1 @@", "-a", "+b")},
			},
		},
		{
			name: "context line that looks like a diff header is content",
			in: diff(
				"diff --git a/x b/x",
				"--- a/x",
				"+++ b/x",
				"@@ -1,2 +1,2 @@",
				" diff --git a/x b/x",
				"-old",
				"+new",
			),
			want: []File{{
				Path: "x", Type: Modified, Additions: 1, Deletions: 1,
				Patch: diff("@@ -1,2 +1,2 @@", " diff --git a/x b/x", "-old", "+new"),
			}},
		},
		{
			name: "deleted line that looks like a diff header is content",
			in: diff(
				"diff --git a/x b/x",
				"--- a/x",
				"+++ b/x",
				"@@ -1 +0,0 @@",
				"-diff --git a/x b/x",
			),
			want: []File{{
				Path: "x", Type: Modified, Deletions: 1,
				Patch: diff("@@ -1 +0,0 @@", "-diff --git a/x b/x"),
			}},
		},
		{
			name: "preamble before the first file is skipped",
			in: diff(
				"From: someone",
				"Subject: [PATCH] placeholder",
				"",
				"diff --git a/a b/a",
				"old mode 100644",
				"new mode 100755",
			),
			want: []File{{Path: "a", Type: Modified}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Parse(strings.NewReader(tt.in))
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Parse() mismatch\n got: %#v\nwant: %#v", got, tt.want)
			}
		})
	}
}

// TestParseHunkToZeroStaysModifiedWithoutDeleteMarkers documents that a hunk
// removing all content of a file is still "modified" unless the header says
// otherwise.
func TestParseHunkToZeroStaysModifiedWithoutDeleteMarkers(t *testing.T) {
	t.Parallel()
	got, err := Parse(strings.NewReader(diff(
		"diff --git a/x b/x", "--- a/x", "+++ b/x", "@@ -1 +0,0 @@", "-a")))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Type != Modified {
		t.Fatalf("got %#v, want one modified file", got)
	}
}

func TestParseErrors(t *testing.T) {
	t.Parallel()

	const secret = "SECRET-CONTENT-MARKER"
	hdr := diff("diff --git a/bad.txt b/bad.txt", "--- a/bad.txt", "+++ b/bad.txt")

	tests := []struct {
		name     string
		in       string
		wantPath string // must appear in the error; empty means not checked
	}{
		{"hunk header with bad numbers", hdr + diff("@@ -x,2 +1,2 @@", " "+secret), "bad.txt"},
		{"hunk header missing closing marker", hdr + diff("@@ -1,2 +1,2", " "+secret), "bad.txt"},
		{"hunk header with negative count", hdr + diff("@@ -1,-2 +1,2 @@", " "+secret), "bad.txt"},
		{"hunk with too few lines at end of input", hdr + diff("@@ -1,3 +1,3 @@", " "+secret), "bad.txt"},
		{"hunk with too many deletions", hdr + diff("@@ -1,1 +1,1 @@", "-"+secret, "-"+secret), "bad.txt"},
		{"hunk with too many additions", hdr + diff("@@ -1,1 +1,1 @@", "+"+secret, "+"+secret), "bad.txt"},
		{"hunk line with invalid prefix", hdr + diff("@@ -1,2 +1,2 @@", "?"+secret, " x"), "bad.txt"},
		{"hunk with empty line instead of context", hdr + diff("@@ -1,2 +1,2 @@", "", " x"), "bad.txt"},
		{"text after a complete hunk", hdr + diff("@@ -1 +1 @@", "-a", "+b", secret), "bad.txt"},
		{"invalid quoted path in rename", diff("diff --git a/x b/y", "rename from x", `rename to "bad\q"`), ""},
		{"invalid quoted path in diff header", diff(`diff --git "a/bad\q" "b/bad\q"`, "old mode 100644"), ""},
		{"invalid quoted path in plus line", diff("diff --git a/x b/x", "--- a/x", `+++ "b/bad\q"`, "@@ -1 +1 @@", "-a", "+b"), ""},
		{"unterminated quote in diff header", diff(`diff --git "a/x b/x`, "old mode 100644"), ""},
		{"no determinable path", diff("diff --git a/one b/two", "old mode 100644"), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Parse(strings.NewReader(tt.in))
			if err == nil {
				t.Fatalf("Parse() = %#v, want error", got)
			}
			if got != nil {
				t.Errorf("Parse() files = %#v on error, want nil", got)
			}
			if strings.Contains(err.Error(), secret) {
				t.Errorf("error echoes diff content: %v", err)
			}
			if tt.wantPath != "" && !strings.Contains(err.Error(), tt.wantPath) {
				t.Errorf("error %q does not name path %q", err, tt.wantPath)
			}
		})
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

func TestParseReadError(t *testing.T) {
	t.Parallel()
	if _, err := Parse(errReader{}); err == nil {
		t.Fatal("Parse() error = nil, want read error")
	}
}

// naiveHeaderPath derives the path from a "diff --git" line by splitting on
// " b/", the approach of the upstream implementation. It is wrong for paths
// that contain " b/" and exists only to prove that the tests below catch it.
func naiveHeaderPath(rest string) string {
	_, newPath, _ := strings.Cut(rest, " b/")
	return newPath
}

// TestNaiveSplitFailsOnSpaceBSlashPath is the [canary] for the " b/" case:
// it shows the naive derivation produces a wrong path for the inputs on which
// Parse is required to be right.
func TestNaiveSplitFailsOnSpaceBSlashPath(t *testing.T) {
	t.Parallel()
	const want = "dir b/file.txt"
	line := "a/dir b/file.txt b/dir b/file.txt"

	if got := naiveHeaderPath(line); got == want {
		t.Fatalf("naive split unexpectedly returned the right path %q; canary is void", got)
	}

	files, err := Parse(strings.NewReader(diff("diff --git "+line, "old mode 100644", "new mode 100755")))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Path != want {
		t.Fatalf("Parse() = %#v, want path %q", files, want)
	}
}

func TestParseReturnsFilesInInputOrder(t *testing.T) {
	t.Parallel()
	in := diff(
		"diff --git a/1 b/1", "old mode 100644", "new mode 100755",
		"diff --git a/2 b/2", "old mode 100644", "new mode 100755",
		"diff --git a/3 b/3", "old mode 100644", "new mode 100755",
	)
	got, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, f := range got {
		paths = append(paths, f.Path)
	}
	if want := []string{"1", "2", "3"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
}
