// Package checkout turns a cart into a paid order.
package checkout

import (
	"context"
	"errors"
	"fmt"
	"log"

	"example.com/orders/cart"
)

// ErrItemGone means a cart line's product is no longer sold.
var ErrItemGone = errors.New("checkout: product no longer sold")

// PricedLine is a cart line at the catalog's current price.
type PricedLine struct {
	SKU        string
	Name       string
	Quantity   int
	PriceCents int64
}

// Priced is a cart priced for payment.
type Priced struct {
	Lines      []PricedLine
	TotalCents int64
}

// Pricer prices carts with the catalog's current prices.
type Pricer struct {
	Catalog cart.Catalog
}

func (p *Pricer) Price(ctx context.Context, c cart.Cart) (Priced, error) {
	var out Priced
	for _, line := range c.Lines {
		entry, err := p.Catalog.Lookup(ctx, line.SKU)
		if err != nil {
			return Priced{}, fmt.Errorf("price %s: %w", line.SKU, err)
		}
		if entry == nil {
			return Priced{}, fmt.Errorf("%w: %s", ErrItemGone, line.SKU)
		}
		if entry.PriceCents != line.PriceCents {
			log.Printf("checkout: price of %s changed from %d to %d since it was added", line.SKU, line.PriceCents, entry.PriceCents)
		}
		out.Lines = append(out.Lines, PricedLine{SKU: line.SKU, Name: entry.Name, Quantity: line.Quantity, PriceCents: entry.PriceCents})
		out.TotalCents += entry.PriceCents * int64(line.Quantity)
	}
	return out, nil
}
