package gitlab

// The inline part of the contract fake (WP-6c): POST .../discussions with a
// text position, checked against the Spec's hunks as GitLab checks it, and
// DELETE .../notes/{id} for the stray-note guard.

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/patch"
	"github.com/nevzatcirak/review-mcp/internal/provider/contract"
)

// ctLineInfo is a new-side line of a hunk: its old-side number for a
// context line, 0 for an added line.
type ctLineInfo struct {
	added bool
	old   int
}

// ctNewLines maps the new-side lines of f's hunks.
func ctNewLines(f *contract.File) map[int]ctLineInfo {
	out := map[int]ctLineInfo{}
	hunks, err := patch.ParseHunks(f.Hunks)
	if err != nil {
		return out
	}
	for _, h := range hunks {
		o, n := h.OldStart, h.NewStart
		for _, l := range h.Lines {
			switch l.Op {
			case ' ':
				out[n] = ctLineInfo{old: o}
				o++
				n++
			case '+':
				out[n] = ctLineInfo{added: true}
				n++
			case '-':
				o++
			}
		}
	}
	return out
}

// ctPositionIn is the position the fake reads; a missing line is nil.
type ctPositionIn struct {
	PositionType string `json:"position_type"`
	BaseSHA      string `json:"base_sha"`
	StartSHA     string `json:"start_sha"`
	HeadSHA      string `json:"head_sha"`
	OldPath      string `json:"old_path"`
	NewPath      string `json:"new_path"`
	NewLine      *int   `json:"new_line"`
	OldLine      *int   `json:"old_line"`
}

// ctRefusePosition answers GitLab's 400 for a note it cannot place: the
// note's errors as a Ruby hash in the message, or (Fixture.objectErrors)
// as an object. Both carry the sentinel, which must never reach an error.
func (g *ctGitLab) ctRefusePosition(w http.ResponseWriter) {
	var msg any = `400 Bad request - Note {:line_code=>["can't be blank", "must be a valid line code ` + contract.Sentinel + `"]}`
	if g.fx.objectErrors {
		msg = map[string]any{"line_code": []string{"can't be blank " + contract.Sentinel}}
	}
	ctJSON(w, http.StatusBadRequest, map[string]any{"message": msg})
}

// serveInline answers the inline requests; it reports whether the request
// was one of them.
func (g *ctGitLab) serveInline(w http.ResponseWriter, r *http.Request, path string) bool {
	switch {
	case path == ctMR+"/discussions" && r.Method == http.MethodPost:
		g.initDiscs()
		g.postDiscussion(w, r)
	case strings.HasPrefix(path, ctMR+"/notes/") && r.Method == http.MethodDelete:
		g.initDiscs()
		id, err := strconv.ParseInt(strings.TrimPrefix(path, ctMR+"/notes/"), 10, 64)
		d, n := g.findNote(id)
		if err != nil || n == nil {
			contract.WriteError(w, http.StatusNotFound)
			return true
		}
		if n.author.ID != g.pr.TokenUser.ID {
			g.t.Errorf("DELETE of note %d, written by %s", id, n.author.Login)
			contract.WriteError(w, http.StatusForbidden)
			return true
		}
		g.deleted = append(g.deleted, id)
		if g.fx.strayDeleteStatus != 0 {
			contract.WriteError(w, g.fx.strayDeleteStatus)
			return true
		}
		for i, m := range d.notes {
			if m == n {
				d.notes = append(d.notes[:i], d.notes[i+1:]...)
				break
			}
		}
		if len(d.notes) == 0 {
			for i, e := range g.discs {
				if e == d {
					g.discs = append(g.discs[:i], g.discs[i+1:]...)
					break
				}
			}
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		return false
	}
	return true
}

// postDiscussion creates a discussion. Without a position it is a general
// discussion. With one it is a DiffNote when the position is on the diff:
// the merge request's diff_refs, the file's paths, new_line a new-side line
// of a hunk, and old_line exactly for an unchanged line (its old-side
// number). Any other position is GitLab's 400. Fixture.strayLine makes the
// fake create a general note instead, as GitLab does for a position it
// cannot place, written by Fixture.strayAuthor when set.
func (g *ctGitLab) postDiscussion(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Body     *string       `json:"body"`
		Position *ctPositionIn `json:"position"`
	}
	if r.Header.Get("Content-Type") != "application/json" {
		g.t.Errorf("POST discussions: Content-Type %q", r.Header.Get("Content-Type"))
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Body == nil {
		g.t.Errorf("POST discussions: no body field")
		contract.WriteError(w, http.StatusBadRequest)
		return
	}
	pos := in.Position
	if pos == nil {
		g.t.Errorf("POST discussions without a position")
		contract.WriteError(w, http.StatusBadRequest)
		return
	}
	g.positions = append(g.positions, *pos)
	if pos.PositionType != "text" || pos.BaseSHA != g.pr.BaseSHA || pos.StartSHA != ctStartSHA || pos.HeadSHA != g.pr.HeadSHA {
		g.t.Errorf("position on the wrong revisions: %+v", *pos)
		ctJSON(w, http.StatusBadRequest, map[string]any{"message": "400 Bad request - position is invalid " + contract.Sentinel})
		return
	}
	var f *contract.File
	for i := range g.pr.Files {
		if g.pr.Files[i].Path == pos.NewPath {
			f = &g.pr.Files[i]
		}
	}
	if f == nil || pos.OldPath != f.BasePath() {
		g.t.Errorf("position paths %q -> %q do not name a changed file", pos.OldPath, pos.NewPath)
		g.ctRefusePosition(w)
		return
	}
	if pos.NewLine != nil && *pos.NewLine == g.fx.strayLine && g.fx.strayLine != 0 {
		g.nextID++
		author := g.pr.TokenUser
		if g.fx.strayAuthor != nil {
			author = *g.fx.strayAuthor
		}
		n := &ctNote{id: g.nextID, author: author, body: *in.Body, created: time.Date(2026, 3, 1, 0, 0, int(g.nextID), 0, time.UTC)}
		d := &ctDisc{id: strconv.FormatInt(n.id, 16) + "ef", individual: true, notes: []*ctNote{n}}
		g.discs = append(g.discs, d)
		g.writes = append(g.writes, *in.Body)
		ctJSON(w, http.StatusCreated, g.discJSON(d))
		return
	}
	info, ok := ctLineInfo{}, false
	if pos.NewLine != nil {
		info, ok = ctNewLines(f)[*pos.NewLine]
	}
	switch {
	case !ok:
	case info.added:
		ok = pos.OldLine == nil
	default:
		ok = pos.OldLine != nil && *pos.OldLine == info.old
	}
	if !ok {
		g.ctRefusePosition(w)
		return
	}
	g.nextID++
	n := &ctNote{id: g.nextID, author: g.pr.TokenUser, body: *in.Body, created: time.Date(2026, 3, 1, 0, 0, int(g.nextID), 0, time.UTC)}
	d := &ctDisc{id: strconv.FormatInt(n.id, 16) + "dd", resolvable: true, path: pos.NewPath, line: *pos.NewLine,
		head: g.pr.HeadSHA, notes: []*ctNote{n}}
	g.discs = append(g.discs, d)
	g.writes = append(g.writes, *in.Body)
	ctJSON(w, http.StatusCreated, g.discJSON(d))
}
