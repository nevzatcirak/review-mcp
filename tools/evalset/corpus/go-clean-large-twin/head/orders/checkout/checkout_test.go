package checkout

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"example.com/inventory/catalog"
	"example.com/inventory/store"
	"example.com/orders/cart"
)

type stubCatalog map[string]*catalog.Entry

func (s stubCatalog) Lookup(_ context.Context, sku string) (*catalog.Entry, error) {
	if e := s[sku]; e != nil {
		return e, nil
	}
	return nil, fmt.Errorf("%w: %s", store.ErrUnknownSKU, sku)
}

func (s stubCatalog) Availability(_ context.Context, sku string) (int, error) {
	if e := s[sku]; e != nil {
		return e.Available, nil
	}
	return 0, nil
}

type okPayments struct{ charged int64 }

func (p *okPayments) Charge(_ context.Context, _ string, amount int64, _ string) (string, error) {
	p.charged = amount
	return "ch-1", nil
}

func (p *okPayments) Refund(context.Context, string) error { return nil }

func TestCheckout(t *testing.T) {
	carts := cart.NewStore(time.Now)
	carts.Save(cart.Cart{ID: cart.CustomerCartID("c-1"), Lines: []cart.Line{{SKU: "mug-1", Quantity: 2, PriceCents: 800}}})
	cat := stubCatalog{"mug-1": {SKU: "mug-1", Name: "Mug", PriceCents: 850, Available: 5}}
	pay := &okPayments{}
	s := &Service{Carts: carts, Pricer: &Pricer{Catalog: cat}, Stock: cat, Payments: pay, NewID: func() string { return "o-1" }}

	o, err := s.Checkout(context.Background(), "c-1")
	if err != nil || o.TotalCents != 1700 || pay.charged != 1700 {
		t.Fatalf("Checkout = %+v, %v", o, err)
	}
	if _, ok := carts.Get(cart.CustomerCartID("c-1")); ok {
		t.Fatal("the cart was kept after checkout")
	}
}

func TestCheckoutOfAProductNoLongerSold(t *testing.T) {
	carts := cart.NewStore(time.Now)
	carts.Save(cart.Cart{ID: cart.CustomerCartID("c-1"), Lines: []cart.Line{{SKU: "gone-7", Quantity: 1, PriceCents: 500}}})
	s := &Service{Carts: carts, Pricer: &Pricer{Catalog: stubCatalog{}}, Stock: stubCatalog{}, Payments: &okPayments{}, NewID: func() string { return "o-2" }}

	if _, err := s.Checkout(context.Background(), "c-1"); !errors.Is(err, ErrItemGone) {
		t.Fatalf("Checkout: %v, want ErrItemGone", err)
	}
}
