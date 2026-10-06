package diffpipe

import (
	"fmt"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

// largePR is a synthetic pull request of 200 files and about 2 MB of
// patches across several languages: added and deleted files of 280 lines
// and modified files of 1650 lines with a one-line change every 15 lines.
func largePR() []provider.FilePatch {
	exts := []string{".go", ".py", ".js", ".java", ".zzz"}
	var fps []provider.FilePatch
	for i := range 200 {
		path := fmt.Sprintf("src/module_%d/file_%d%s", i%17, i, exts[i%len(exts)])
		switch i % 10 {
		case 0:
			fps = append(fps, added(path, 280))
		case 1:
			fps = append(fps, deleted(path, 280))
		default:
			fps = append(fps, modified(path, 1650, 15))
		}
	}
	return fps
}

// BenchmarkPrepareCompressedLargePR runs the compressed path (128k context
// window) on largePR, in both modes.
func BenchmarkPrepareCompressedLargePR(b *testing.B) {
	for _, mode := range []Mode{ModePlain, ModeNumbered} {
		b.Run([]string{"plain", "numbered"}[mode], func(b *testing.B) { benchPrepare(b, mode) })
	}
}

func benchPrepare(b *testing.B, mode Mode) {
	fps := largePR()
	size := 0
	for _, f := range fps {
		size += len(f.Patch)
	}
	in := Input{Files: fps, Mode: mode, Diff: diffCfg("clip"),
		Budget: tokens.Budget{ContextWindow: 128000, PromptTokens: 1500, Factor: 0.3}}
	b.ReportMetric(float64(size)/1e6, "MB-patches")
	b.ResetTimer()
	for range b.N {
		p, err := Prepare(in)
		if err != nil {
			b.Fatal(err)
		}
		if p.FastPath || len(p.Included) == 0 {
			b.Fatalf("FastPath = %v, included %d", p.FastPath, len(p.Included))
		}
	}
}
