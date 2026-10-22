package acceptv251

// Item is one entry of catalog.json.
type Item struct {
	SKU          string `json:"sku"`
	Name         string `json:"name"`
	PriceCents   int    `json:"price_cents"`
	Discontinued bool   `json:"discontinued"`
}

// Price returns the price of an item in cents, or 0 when it cannot be sold.
func Price(it Item) int {
	if it.PriceCents < 0 {
		return 0
	}
	return it.PriceCents
}
