package cart

import (
	"context"
	"fmt"
	"strings"

	"example.com/inventory/catalog"
)

// Merging carts
//
// A visitor who is not signed in shops with a guest cart. Its id is the
// value of the "guest" cookie, which the front end creates on the first
// "add to cart" and keeps for 30 days; Store.Purge removes guest carts that
// nobody touched for that long. A signed-in customer shops with the cart
// stored under CustomerCartID, which outlives sessions and devices: a
// customer who fills the cart on the phone finds it on the laptop.
//
// Signing in therefore meets two carts. The shop keeps both: the guest's
// lines are added to the customer's cart, so neither what the customer chose
// on this visit nor what they left in the cart last week is lost. The guest
// cart is deleted afterwards and the front end drops the cookie, so that a
// second sign-in from the same browser does not add the lines again.
//
// A guest cart can be old, so the merge looks at the catalog again. The
// customer's own cart is not re-checked here; checkout prices and checks
// every line anyway, and doing it at sign-in would slow down every sign-in
// for customers with long carts.
//
// The saved-for-later list ("move all to cart") uses the same rules for
// adding up quantities and for the line limit, without the catalog check.

// Catalog is the part of the inventory catalog that carts need.
type Catalog interface {
	Lookup(ctx context.Context, sku string) (*catalog.Entry, error)
}

// MergeResult tells the sign-in page what happened to the guest's cart, so
// that it can explain changes to the customer.
type MergeResult struct {
	Cart Cart
	// Dropped lists the SKUs of guest lines whose product is no longer sold.
	Dropped []string
	// Capped lists the SKUs whose quantity was lowered to the stock that is
	// still available.
	Capped []string
}

// Merger moves a guest's cart into the customer's cart at sign-in.
type Merger struct {
	Carts *Store
	// Catalog tells which products are still sold, at what price and how
	// many are available.
	Catalog Catalog
	// MaxLines is the most lines a cart may have; 0 means no limit.
	MaxLines int
}

// MergeGuestCart adds the guest cart's lines to the customer's cart and
// deletes the guest cart. A guest may have kept a cart for weeks before
// signing in, so its products are checked against the catalog first:
//
//   - a product that left the catalog in the meantime is dropped and
//     reported in Dropped;
//   - a quantity above the available stock is lowered and reported in
//     Capped;
//   - the line takes the catalog's current price.
//
// Lines of products the customer already has are added up. An empty or
// missing guest cart leaves the customer's cart as it is.
func (m *Merger) MergeGuestCart(ctx context.Context, guestCartID, customerID string) (MergeResult, error) {
	target, _ := m.Carts.Get(CustomerCartID(customerID))
	guest, ok := m.Carts.Get(guestCartID)
	if !ok || len(guest.Lines) == 0 {
		return MergeResult{Cart: target}, nil
	}

	var result MergeResult
	for _, line := range dedupe(guest.Lines) {
		entry, err := m.Catalog.Lookup(ctx, line.SKU)
		if err != nil {
			return MergeResult{}, fmt.Errorf("merge cart %s: %w", guestCartID, err)
		}
		if entry == nil {
			result.Dropped = append(result.Dropped, line.SKU)
			continue
		}
		quantity := target.QuantityOf(line.SKU) + line.Quantity
		if quantity > entry.Available {
			quantity = entry.Available
			result.Capped = append(result.Capped, line.SKU)
		}
		if m.full(target, line.SKU) {
			result.Dropped = append(result.Dropped, line.SKU)
			continue
		}
		target.Set(line.SKU, quantity, entry.PriceCents)
	}

	m.Carts.Save(target)
	m.Carts.Delete(guestCartID)
	result.Cart = target
	return result, nil
}

// Notice is the message the sign-in page shows above the cart when the
// merge changed something, or "" when it did not.
func (r MergeResult) Notice() string {
	var parts []string
	if n := len(r.Dropped); n == 1 {
		parts = append(parts, "1 product from your earlier visit is no longer available and was removed")
	} else if n > 1 {
		parts = append(parts, fmt.Sprintf("%d products from your earlier visit are no longer available and were removed", n))
	}
	if n := len(r.Capped); n == 1 {
		parts = append(parts, "1 quantity was lowered to the stock we have")
	} else if n > 1 {
		parts = append(parts, fmt.Sprintf("%d quantities were lowered to the stock we have", n))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "; ") + "."
}

// MergeSavedForLater moves the lines of a customer's saved-for-later list
// back into their cart, as the "move all to cart" button does. Unlike a
// guest cart, the list belongs to the signed-in customer and was checked
// against the catalog when it was saved; prices and stock are checked again
// at checkout. Quantities of SKUs already in the cart are added up, and the
// list's lines beyond MaxLines stay on the list and are returned.
func (m *Merger) MergeSavedForLater(customerID string, saved []Line) (Cart, []Line) {
	target, _ := m.Carts.Get(CustomerCartID(customerID))
	var left []Line
	for _, line := range dedupe(saved) {
		if m.full(target, line.SKU) {
			left = append(left, line)
			continue
		}
		price := line.PriceCents
		if have := priceIn(target, line.SKU); have > 0 {
			price = have
		}
		target.Set(line.SKU, target.QuantityOf(line.SKU)+line.Quantity, price)
	}
	m.Carts.Save(target)
	return target, left
}

// dedupe returns the lines with one line per SKU, in the order of first
// appearance, adding up quantities and keeping the first price. Carts saved
// by the shop's first mobile app could hold a SKU twice.
func dedupe(lines []Line) []Line {
	index := make(map[string]int, len(lines))
	out := make([]Line, 0, len(lines))
	for _, l := range lines {
		if l.Quantity <= 0 {
			continue
		}
		if i, ok := index[l.SKU]; ok {
			out[i].Quantity += l.Quantity
			continue
		}
		index[l.SKU] = len(out)
		out = append(out, l)
	}
	return out
}

// priceIn is the unit price of sku in the cart, 0 when the cart has none.
func priceIn(c Cart, sku string) int64 {
	for _, l := range c.Lines {
		if l.SKU == sku {
			return l.PriceCents
		}
	}
	return 0
}

// Changed reports whether the merge dropped or lowered anything.
func (r MergeResult) Changed() bool {
	return len(r.Dropped) > 0 || len(r.Capped) > 0
}

// full reports whether adding sku would give the cart more lines than
// MaxLines allows. A SKU already in the cart never adds a line.
func (m *Merger) full(c Cart, sku string) bool {
	if m.MaxLines == 0 || c.QuantityOf(sku) > 0 {
		return false
	}
	return len(c.Lines) >= m.MaxLines
}
