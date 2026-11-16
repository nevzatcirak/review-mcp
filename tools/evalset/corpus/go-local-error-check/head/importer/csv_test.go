package importer

import (
	"strings"
	"testing"

	"example.com/pricing/catalog"
)

func TestParseCSV(t *testing.T) {
	in := "sku,price,currency\nmug-1,12.5,eur\nbowl-2,7,EUR\n"
	got, err := ParseCSV(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	want := []catalog.Price{{SKU: "mug-1", Cents: 1250, Currency: "EUR"}, {SKU: "bowl-2", Cents: 700, Currency: "EUR"}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseCSVRejectsAWrongHeader(t *testing.T) {
	if _, err := ParseCSV(strings.NewReader("sku;price;currency\n")); err == nil {
		t.Fatal("accepted a wrong header")
	}
}

func TestParseCents(t *testing.T) {
	for in, want := range map[string]int64{"12": 1200, "12.5": 1250, "0.99": 99} {
		if got, err := parseCents(in); err != nil || got != want {
			t.Errorf("parseCents(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"12.345", "abc", "-1", "1.x"} {
		if _, err := parseCents(in); err == nil {
			t.Errorf("parseCents(%q) accepted", in)
		}
	}
}
