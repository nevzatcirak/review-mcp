package catalog

import (
	"context"
	"strings"

	"example.com/platform/obs"
)

// Search returns the entries whose name contains every word of query,
// ignoring case, at most limit of them.
func (c *Catalog) Search(ctx context.Context, query string, limit int) ([]*Entry, error) {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return nil, nil
	}
	items, err := c.items.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []*Entry
	for _, item := range items {
		name := strings.ToLower(item.Name)
		if !containsAll(name, words) {
			continue
		}
		entry, err := c.entryFor(ctx, item)
		if err != nil {
			return nil, err
		}
		out = append(out, entry)
		if len(out) == limit {
			break
		}
	}
	if len(out) == 0 {
		obs.Printf("catalog: no results for %q", query)
	}
	return out, nil
}

func containsAll(s string, words []string) bool {
	for _, w := range words {
		if !strings.Contains(s, w) {
			return false
		}
	}
	return true
}
