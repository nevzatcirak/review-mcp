// Package importer applies the supplier's product feed to the item store.
package importer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"

	"example.com/inventory/catalog"
	"example.com/inventory/store"
)

// FeedItem is one product of the supplier feed.
type FeedItem struct {
	SKU          string `json:"sku"`
	Name         string `json:"name"`
	PriceCents   int64  `json:"price_cents"`
	Discontinued bool   `json:"discontinued"`
}

// Result counts what an import did.
type Result struct {
	Created, Updated, Removed int
}

// Apply reads a JSON array of feed items. New SKUs are created with no
// stock, known ones get the feed's name and price, and discontinued ones
// are removed from the store.
func Apply(ctx context.Context, r io.Reader, items *store.ItemStore, cat *catalog.Catalog) (Result, error) {
	var feed []FeedItem
	if err := json.NewDecoder(r).Decode(&feed); err != nil {
		return Result{}, fmt.Errorf("importer: feed: %w", err)
	}
	var res Result
	for _, f := range feed {
		existing, err := items.Get(ctx, f.SKU)
		if err != nil {
			return res, fmt.Errorf("importer: %s: %w", f.SKU, err)
		}
		switch {
		case f.Discontinued:
			if existing != nil {
				if err := items.Delete(ctx, f.SKU); err != nil {
					return res, err
				}
				res.Removed++
			}
		case existing == nil:
			if err := items.Put(ctx, store.Item{SKU: f.SKU, Name: f.Name, PriceCents: f.PriceCents}); err != nil {
				return res, err
			}
			res.Created++
		default:
			existing.Name, existing.PriceCents = f.Name, f.PriceCents
			if err := items.Put(ctx, *existing); err != nil {
				return res, err
			}
			res.Updated++
		}
		cat.Invalidate(f.SKU)
	}
	log.Printf("importer: %d created, %d updated, %d removed", res.Created, res.Updated, res.Removed)
	return res, nil
}
