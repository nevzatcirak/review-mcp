package config

import (
	"io/fs"
	"os"
	"sort"
	"strings"
)

// Source abstracts the process environment and file reading so that loading
// can be tested without touching the real environment or disk.
type Source interface {
	// LookupEnv behaves like os.LookupEnv.
	LookupEnv(key string) (string, bool)
	// Environ behaves like os.Environ ("KEY=value" strings).
	Environ() []string
	// ReadFile reads the named file.
	ReadFile(path string) ([]byte, error)
	// Stat returns file info for the named file.
	Stat(path string) (fs.FileInfo, error)
}

// osSource is the Source backed by the real process environment and disk.
type osSource struct{}

func (osSource) LookupEnv(key string) (string, bool) { return os.LookupEnv(key) }
func (osSource) Environ() []string                   { return os.Environ() }

func (osSource) ReadFile(path string) ([]byte, error) {
	//nolint:gosec // G304: reading the operator-configured path (REVIEW_MCP_CONFIG, *_CA_CERT) is this abstraction's purpose.
	return os.ReadFile(path)
}

func (osSource) Stat(path string) (fs.FileInfo, error) { return os.Stat(path) }

// LoadFromOS loads the configuration from the real process environment and
// file system. See Load for the failure contract.
func LoadFromOS() (*Config, *Report, error) { return Load(osSource{}) }

// LoadFromOSWith is LoadFromOS for the given options (see LoadWith).
func LoadFromOSWith(opts LoadOptions) (*Config, *Report, error) { return LoadWith(osSource{}, opts) }

// MemSource is an in-memory Source for tests: Env is the environment and FS
// serves files. Leading "/" in paths is stripped before consulting FS, so a
// testing/fstest.MapFS keyed "cfg/review.toml" serves "/cfg/review.toml".
type MemSource struct {
	Env map[string]string
	FS  fs.FS
}

// LookupEnv implements Source.
func (m MemSource) LookupEnv(key string) (string, bool) {
	v, ok := m.Env[key]
	return v, ok
}

// Environ implements Source (sorted for determinism).
func (m MemSource) Environ() []string {
	out := make([]string, 0, len(m.Env))
	for k, v := range m.Env {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

func (m MemSource) name(path string) string { return strings.TrimLeft(path, "/") }

// ReadFile implements Source.
func (m MemSource) ReadFile(path string) ([]byte, error) {
	if m.FS == nil {
		return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrNotExist}
	}
	return fs.ReadFile(m.FS, m.name(path))
}

// Stat implements Source.
func (m MemSource) Stat(path string) (fs.FileInfo, error) {
	if m.FS == nil {
		return nil, &fs.PathError{Op: "stat", Path: path, Err: fs.ErrNotExist}
	}
	return fs.Stat(m.FS, m.name(path))
}
