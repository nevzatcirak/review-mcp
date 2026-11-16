package obs

import (
	"bytes"
	"testing"
	"time"
)

func TestPrintfWritesOneJSONLine(t *testing.T) {
	var buf bytes.Buffer
	out, now = &buf, func() time.Time { return time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC) }
	SetService("test")

	Printf("reserved %d x %s", 2, "mug-1")

	want := `{"time":"2024-03-01T12:00:00Z","level":"info","service":"test","msg":"reserved 2 x mug-1"}` + "\n"
	if buf.String() != want {
		t.Fatalf("got %s want %s", buf.String(), want)
	}
}

func TestFatalExits(t *testing.T) {
	var buf bytes.Buffer
	code := -1
	out, exit = &buf, func(c int) { code = c }

	Fatal("listen: ", "address in use")

	if code != 1 || !bytes.Contains(buf.Bytes(), []byte(`"level":"error"`)) {
		t.Fatalf("code %d, line %s", code, buf.String())
	}
}
