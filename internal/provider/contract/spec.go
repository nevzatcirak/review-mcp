package contract

import (
	"strconv"
	"strings"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// OwnMarker marks review-mcp's own review in a Spec: the body of a review
// with Reviewer.Own carries it, and the suite's IsOwn looks for it.
const OwnMarker = "<!-- contract:review-mcp-own -->"

// noNewlineMarker is the git marker line that follows a last line without a
// trailing newline.
const noNewlineMarker = "\\ No newline at end of file"

// Spec describes one synthetic pull request in provider-neutral terms. A
// Fixture serves exactly this data through its fake server; it must not add
// comments, reviews or files of its own.
type Spec struct {
	Title, Description         string
	Author                     User
	SourceBranch, TargetBranch string
	// HeadSHA is the head commit. BaseSHA is the revision the diff is
	// computed against (the merge base); a fixture serves it through the
	// host's preferred base strategy.
	HeadSHA, BaseSHA string
	// Files are the changed files in the host's listing order.
	Files []File
	// Threads are the PR's comment threads, in no particular order.
	Threads []Thread
	// TokenUser is the user the token authenticates as.
	TokenUser User
	Reviewers []Reviewer
	// Draft marks a draft pull request, on a host that has the flag.
	Draft bool
	// Env is the harness side of the Spec: not pull request data.
	Env Env
}

// User is an account of the synthetic host.
type User struct {
	ID          int64
	Login       string
	DisplayName string
}

// File is one changed file.
type File struct {
	// Path is the new path; for a deletion, the old path.
	Path string
	// OldPath is set only for a rename.
	OldPath string
	Type    provider.ChangeType
	// Hunks is the hunk-only unified diff git produces for the file, in the
	// format of provider.FilePatch.Patch: LF line endings and "\ No newline
	// at end of file" markers kept. Empty for a binary file.
	Hunks string
	// Base and Head are the full contents at BaseSHA (at OldPath for a
	// rename) and HeadSHA. Base is empty for an added file, Head for a
	// deleted one.
	Base, Head string
	// Binary marks a file the host reports as binary. Its contents contain a
	// NUL byte.
	Binary bool
	// TooLarge marks a file whose Head is larger than Env.MaxFileBytes.
	TooLarge bool
}

// BasePath is the path of the file at BaseSHA.
func (f *File) BasePath() string {
	if f.OldPath != "" {
		return f.OldPath
	}
	return f.Path
}

// NewLines returns the new-side lines of the hunks that an inline comment
// can be anchored to, with their line type. Any other line is outside the
// diff.
func (f *File) NewLines() map[int]provider.LineType {
	out := map[int]provider.LineType{}
	n := 0
	for _, l := range strings.Split(f.Hunks, "\n") {
		switch {
		case strings.HasPrefix(l, "@@"):
			n = hunkNewStart(l)
		case strings.HasPrefix(l, "+"):
			out[n] = provider.LineAdded
			n++
		case strings.HasPrefix(l, " "):
			out[n] = provider.LineContext
			n++
		}
	}
	return out
}

// hunkNewStart returns c of a "@@ -a,b +c,d @@" header, or 0.
func hunkNewStart(header string) int {
	_, rest, ok := strings.Cut(header, " +")
	if !ok {
		return 0
	}
	end := strings.IndexAny(rest, ", ")
	if end < 0 {
		return 0
	}
	n, err := strconv.Atoi(rest[:end])
	if err != nil {
		return 0
	}
	return n
}

// Thread is one comment thread.
type Thread struct {
	Kind provider.ThreadKind
	// Path and Line (new side) anchor an inline thread.
	Path     string
	Line     int
	Resolved bool
	// Comments holds the root first, then the replies, oldest first. A
	// general thread has a root only: not every host threads PR-level
	// comments.
	Comments []Comment
}

// Comment is one comment of a Thread. IDs are unique across the Spec.
type Comment struct {
	ID      int64
	Author  User
	Body    string
	Created time.Time
}

// Reviewer is one reviewer and the verdict they gave.
type Reviewer struct {
	User User
	// State is approved, changes_requested, pending (asked to review, no
	// verdict yet), or commented for an Own review.
	State provider.ReviewState
	// Stale: the verdict was given on a commit older than HeadSHA.
	Stale bool
	// Dismissed: the verdict was dismissed. A host without dismissal does
	// not serve the reviewer at all; either way it is not listed.
	Dismissed bool
	// Own: review-mcp's own review by TokenUser, whose body carries
	// OwnMarker. A host where review-mcp casts no reviews does not serve it;
	// either way it is not listed.
	Own bool
}

// Env configures the harness around the pull request.
type Env struct {
	// Token is the provider token. The fixture configures the provider with
	// it and its fake server rejects any request that does not carry it.
	Token string
	// MaxFiles and MaxFileBytes are the provider's diff limits
	// (diff.max_files_full_content and diff.max_file_bytes).
	MaxFiles, MaxFileBytes int
	// Failure, when set, makes every request fail (see StartServer).
	Failure Failure

	log *requestLog
}

// Failure makes the fake server fail every request: with HTTP Status and a
// body that carries Sentinel, or, when Network is set, at the connection.
type Failure struct {
	Status  int
	Network bool
}

// testToken is the token of every Spec. It is synthetic.
const testToken = "tok-CONTRACT-LEAKCANARY-6d1f0b" //nolint:gosec // synthetic test value

var t0 = time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC)

func at(h int) time.Time { return t0.Add(time.Duration(h) * time.Hour) }

// Users of the synthetic host.
var (
	tokenUser = User{ID: 900, Login: "review-bot", DisplayName: "Review Bot"}
	alice     = User{ID: 101, Login: "alice", DisplayName: "Alice Example"}
	bob       = User{ID: 102, Login: "bob", DisplayName: "Bob Example"}
	carol     = User{ID: 103, Login: "carol", DisplayName: "Carol Example"}
	dave      = User{ID: 104, Login: "dave", DisplayName: "Dave Example"}
	erin      = User{ID: 105, Login: "erin", DisplayName: "Erin Example"}
	frank     = User{ID: 106, Login: "frank", DisplayName: "Frank Example"}
	grace     = User{ID: 107, Login: "grace", DisplayName: "Grace Example"}
	heidi     = User{ID: 108, Login: "heidi", DisplayName: "Heidi Example"}
)

// Paths of samplePR's files.
const (
	pathModified  = "src/app.go"
	pathAdded     = "src/added.go"
	pathDeleted   = "old/removed.go"
	pathRenamed   = "pkg/fresh_name.go"
	pathRenamedOl = "pkg/legacy_name.go"
	pathNoNewline = "docs/notes.txt"
	pathBinary    = "assets/logo.bin"
	pathTooLarge  = "data/huge.txt"
)

// Comment IDs of samplePR.
const (
	idGeneralOther = 11 // alice's general comment
	idGeneralOwn   = 12 // the token user's general comment
	idInlineRoot   = 21 // root of the inline thread on pathModified
)

// tooLargeContent is longer than samplePR's MaxFileBytes.
func tooLargeContent() (content, hunks string) {
	var c, h strings.Builder
	const lines = 120
	h.WriteString("@@ -0,0 +1," + strconv.Itoa(lines) + " @@\n")
	for i := 1; i <= lines; i++ {
		l := "generated line " + strconv.Itoa(i) + " of a large file\n"
		c.WriteString(l)
		h.WriteString("+" + l)
	}
	return c.String(), h.String()
}

// samplePR is the pull request of most cases.
func samplePR() Spec {
	huge, hugeHunks := tooLargeContent()
	return Spec{
		Title:        "Greet the whole world",
		Description:  "Changes the greeting.\n\nSynthetic pull request of the contract suite.",
		Author:       alice,
		SourceBranch: "feature/greeting",
		TargetBranch: "main",
		HeadSHA:      "a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1",
		BaseSHA:      "b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2",
		Files: []File{
			{
				Path: pathModified, Type: provider.ChangeModified,
				Base: "package app\n\nimport \"fmt\"\n\nfunc Greet() {\n\tfmt.Println(\"hello\")\n}\n\nfunc Spare() {}\n",
				Head: "package app\n\nimport \"fmt\"\n\nfunc Greet() {\n\tfmt.Println(\"hello, world\")\n}\n\nfunc Spare() {}\n",
				Hunks: "@@ -3,7 +3,7 @@\n import \"fmt\"\n \n func Greet() {\n" +
					"-\tfmt.Println(\"hello\")\n+\tfmt.Println(\"hello, world\")\n }\n \n func Spare() {}\n",
			},
			{
				Path: pathAdded, Type: provider.ChangeAdded,
				Head:  "package app\n\nvar Added = true\n",
				Hunks: "@@ -0,0 +1,3 @@\n+package app\n+\n+var Added = true\n",
			},
			{
				Path: pathDeleted, Type: provider.ChangeDeleted,
				Base:  "package old\n\nvar Gone = 1\n",
				Hunks: "@@ -1,3 +0,0 @@\n-package old\n-\n-var Gone = 1\n",
			},
			{
				Path: pathRenamed, OldPath: pathRenamedOl, Type: provider.ChangeRenamed,
				Base:  "package pkg\n\nconst Name = \"legacy\"\n",
				Head:  "package pkg\n\nconst Name = \"fresh\"\n",
				Hunks: "@@ -1,3 +1,3 @@\n package pkg\n \n-const Name = \"legacy\"\n+const Name = \"fresh\"\n",
			},
			{
				Path: pathNoNewline, Type: provider.ChangeModified,
				Base: "alpha\nbeta",
				Head: "alpha\ngamma",
				Hunks: "@@ -1,2 +1,2 @@\n alpha\n-beta\n" + noNewlineMarker + "\n+gamma\n" +
					noNewlineMarker + "\n",
			},
			{
				Path: pathBinary, Type: provider.ChangeModified, Binary: true,
				Base: "\x89PNG\r\n\x1a\n\x00\x00\x00\x01",
				Head: "\x89PNG\r\n\x1a\n\x00\x00\x00\x02",
			},
			{
				Path: pathTooLarge, Type: provider.ChangeAdded, TooLarge: true,
				Head: huge, Hunks: hugeHunks,
			},
		},
		Threads: []Thread{
			{Kind: provider.ThreadInline, Path: pathModified, Line: 6, Comments: []Comment{
				{ID: idInlineRoot, Author: bob, Body: "Should this greeting be configurable?", Created: at(2)},
				{ID: 22, Author: alice, Body: "Not in this pull request.", Created: at(3)},
				{ID: 23, Author: carol, Body: "Agreed, a follow-up then.", Created: at(4)},
			}},
			{Kind: provider.ThreadGeneral, Comments: []Comment{
				{ID: idGeneralOther, Author: alice, Body: "Ready for review.", Created: at(1)},
			}},
			{Kind: provider.ThreadInline, Path: pathRenamed, Line: 3, Resolved: true, Comments: []Comment{
				{ID: 31, Author: carol, Body: "Why the new name?", Created: at(5)},
				{ID: 32, Author: bob, Body: "It matches the package now.", Created: at(6)},
			}},
			{Kind: provider.ThreadGeneral, Resolved: true, Comments: []Comment{
				{ID: idGeneralOwn, Author: tokenUser, Body: "Automated summary of the pull request.", Created: at(0)},
			}},
		},
		TokenUser: tokenUser,
		Reviewers: []Reviewer{
			{User: dave, State: provider.ReviewApproved},
			{User: erin, State: provider.ReviewChangesRequested},
			{User: frank, State: provider.ReviewApproved, Stale: true},
			{User: grace, State: provider.ReviewChangesRequested, Dismissed: true},
			{User: tokenUser, State: provider.ReviewCommented, Own: true},
			{User: heidi, State: provider.ReviewPending},
		},
		Env: Env{Token: testToken, MaxFiles: 50, MaxFileBytes: 2048},
	}
}

// fileLimitPR has three small modified files and a file cap of two.
func fileLimitPR() Spec {
	pr := samplePR()
	pr.Threads, pr.Reviewers = nil, nil
	pr.Files = nil
	for _, n := range []string{"one", "two", "three"} {
		pr.Files = append(pr.Files, File{
			Path: "limit/" + n + ".txt", Type: provider.ChangeModified,
			Base: n + "\n", Head: n + " changed\n",
			Hunks: "@@ -1 +1 @@\n-" + n + "\n+" + n + " changed\n",
		})
	}
	pr.Env.MaxFiles = 2
	return pr
}
