package repocfg

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// MaxFileBytes is the size cap of the repository config: a larger file is
// not parsed.
const MaxFileBytes = 16 * 1024

// Limits of the key names shown in notes and the result.
const (
	// MaxIgnoredNames is the most ignored key names listed; the rest is
	// counted ("and N more").
	MaxIgnoredNames = 10
	// MaxKeyBytes caps one sanitised key name.
	MaxKeyBytes = 64
)

// Status is the outcome of reading the repository config (X-40 §1.3).
type Status string

// The statuses of the result's repo_config.status.
const (
	// StatusApplied: the file was read and at least one setting was applied.
	StatusApplied Status = "applied"
	// StatusAbsent: there is no file at the base revision.
	StatusAbsent Status = "absent"
	// StatusNothingApplied: the file was read and is valid, but nothing in it
	// was applied (it is empty, holds only comments, or only keys that are
	// ignored or have an invalid value).
	StatusNothingApplied Status = "nothing_applied"
	// StatusInvalid: the file is not TOML, or larger than MaxFileBytes.
	StatusInvalid Status = "invalid"
	// StatusOff: repo_config.enabled is false; nothing was read.
	StatusOff Status = "off"
	// StatusUnreadable: the read failed for a reason other than not found.
	StatusUnreadable Status = "unreadable"
)

// state is what reading the file came to, before any merge.
type state int

const (
	stateAbsent state = iota
	stateOff
	stateUnreadable
	stateTooLarge
	stateSyntax
	stateParsed
)

// FileGetter is the part of a provider that reads a file at a revision.
type FileGetter interface {
	GetFileAt(ctx context.Context, ref provider.PRRef, path, rev string) ([]byte, error)
}

// File is the repository config as read at the base revision: the values of
// the allowed keys, undecoded, and the names of the ignored keys. It holds
// no text of the file besides these.
type File struct {
	path  string
	state state
	// line is the line of a syntax error (0 when unknown).
	line int
	// values are the allowed keys present in the file, with their decoded
	// TOML values (int64, float64, string, bool, []any, ...).
	values map[string]any
	// ignored are the sanitised names of the keys a repository may not
	// change, in file order.
	ignored []string
}

// Read reads the repository config of pr: one GetFileAt of cfg.Path at the
// base revision (pr.BaseSHA), and nothing else. The head revision is never
// read, so the pull request cannot change the settings it is reviewed with;
// with no base revision nothing is read and the file is unreadable. It never
// fails: every outcome is a File whose notes say what happened. log may be
// nil.
func Read(ctx context.Context, log *slog.Logger, cfg config.RepoConfig, p FileGetter, ref provider.PRRef, pr *provider.PullRequest) *File {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	f := &File{path: cfg.Path}
	if !cfg.Enabled {
		f.state = stateOff
		return f
	}
	if pr == nil || pr.BaseSHA == "" {
		f.state = stateUnreadable
		return f
	}
	b, err := p.GetFileAt(ctx, ref, cfg.Path, pr.BaseSHA)
	switch {
	case errors.Is(err, provider.ErrNotFound):
		f.state = stateAbsent
		return f
	case errors.Is(err, provider.ErrTooLarge):
		f.state = stateTooLarge
		return f
	case err != nil:
		log.Debug("repo config: not read", "error", errorClass(err))
		f.state = stateUnreadable
		return f
	case len(b) > MaxFileBytes:
		f.state = stateTooLarge
		return f
	}
	f.parse(b)
	return f
}

// errorClass is the fixed class of a read error, for the debug log.
func errorClass(err error) string {
	var pe *provider.Error
	if errors.As(err, &pe) {
		return string(pe.Class)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "canceled"
	}
	return "unclassified"
}

// parse decodes b. The syntax is strict (a syntax error makes the whole file
// invalid, with its line); unknown keys are collected, not rejected. Only the
// line of an error is kept: upstream messages can echo the file.
func (f *File) parse(b []byte) {
	var raw map[string]any
	md, err := toml.Decode(string(b), &raw)
	if err != nil {
		f.state = stateSyntax
		var pe toml.ParseError
		if errors.As(err, &pe) {
			f.line = pe.Position.Line
		}
		return
	}
	f.state = stateParsed
	f.values = map[string]any{}
	for _, k := range md.Keys() {
		v, ok := lookup(raw, k)
		if !ok {
			continue
		}
		if _, table := v.(map[string]any); table {
			continue // a table header or an inline table: its keys are listed on their own
		}
		key := strings.Join(k, ".")
		if allowed[key] && !dottedPart(k) {
			f.values[key] = v
			continue
		}
		f.ignored = append(f.ignored, cleanKey(key))
	}
}

// lookup walks the decoded tables along the key path k.
func lookup(raw map[string]any, k toml.Key) (any, bool) {
	var cur any = raw
	for _, part := range k {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// dottedPart reports a quoted key part that contains a ".", which would
// otherwise pass for a section path ("review.max_findings" = 1).
func dottedPart(k toml.Key) bool {
	for _, part := range k {
		if strings.Contains(part, ".") {
			return true
		}
	}
	return false
}

// cleanKey makes an untrusted key name safe to show: only ASCII letters,
// digits, "_", "-" and "." are kept (anything else becomes "_"), and the name
// is cut to MaxKeyBytes.
func cleanKey(s string) string {
	var b strings.Builder
	for _, r := range s {
		if b.Len() >= MaxKeyBytes {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// Parsed reports that the file was read and is valid TOML.
func (f *File) Parsed() bool { return f.state == stateParsed }

// Notes are the notes of reading the file (X-40 §1.5), in the order they are
// shown: why it was ignored, or the keys a repository may not change.
func (f *File) Notes() []string {
	p := "`" + f.path + "`"
	switch f.state {
	case stateSyntax:
		if f.line > 0 {
			return []string{fmt.Sprintf("%s on the target branch could not be read as TOML (line %d) and was ignored.", p, f.line)}
		}
		return []string{p + " on the target branch could not be read as TOML and was ignored."}
	case stateTooLarge:
		return []string{p + " on the target branch is larger than 16 KiB and was ignored."}
	case stateUnreadable:
		return []string{p + " on the target branch could not be read and was ignored."}
	case stateParsed:
		if n := f.ignoredNote(); n != "" {
			return []string{n}
		}
	}
	return nil
}

// IgnoredNames are the ignored key names as shown: at most MaxIgnoredNames,
// then "and N more".
func (f *File) IgnoredNames() []string {
	if len(f.ignored) <= MaxIgnoredNames {
		return append([]string{}, f.ignored...)
	}
	out := append([]string{}, f.ignored[:MaxIgnoredNames]...)
	return append(out, fmt.Sprintf("and %d more", len(f.ignored)-MaxIgnoredNames))
}

func (f *File) ignoredNote() string {
	if len(f.ignored) == 0 {
		return ""
	}
	return fmt.Sprintf("Settings in `%s` that a repository may not change were ignored: %s.", f.path, strings.Join(f.IgnoredNames(), ", "))
}
