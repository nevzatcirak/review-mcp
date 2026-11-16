// Package store keeps the warehouse's items and their stock.
package store

// Item is a stock-keeping unit and how many of it are on the shelves.
type Item struct {
	SKU      string `json:"sku"`
	Name     string `json:"name"`
	OnHand   int    `json:"on_hand"`
	Reserved int    `json:"reserved"`
}

// Available is the stock that can still be sold.
func (i Item) Available() int {
	if i.Reserved >= i.OnHand {
		return 0
	}
	return i.OnHand - i.Reserved
}
