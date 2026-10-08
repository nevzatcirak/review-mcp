package contract

import (
	"os/exec"
	"strings"
	"testing"
)

const (
	modulePath   = "github.com/nevzatcirak/review-mcp"
	contractPath = modulePath + "/internal/provider/contract"
	repoRoot     = "../../.."
)

// TestNoProductionPackageImportsContract: the contract suite is test support
// (it imports "testing"), so no non-test package of cmd/... or internal/...
// other than the suite itself may depend on it, directly or not. Only
// _test.go files of the providers import it.
func TestNoProductionPackageImportsContract(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go tool not found")
	}
	cmd := exec.Command(goBin, "list", "-test=false", "-f", "{{.ImportPath}} {{join .Deps \" \"}}", "./cmd/...", "./internal/...") //nolint:gosec // fixed arguments
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list failed: %v", err)
	}
	checked := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] == contractPath {
			continue
		}
		checked++
		for _, dep := range fields[1:] {
			if dep == contractPath {
				t.Errorf("%s depends on %s", fields[0], contractPath)
			}
		}
	}
	if checked < 10 {
		t.Fatalf("go list covered only %d packages; the guard looks at the wrong tree", checked)
	}
}
