package tokens

import (
	"errors"
	"math/rand"
	"os"
	"os/exec"
	"strings"
	"testing"
	"unicode/utf8"
)

// Oracle fixtures. Expected counts were computed with upstream's tokenizer
// library in the parity environment:
//
//	/home/user/pr-agent-venv/bin/python -I oracle.py
//	  # tiktoken.get_encoding("o200k_base").encode(text, disallowed_special=())
//	prose 30
//	gocode 50
//	multibyte 36
//	special 17
//	empty 0
var rawFixtures = []struct {
	name string
	text string
	want int
}{
	{"prose", "The quick brown fox jumps over the lazy dog. Pack my box with five dozen liquor jugs!\nHow vexingly quick daft zebras jump.", 30},
	{"gocode", "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfor i := 0; i < 3; i++ {\n\t\tfmt.Printf(\"item %d: %v\\n\", i, []string{\"a\", \"b\"})\n\t}\n}\n", 50},
	{"multibyte turkish cjk", "Türkçe karakterler: ığüşöç İĞÜŞÖÇ. 今日は天気がいいですね。你好,世界!한국어 텍스트.", 36},
	{"special token spellings", "text with <|endoftext|> and <|fim_prefix|> inside", 17},
	{"empty", "", 0},
}

const helperEnv = "TOKENS_TEST_OFFLINE_HELPER"

// TestRawOfflineHelper runs only inside the subprocess started by
// TestRawOfflineNoNetwork; it must be the first thing that touches the codec.
func TestRawOfflineHelper(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		t.Skip("helper for TestRawOfflineNoNetwork")
	}
	for _, f := range rawFixtures {
		if got := Raw(f.text); got != f.want {
			t.Errorf("Raw(%s) = %d, want %d", f.name, got, f.want)
		}
	}
}

// TestRawOfflineNoNetwork is the no-network canary (spec 2.1): in a fresh
// process (cold codec) with proxies pointing at a closed port, Raw returns
// the oracle counts. A tokenizer that downloads BPE files would fail here.
func TestRawOfflineNoNetwork(t *testing.T) {
	proxy := "http://127.0.0.1:1"
	env := append(os.Environ(),
		helperEnv+"=1",
		"HTTPS_PROXY="+proxy, "HTTP_PROXY="+proxy,
		"https_proxy="+proxy, "http_proxy="+proxy,
		"ALL_PROXY="+proxy, "NO_PROXY=",
	)
	cmd := exec.Command(os.Args[0], "-test.run=^TestRawOfflineHelper$", "-test.v") //nolint:gosec // re-executes this test binary
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("offline subprocess failed: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "SKIP") {
		t.Fatalf("helper was skipped:\n%s", out)
	}
}

func TestRawFixtures(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	for _, f := range rawFixtures {
		t.Run(f.name, func(t *testing.T) {
			if got := Raw(f.text); got != f.want {
				t.Errorf("Raw = %d, want %d", got, f.want)
			}
		})
	}
}

// TestNoNetworkInImportGraph checks statically that the package's complete
// import graph (including the tokenizer module and its dependencies) has no
// network-capable standard library package.
func TestNoNetworkInImportGraph(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go tool not available")
	}
	out, err := exec.Command(goBin, "list", "-deps", "-f", "{{.ImportPath}}", ".").Output() //nolint:gosec // fixed arguments, go resolved from PATH
	if err != nil {
		t.Skipf("go list failed: %v", err)
	}
	deps := strings.Fields(string(out))
	found := false
	for _, d := range deps {
		if d == "github.com/tiktoken-go/tokenizer/codec" {
			found = true
		}
		switch d {
		case "net", "net/http", "crypto/tls", "os/exec":
			t.Errorf("import graph contains %q", d)
		}
	}
	if !found {
		t.Error("tokenizer codec package not in import graph; check is vacuous")
	}
}

func TestEstimate(t *testing.T) {
	text := rawFixtures[0].text // raw 30
	tests := []struct {
		factor float64
		want   int
	}{
		{0, 30},
		{0.3, 39},
		{0.5, 45},
		{0.01, 31}, // ceil(30.3)
	}
	for _, tc := range tests {
		if got := Estimate(text, tc.factor); got != tc.want {
			t.Errorf("Estimate(factor=%v) = %d, want %d", tc.factor, got, tc.want)
		}
	}
	if got := Estimate("", 0.3); got != 0 {
		t.Errorf("Estimate(empty) = %d, want 0", got)
	}
}

func intp(n int) *int { return &n }

func TestBudgetDerived(t *testing.T) {
	tests := []struct {
		name                         string
		maxOut                       *int
		window, prompt               int
		hardRes, softRes, soft, hard int
	}{
		{"nil output", nil, 10000, 500, 1000, 1500, 8000, 8500},
		{"output 4000", intp(4000), 20000, 1000, 4000, 4500, 14500, 15000},
		{"output 500 floors to 1000", intp(500), 10000, 0, 1000, 1500, 8500, 9000},
		{"output zero", intp(0), 10000, 0, 1000, 1500, 8500, 9000},
		{"negative output floors", intp(-5), 10000, 0, 1000, 1500, 8500, 9000},
		{"prompt exceeds window", nil, 4096, 5000, 1000, 1500, -2404, -1904},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := Budget{ContextWindow: tc.window, MaxOutputTokens: tc.maxOut, PromptTokens: tc.prompt}
			if got := b.HardReserve(); got != tc.hardRes {
				t.Errorf("HardReserve = %d, want %d", got, tc.hardRes)
			}
			if got := b.SoftReserve(); got != tc.softRes {
				t.Errorf("SoftReserve = %d, want %d", got, tc.softRes)
			}
			if got := b.SoftLimit(); got != tc.soft {
				t.Errorf("SoftLimit = %d, want %d", got, tc.soft)
			}
			if got := b.HardLimit(); got != tc.hard {
				t.Errorf("HardLimit = %d, want %d", got, tc.hard)
			}
		})
	}
}

func TestRequireCapacity(t *testing.T) {
	tests := []struct {
		name string
		b    Budget
		fits bool
	}{
		{"roomy", Budget{ContextWindow: 8000}, true},
		{"soft limit exactly 1", Budget{ContextWindow: 1501 + 100, PromptTokens: 100}, true},
		{"soft limit exactly 0", Budget{ContextWindow: 1500}, false},
		{"soft limit negative", Budget{ContextWindow: 4096, PromptTokens: 4000}, false},
		{"big output reserve", Budget{ContextWindow: 4096, MaxOutputTokens: intp(4000)}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.b.RequireCapacity()
			if tc.fits && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if !tc.fits && !errors.Is(err, ErrDoesNotFit) {
				t.Errorf("err = %v, want ErrDoesNotFit", err)
			}
		})
	}
}

func TestRequestTokens(t *testing.T) {
	sys, user := rawFixtures[0].text, rawFixtures[2].text // raw 30 and 36
	if got, want := RequestTokens(sys, user, 0), 30+36+3*16; got != want {
		t.Errorf("RequestTokens = %d, want %d", got, want)
	}
	if got, want := RequestTokens("", "", 0.3), 48; got != want {
		t.Errorf("RequestTokens(empty) = %d, want %d", got, want)
	}
}

func TestClipBasics(t *testing.T) {
	text := strings.Repeat("alpha beta gamma\n", 200)
	if got := Clip(text, 0, 0.3, false); got != "" {
		t.Errorf("maxTokens 0 = %q, want empty", got)
	}
	if got := Clip(text, -3, 0.3, false); got != "" {
		t.Errorf("maxTokens -3 = %q, want empty", got)
	}
	short := "short text"
	if got := Clip(short, 100, 0.3, true); got != short {
		t.Errorf("fitting text changed: %q", got)
	}
	got := Clip(text, 50, 0.3, true)
	if !strings.HasSuffix(got, "\n...(truncated)") {
		t.Errorf("missing marker: %q", got)
	}
	if Estimate(got, 0.3) > 50 {
		t.Errorf("exceeds cap: %d", Estimate(got, 0.3))
	}
	body := strings.TrimSuffix(got, TruncationMarker)
	if !strings.HasPrefix(text, body) {
		t.Errorf("clipped body is not a prefix of the input")
	}
	// deleteLastLine: body is made of whole lines only.
	if !strings.HasSuffix(body+"\n", "gamma\n") {
		t.Errorf("deleteLastLine body does not end on a line: %q", body[max(0, len(body)-20):])
	}
	// Without deleteLastLine a long single line is cut mid-line.
	line := strings.Repeat("word ", 400)
	got = Clip(line, 30, 0, true)
	if got == "" || Estimate(got, 0) > 30 {
		t.Errorf("single line clip failed: %q", got)
	}
}

func TestClipCapOne(t *testing.T) {
	// Marker alone costs more than one token: nothing fits, so "".
	got := Clip(strings.Repeat("x ", 100), 1, 0, false)
	if got != "" {
		t.Errorf("Clip(max=1) = %q, want empty", got)
	}
}

func TestClipWrappers(t *testing.T) {
	text := strings.Repeat("some description text ", 300)
	for name, fn := range map[string]func(string, int, float64) string{
		"description": ClipDescription, "commits": ClipCommits,
	} {
		got := fn(text, 40, 0.3)
		if got == "" || Estimate(got, 0.3) > 40 || !strings.HasSuffix(got, TruncationMarker) {
			t.Errorf("%s: bad clip %q", name, got)
		}
	}
}

// pathological returns inputs that stress the heuristic: BPE counts that
// diverge sharply from rune counts.
func pathological() map[string]string {
	return map[string]string{
		"cjk single line":      strings.Repeat("今日は天気がいいですね。你好,世界!", 400),
		"korean single line":   strings.Repeat("한국어 텍스트", 600),
		"emoji":                strings.Repeat("😀👍🏽🇹🇷🧑‍💻", 60),
		"turkish":              strings.Repeat("ığüşöçİĞÜŞÖÇ", 500),
		"ascii digits":         strings.Repeat("1234567890", 800),
		"spaces then words":    strings.Repeat(" ", 3000) + strings.Repeat("word ", 300),
		"rare bytes":           string([]rune(strings.Repeat("กขฃሴ☃", 600))),
		"merge boundary":       strings.Repeat("a", 5000),
		"newline only tail":    strings.Repeat("x", 2000) + strings.Repeat("\n", 2000),
		"dense symbols":        strings.Repeat("{}[]<>=!&|;:", 500),
		"mixed ascii then cjk": strings.Repeat("hello world ", 300) + strings.Repeat("漢字漢字", 600),
	}
}

// TestClipNeverExceeds is a [canary]: the exact-recount loop is what makes
// the guarantee hold; removing it makes this test fail.
func TestClipNeverExceeds(t *testing.T) {
	for name, text := range pathological() {
		for _, factor := range []float64{0.3} {
			for _, max := range []int{2, 17, 300} {
				for _, dll := range []bool{false, true} {
					got := Clip(text, max, factor, dll)
					if n := Estimate(got, factor); n > max {
						t.Fatalf("%s factor=%v max=%d dll=%v: estimate %d exceeds cap", name, factor, max, dll, n)
					}
					if !utf8.ValidString(got) {
						t.Fatalf("%s: invalid UTF-8 in result", name)
					}
				}
			}
		}
	}
}

func TestClipNeverExceedsRandomized(t *testing.T) {
	alphabets := [][]rune{
		[]rune("abcdefghijklmnopqrstuvwxyz     \n\n"),
		[]rune("ığüşöçİĞÜŞÖÇ abc\n"),
		[]rune("今日は天気がいいですね你好世界한국어"),
		[]rune("😀👍🧑💻🇹🇷‍́ a\n"),
		[]rune("{}()[];:=+-*/<>\"'\\\t\n 0123456789"),
	}
	rng := rand.New(rand.NewSource(42)) //nolint:gosec // deterministic test data, not security-sensitive
	for i := 0; i < 150; i++ {
		alpha := alphabets[rng.Intn(len(alphabets))]
		n := 1 + rng.Intn(1500)
		rs := make([]rune, n)
		for j := range rs {
			rs[j] = alpha[rng.Intn(len(alpha))]
		}
		text := string(rs)
		max := 1 + rng.Intn(400)
		factor := []float64{0, 0.3, 0.75}[rng.Intn(3)]
		dll := rng.Intn(2) == 0
		got := Clip(text, max, factor, dll)
		if e := Estimate(got, factor); e > max {
			t.Fatalf("case %d (len=%d max=%d factor=%v dll=%v): estimate %d exceeds cap", i, n, max, factor, dll, e)
		}
		if got != "" && got != text && !strings.HasSuffix(got, TruncationMarker) {
			t.Fatalf("case %d: clipped result lacks marker", i)
		}
		if !utf8.ValidString(got) {
			t.Fatalf("case %d: invalid UTF-8", i)
		}
	}
}

func BenchmarkEstimate1MB(b *testing.B) {
	text := strings.Repeat("func example(x int) string { return strconv.Itoa(x) } // comment\n", 1<<20/64+1)
	text = text[:1<<20]
	Raw("warm up") // exclude one-time codec initialisation
	b.SetBytes(int64(len(text)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Estimate(text, 0.3)
	}
}

func TestClipLargeInputIsFast(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	text := strings.Repeat("今日は天気がいいですね。", 20000)
	got := Clip(text, 1000, 0.3, false)
	if Estimate(got, 0.3) > 1000 {
		t.Fatal("large clip exceeds cap")
	}
}

func TestBudgetDiffCap(t *testing.T) {
	// Without a cap: 10000 - 1500 - 500 = 8000 soft, 8500 hard.
	base := Budget{ContextWindow: 10000, PromptTokens: 500}
	tests := []struct {
		name       string
		cap        int
		soft, hard int
		limit      string
	}{
		{"unset", 0, 8000, 8500, LimitContextWindow},
		{"cap below the budget wins", 3000, 3000, 3500, LimitDiffMaxTokens},
		{"cap equal to the budget", 8000, 8000, 8500, LimitContextWindow},
		{"cap above the budget has no effect", 20000, 8000, 8500, LimitContextWindow},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := base
			b.MaxDiffTokens = tc.cap
			if b.SoftLimit() != tc.soft || b.HardLimit() != tc.hard || b.Limit() != tc.limit {
				t.Errorf("soft %d hard %d limit %q, want %d %d %q", b.SoftLimit(), b.HardLimit(), b.Limit(), tc.soft, tc.hard, tc.limit)
			}
		})
	}
	if Cap(nil) != 0 || Cap(intp(1234)) != 1234 {
		t.Error("Cap does not convert the optional value")
	}
}
