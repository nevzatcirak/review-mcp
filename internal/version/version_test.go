package version

import (
	"runtime"
	"testing"
)

func TestInfo(t *testing.T) {
	oldV, oldC := Version, Commit
	t.Cleanup(func() { Version, Commit = oldV, oldC })

	Version, Commit = "v1.2.3", "abc1234"
	got := Info()
	if got.Version != "v1.2.3" || got.Commit != "abc1234" || got.GoVersion != runtime.Version() {
		t.Fatalf("unexpected Info: %+v", got)
	}

	// Empty commit must not panic; the fallback value depends on how the
	// test binary was built, so only the other fields are asserted.
	Commit = ""
	got = Info()
	if got.Version != "v1.2.3" || got.GoVersion != runtime.Version() {
		t.Fatalf("unexpected Info with empty commit: %+v", got)
	}
}
