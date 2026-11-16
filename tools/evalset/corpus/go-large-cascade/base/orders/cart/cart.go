// Package cart keeps shopping carts: a guest's cart under its cookie id, a
// signed-in customer's cart under "customer:<id>".
package cart

import "sort"

// Line is one product in a cart, with the unit price when it was added.
type Line struct {
	SKU        string
	Quantity   int
	PriceCents int64
}

// Cart is an ordered list of lines, at most one per SKU.
type Cart struct {
	ID    string
	Lines []Line
}

// QuantityOf is the quantity of sku in the cart, 0 when it has none.
func (c *Cart) QuantityOf(sku string) int {
	for _, l := range c.Lines {
		if l.SKU == sku {
			return l.Quantity
		}
	}
	return 0
}

// Set puts quantity units of sku at the given price into the cart; a
// quantity of 0 removes the line.
func (c *Cart) Set(sku string, quantity int, priceCents int64) {
	for i, l := range c.Lines {
		if l.SKU != sku {
			continue
		}
		if quantity == 0 {
			c.Lines = append(c.Lines[:i], c.Lines[i+1:]...)
			return
		}
		c.Lines[i] = Line{SKU: sku, Quantity: quantity, PriceCents: priceCents}
		return
	}
	if quantity > 0 {
		c.Lines = append(c.Lines, Line{SKU: sku, Quantity: quantity, PriceCents: priceCents})
		sort.Slice(c.Lines, func(i, j int) bool { return c.Lines[i].SKU < c.Lines[j].SKU })
	}
}

// CustomerCartID is the id of a signed-in customer's cart.
func CustomerCartID(customerID string) string {
	return "customer:" + customerID
}
