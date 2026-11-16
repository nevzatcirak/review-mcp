// Package catalog holds the sale prices.
package catalog

import "fmt"

// Price is the sale price of one SKU, in cents of its currency.
type Price struct {
	SKU      string `json:"sku"`
	Cents    int64  `json:"cents"`
	Currency string `json:"currency"`
}

// String formats the price for logs, for example "mug-1 12.50 EUR".
func (p Price) String() string {
	return fmt.Sprintf("%s %d.%02d %s", p.SKU, p.Cents/100, p.Cents%100, p.Currency)
}
