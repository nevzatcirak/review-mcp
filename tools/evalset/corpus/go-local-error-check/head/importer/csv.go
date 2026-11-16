// Package importer reads price lists exported by the merchandising team.
package importer

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"example.com/pricing/catalog"
)

// ParseCSV reads a price list with the header sku,price,currency, where
// price is a decimal such as 12.50.
func ParseCSV(r io.Reader) ([]catalog.Price, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = 3
	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("price list header: %w", err)
	}
	if strings.Join(header, ",") != "sku,price,currency" {
		return nil, fmt.Errorf("price list header is %q, want sku,price,currency", strings.Join(header, ","))
	}
	var prices []catalog.Price
	for line := 2; ; line++ {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("price list line %d: %w", line, err)
		}
		cents, err := parseCents(rec[1])
		currency := strings.ToUpper(strings.TrimSpace(rec[2]))
		if len(currency) != 3 {
			return nil, fmt.Errorf("price list line %d: currency %q", line, rec[2])
		}
		prices = append(prices, catalog.Price{SKU: strings.TrimSpace(rec[0]), Cents: cents, Currency: currency})
	}
	return prices, nil
}

// parseCents turns "12.5", "12.50" or "12" into 1250.
func parseCents(s string) (int64, error) {
	whole, frac, _ := strings.Cut(strings.TrimSpace(s), ".")
	if len(frac) > 2 {
		return 0, fmt.Errorf("price %q has more than two decimals", s)
	}
	frac += strings.Repeat("0", 2-len(frac))
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("price %q: %w", s, err)
	}
	f, err := strconv.ParseInt(frac, 10, 64)
	if err != nil || w < 0 {
		return 0, fmt.Errorf("price %q is not a positive decimal", s)
	}
	return w*100 + f, nil
}
