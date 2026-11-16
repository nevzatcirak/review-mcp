package cart

import (
	"context"
	"testing"
	"time"

	"example.com/inventory/catalog"
)

type fakeCatalog map[string]*catalog.Entry

func (f fakeCatalog) Lookup(_ context.Context, sku string) (*catalog.Entry, error) {
	return f[sku], nil
}

func TestSetAddsReplacesAndRemoves(t *testing.T) {
	var c Cart
	c.Set("mug-1", 2, 850)
	c.Set("bowl-2", 1, 1200)
	c.Set("mug-1", 3, 850)
	if len(c.Lines) != 2 || c.Lines[0].SKU != "bowl-2" || c.QuantityOf("mug-1") != 3 {
		t.Fatalf("lines = %+v", c.Lines)
	}
	c.Set("bowl-2", 0, 0)
	if len(c.Lines) != 1 {
		t.Fatalf("lines after removal = %+v", c.Lines)
	}
}

func TestMergeGuestCartAddsUpAndCaps(t *testing.T) {
	store := NewStore(time.Now)
	store.Save(Cart{ID: "guest-1", Lines: []Line{{SKU: "mug-1", Quantity: 2, PriceCents: 800}, {SKU: "bowl-2", Quantity: 5, PriceCents: 1200}}})
	store.Save(Cart{ID: CustomerCartID("c-1"), Lines: []Line{{SKU: "mug-1", Quantity: 1, PriceCents: 800}}})
	m := &Merger{Carts: store, Catalog: fakeCatalog{
		"mug-1":  {SKU: "mug-1", PriceCents: 850, Available: 10},
		"bowl-2": {SKU: "bowl-2", PriceCents: 1200, Available: 3},
	}}

	res, err := m.MergeGuestCart(context.Background(), "guest-1", "c-1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Cart.QuantityOf("mug-1") != 3 || res.Cart.QuantityOf("bowl-2") != 3 || len(res.Capped) != 1 {
		t.Fatalf("result = %+v", res)
	}
	if _, ok := store.Get("guest-1"); ok {
		t.Fatal("the guest cart was kept")
	}
}
