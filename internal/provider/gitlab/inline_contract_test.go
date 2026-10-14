package gitlab

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/contract"
	"github.com/nevzatcirak/review-mcp/internal/review/anchor"
)

// ctShifted is a change whose unchanged lines below the added one have
// another number on each side: new-side line 5 is old-side line 4.
var ctShifted = contract.File{Path: "src/shift.go", Type: provider.ChangeModified,
	Base: "a\nb\nc\nd\ne\n", Head: "a\nb\nc\nX\nd\ne\n",
	Hunks: "@@ -2,4 +2,5 @@\n b\n c\n+X\n d\n e\n"}

// ctRenamedEdit is a renamed file with an edit.
var ctRenamedEdit = contract.File{Path: "pkg/edit.go", OldPath: "pkg/orig.go", Type: provider.ChangeRenamed,
	Base: "x\na\n", Head: "x\nb\n", Hunks: "@@ -1,2 +1,2 @@\n x\n-a\n+b\n"}

// ctItems resolves each line on the provider's own diff, as pr_review does.
func ctItems(t *testing.T, p *Provider, ref provider.PRRef, pr *provider.PullRequest, at ...struct {
	path string
	line int
}) []provider.InlineComment {
	t.Helper()
	d, err := p.GetDiff(t.Context(), ref, pr, provider.DiffOptions{})
	if err != nil {
		t.Fatalf("GetDiff: %v", err)
	}
	var out []provider.InlineComment
	for _, a := range at {
		var fp *provider.FilePatch
		for i := range d.Files {
			if d.Files[i].Path == a.path {
				fp = &d.Files[i]
			}
		}
		an, ok := anchor.Resolve(fp, a.line, a.line)
		if !ok {
			t.Fatalf("%s:%d does not resolve", a.path, a.line)
		}
		out = append(out, an.Comment("On "+a.path+"."))
	}
	return out
}

type ctAt = struct {
	path string
	line int
}

// TestContractInlineContextLine: a comment on an unchanged line whose
// numbers differ on the two sides is posted with old_line and new_line;
// one on an added line with new_line only; a renamed file's comment names
// the old path. Every item is posted as a DiffNote. [canary: leave OldLine
// 0 for a context line]
func TestContractInlineContextLine(t *testing.T) {
	spec := ctSpec(ctShifted, ctRenamedEdit)
	p, ref, g, _ := ctFixture{}.serve(t, spec)
	pr, err := p.GetPullRequest(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	items := ctItems(t, p, ref, pr, ctAt{"src/shift.go", 5}, ctAt{"src/shift.go", 4}, ctAt{"pkg/edit.go", 1})
	res, err := p.PostInlineComments(t.Context(), ref, pr, items)
	if err != nil {
		t.Fatalf("PostInlineComments: %v", err)
	}
	for i, r := range res {
		if !r.Posted || r.Reason != provider.InlineReasonPosted || r.ID == "" {
			t.Errorf("item %d (%s:%d, %s): %+v, want posted", i, items[i].Path, items[i].Line, items[i].LineType, r)
		}
	}
	type side struct{ oldPath, newPath, oldLine, newLine string }
	str := func(n *int) string {
		if n == nil {
			return "-"
		}
		return strconv.Itoa(*n)
	}
	var got []side
	for _, ps := range g.positions {
		got = append(got, side{ps.OldPath, ps.NewPath, str(ps.OldLine), str(ps.NewLine)})
	}
	want := []side{
		{"src/shift.go", "src/shift.go", "4", "5"},
		{"src/shift.go", "src/shift.go", "-", "4"},
		{"pkg/orig.go", "pkg/edit.go", "1", "1"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("positions (old path, new path, old line, new line):\n got %+v\nwant %+v", got, want)
	}
}

// TestContractInlineContextLineNeedsOldLine: GitLab cannot place a comment
// on an unchanged line by its new-side number alone; the item is
// unanchorable, with no server text.
func TestContractInlineContextLineNeedsOldLine(t *testing.T) {
	for _, objectErrors := range []bool{false, true} {
		p, ref, _, _ := ctFixture{objectErrors: objectErrors}.serve(t, ctSpec(ctShifted))
		pr, err := p.GetPullRequest(t.Context(), ref)
		if err != nil {
			t.Fatal(err)
		}
		items := []provider.InlineComment{{Path: "src/shift.go", Line: 5, LineType: provider.LineContext, Body: "No old line."}}
		res, err := p.PostInlineComments(t.Context(), ref, pr, items)
		if err != nil || len(res) != 1 {
			t.Fatalf("PostInlineComments: %+v, %v", res, err)
		}
		if res[0].Posted || res[0].Reason != provider.InlineReasonUnanchorable || strings.Contains(res[0].Error, contract.Sentinel) {
			t.Errorf("object errors %v: %+v, want unanchorable with a fixed sentence", objectErrors, res[0])
		}
	}
}

// TestContractInlineStrayNote: when GitLab creates a general note for an
// inline item it could not place, the provider deletes that note (its own,
// just created) and reports the item unanchorable; the next item is
// posted. When the delete fails, or the note is not the token user's
// (it is then never deleted), the item is failed with the fixed note.
// [canary: accept a non-DiffNote 201 as posted]
func TestContractInlineStrayNote(t *testing.T) {
	foreign := contract.User{ID: 102, Login: "bob"}
	for _, c := range []struct {
		name      string
		fx        ctFixture
		reason    provider.InlineReason
		errText   string
		deletes   int
		strayLeft bool
	}{
		{"deleted", ctFixture{strayLine: 4}, provider.InlineReasonUnanchorable, ErrStrayNoteRemoved, 1, false},
		{"delete_fails", ctFixture{strayLine: 4, strayDeleteStatus: http.StatusInternalServerError},
			provider.InlineReasonFailed, ErrStrayNoteLeft, 1, true},
		{"not_own", ctFixture{strayLine: 4, strayAuthor: &foreign}, provider.InlineReasonFailed, ErrStrayNoteLeft, 0, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, ref, g, _ := c.fx.serve(t, ctSpec(ctShifted))
			pr, err := p.GetPullRequest(t.Context(), ref)
			if err != nil {
				t.Fatal(err)
			}
			items := ctItems(t, p, ref, pr, ctAt{"src/shift.go", 4}, ctAt{"src/shift.go", 5})
			items[0].Body = "Stray."
			res, err := p.PostInlineComments(t.Context(), ref, pr, items)
			if err != nil || len(res) != 2 {
				t.Fatalf("PostInlineComments: %+v, %v", res, err)
			}
			if r := res[0]; r.Posted || r.Reason != c.reason || r.Error != c.errText || r.ID != "" || r.URL != "" {
				t.Errorf("stray item: %+v, want reason %q and %q", r, c.reason, c.errText)
			}
			if r := res[1]; !r.Posted || r.Reason != provider.InlineReasonPosted {
				t.Errorf("next item: %+v, want posted", r)
			}
			if len(g.deleted) != c.deletes {
				t.Errorf("%d deletes (%v), want %d", len(g.deleted), g.deleted, c.deletes)
			}
			threads, err := p.ListThreads(t.Context(), ref)
			if err != nil {
				t.Fatal(err)
			}
			left := false
			for _, th := range threads {
				if th.Kind == provider.ThreadGeneral && th.Comments[0].Body == "Stray." {
					left = true
				}
			}
			if left != c.strayLeft {
				t.Errorf("the stray general note is listed: %v, want %v", left, c.strayLeft)
			}
		})
	}
}
