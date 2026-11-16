package checkout

import (
	"context"
	"errors"
	"fmt"

	"example.com/orders/cart"
	"example.com/platform/obs"
)

// ErrOutOfStock means a line asks for more than is available.
var ErrOutOfStock = errors.New("checkout: not enough stock")

// Availability tells how many units of a SKU can be sold right now.
type Availability interface {
	Availability(ctx context.Context, sku string) (int, error)
}

// Order is a paid order.
type Order struct {
	ID         string
	CustomerID string
	Lines      []PricedLine
	TotalCents int64
	ChargeID   string
}

// Service checks out customers' carts.
type Service struct {
	Carts    *cart.Store
	Pricer   *Pricer
	Stock    Availability
	Payments Payments
	NewID    func() string
}

// Checkout prices the customer's cart, checks the stock, charges the
// customer and empties the cart.
func (s *Service) Checkout(ctx context.Context, customerID string) (Order, error) {
	c, ok := s.Carts.Get(cart.CustomerCartID(customerID))
	if !ok || len(c.Lines) == 0 {
		return Order{}, errors.New("checkout: the cart is empty")
	}
	priced, err := s.Pricer.Price(ctx, c)
	if err != nil {
		return Order{}, err
	}
	for _, line := range priced.Lines {
		n, err := s.Stock.Availability(ctx, line.SKU)
		if err != nil {
			return Order{}, fmt.Errorf("checkout: stock of %s: %w", line.SKU, err)
		}
		if line.Quantity > n {
			return Order{}, fmt.Errorf("%w: %s", ErrOutOfStock, line.SKU)
		}
	}
	id := s.NewID()
	chargeID, err := charge(ctx, s.Payments, customerID, priced.TotalCents, id)
	if err != nil {
		return Order{}, err
	}
	s.Carts.Delete(c.ID)
	obs.Printf("checkout: order %s for %s paid, %d cents", id, customerID, priced.TotalCents)
	return Order{ID: id, CustomerID: customerID, Lines: priced.Lines, TotalCents: priced.TotalCents, ChargeID: chargeID}, nil
}

// Cancel refunds an order that could not be fulfilled.
func (s *Service) Cancel(ctx context.Context, o Order) {
	refund(ctx, s.Payments, o.ChargeID)
	obs.Printf("checkout: order %s cancelled", o.ID)
}
