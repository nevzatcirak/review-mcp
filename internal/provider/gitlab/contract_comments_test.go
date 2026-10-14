package gitlab

// The discussions part of the contract fake (WP-6b): the Spec's threads as
// GitLab discussions, with system notes, DiffNote positions, resolvable
// and resolved flags, and the writes (POST and PUT) with their effect.

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/contract"
)

// ctOldHead is the head of a position GitLab could not move forward.
const ctOldHead = "e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5"

// ctSystemNoteID is the note of the system discussion (Fixture.systemNote).
const ctSystemNoteID = 9001

type ctNote struct {
	id      int64
	author  contract.User
	body    string
	created time.Time
	system  bool
}

type ctDisc struct {
	id         string
	individual bool
	resolvable bool
	resolved   bool
	// path and line are set for a DiffNote discussion; head is the head
	// its position is on.
	path  string
	line  int
	head  string
	notes []*ctNote
}

func (d *ctDisc) inline() bool { return d.path != "" }

// initDiscs builds the discussions from the Spec on first use.
func (g *ctGitLab) initDiscs() {
	if g.discs != nil {
		return
	}
	g.discs = []*ctDisc{}
	g.nextID = 1000
	for _, th := range g.pr.Threads {
		d := &ctDisc{id: strconv.FormatInt(th.Comments[0].ID, 16) + "ab", resolvable: true, resolved: th.Resolved, head: g.pr.HeadSHA}
		if th.Kind == provider.ThreadInline {
			d.path, d.line = th.Path, th.Line
			if th.Outdated {
				d.head = ctOldHead
			}
		} else if g.fx.individual && !th.Resolved {
			d.individual, d.resolvable = true, false
		}
		for _, c := range th.Comments {
			d.notes = append(d.notes, &ctNote{id: c.ID, author: c.Author, body: c.Body, created: c.Created})
		}
		g.discs = append(g.discs, d)
	}
	if g.fx.systemNote {
		g.discs = append(g.discs, &ctDisc{id: "5y5", individual: true, notes: []*ctNote{
			{id: ctSystemNoteID, author: g.pr.Author, body: "added 1 commit", created: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), system: true}}})
	}
}

func (g *ctGitLab) noteJSON(d *ctDisc, n *ctNote) map[string]any {
	m := map[string]any{
		"id": n.id, "type": nil, "body": n.body, "author": ctUser(n.author), "system": n.system,
		"created_at": n.created.Format(time.RFC3339), "updated_at": n.created.Format(time.RFC3339),
		"resolvable": d.resolvable && !n.system, "resolved": d.resolvable && d.resolved,
	}
	if d.inline() {
		m["type"] = "DiffNote"
		// A removed line has an old line and no new line.
		var newLine, oldLine any = d.line, nil
		if d.line == 0 {
			newLine, oldLine = nil, 3
		}
		m["position"] = map[string]any{
			"base_sha": g.pr.BaseSHA, "start_sha": ctStartSHA, "head_sha": d.head, "position_type": "text",
			"old_path": d.path, "new_path": d.path, "new_line": newLine, "old_line": oldLine,
		}
	} else if !d.individual {
		m["type"] = "DiscussionNote"
	}
	return m
}

func (g *ctGitLab) discJSON(d *ctDisc) map[string]any {
	notes := []any{}
	for _, n := range d.notes {
		notes = append(notes, g.noteJSON(d, n))
	}
	return map[string]any{"id": d.id, "individual_note": d.individual, "notes": notes}
}

// findNote returns the note id and its discussion.
func (g *ctGitLab) findNote(id int64) (*ctDisc, *ctNote) {
	for _, d := range g.discs {
		for _, n := range d.notes {
			if n.id == id {
				return d, n
			}
		}
	}
	return nil, nil
}

// addNote appends a note by the token user to d, or to a new individual
// discussion when d is nil.
func (g *ctGitLab) addNote(d *ctDisc, body string) (*ctDisc, *ctNote) {
	g.nextID++
	n := &ctNote{id: g.nextID, author: g.pr.TokenUser, body: body, created: time.Date(2026, 2, 1, 0, 0, int(g.nextID), 0, time.UTC)}
	if d == nil {
		d = &ctDisc{id: strconv.FormatInt(n.id, 16) + "cd", individual: true}
		g.discs = append(g.discs, d)
	}
	d.notes = append(d.notes, n)
	g.writes = append(g.writes, body)
	return d, n
}

// readBody decodes the JSON request body and returns its "body" field; a
// request without one fails the test.
func (g *ctGitLab) readBody(w http.ResponseWriter, r *http.Request) (string, bool) {
	var in struct {
		Body *string `json:"body"`
	}
	if r.Header.Get("Content-Type") != "application/json" {
		g.t.Errorf("request %s %s: Content-Type %q", r.Method, r.URL.EscapedPath(), r.Header.Get("Content-Type"))
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Body == nil {
		g.t.Errorf("request %s %s: no body field", r.Method, r.URL.EscapedPath())
		contract.WriteError(w, http.StatusBadRequest)
		return "", false
	}
	return *in.Body, true
}

// serveComments answers the discussion and note requests; it reports
// whether the request was one of them.
func (g *ctGitLab) serveComments(w http.ResponseWriter, r *http.Request, path string) bool {
	switch {
	case path == ctMR+"/discussions" || path == ctMRByID+"/discussions":
		if r.Method != http.MethodGet {
			return false
		}
		g.initDiscs()
		items := []any{}
		for _, d := range g.discs {
			items = append(items, g.discJSON(d))
		}
		g.ctPage(w, r, items, ctMRByID+"/discussions", ctPageSize, "per_page=100&")
	case path == ctMR+"/notes" && r.Method == http.MethodPost:
		g.initDiscs()
		body, ok := g.readBody(w, r)
		if !ok {
			return true
		}
		d, n := g.addNote(nil, body)
		ctJSON(w, http.StatusCreated, g.noteJSON(d, n))
	case strings.HasPrefix(path, ctMR+"/discussions/") && strings.HasSuffix(path, "/notes") && r.Method == http.MethodPost:
		g.initDiscs()
		id := strings.TrimSuffix(strings.TrimPrefix(path, ctMR+"/discussions/"), "/notes")
		var disc *ctDisc
		for _, d := range g.discs {
			if d.id == id {
				disc = d
			}
		}
		if disc == nil {
			contract.WriteError(w, http.StatusNotFound)
			return true
		}
		body, ok := g.readBody(w, r)
		if !ok {
			return true
		}
		d, n := g.addNote(disc, body)
		ctJSON(w, http.StatusCreated, g.noteJSON(d, n))
	case strings.HasPrefix(path, ctMR+"/notes/") && (r.Method == http.MethodGet || r.Method == http.MethodPut):
		g.initDiscs()
		id, err := strconv.ParseInt(strings.TrimPrefix(path, ctMR+"/notes/"), 10, 64)
		d, n := g.findNote(id)
		if err != nil || n == nil {
			contract.WriteError(w, http.StatusNotFound)
			return true
		}
		if r.Method == http.MethodPut {
			if n.author.ID != g.pr.TokenUser.ID {
				// GitLab refuses the edit of another user's note.
				g.t.Errorf("PUT of note %d, written by %s", id, n.author.Login)
				contract.WriteError(w, http.StatusForbidden)
				return true
			}
			body, ok := g.readBody(w, r)
			if !ok {
				return true
			}
			n.body = body
			g.writes = append(g.writes, body)
		}
		ctJSON(w, http.StatusOK, g.noteJSON(d, n))
	default:
		return false
	}
	return true
}
