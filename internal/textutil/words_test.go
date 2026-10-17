package textutil

import "testing"

func TestCountWords(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"empty", "", 0},
		{"white space only", " \t\n ", 0},
		{"ascii", "count these words", 3},
		{"several spaces", "one   two \t three", 3},
		{"non-ascii", "çalışma günü", 2},
	}
	for _, c := range cases {
		if got := CountWords(c.in); got != c.want {
			t.Errorf("%s: CountWords(%q) = %d, want %d", c.name, c.in, got, c.want)
		}
	}
}
