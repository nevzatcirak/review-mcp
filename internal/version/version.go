// Package version exposes build metadata for review-mcp.
package version

import (
	"runtime"
	"runtime/debug"
)

// Version and Commit are overridable at build time via
// -ldflags "-X github.com/nevzatcirak/review-mcp/internal/version.Version=..."
var (
	Version = "dev"
	Commit  = ""
)

// BuildInfo describes the running binary.
type BuildInfo struct {
	Version   string
	Commit    string
	GoVersion string
}

// Info returns the build metadata. When Commit was not set via ldflags it
// falls back to the vcs.revision recorded by the Go toolchain, if present.
func Info() BuildInfo {
	commit := Commit
	if commit == "" {
		if bi, ok := debug.ReadBuildInfo(); ok {
			for _, s := range bi.Settings {
				if s.Key == "vcs.revision" {
					commit = s.Value
					break
				}
			}
		}
	}
	return BuildInfo{Version: Version, Commit: commit, GoVersion: runtime.Version()}
}
