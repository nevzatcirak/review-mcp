package checkout

import (
	"context"
	"errors"
	"fmt"

	"example.com/platform/obs"
)

// ErrDeclined means the payment provider refused the payment.
var ErrDeclined = errors.New("checkout: payment declined")

// Payments charges customers through the payment provider.
type Payments interface {
	Charge(ctx context.Context, customerID string, amountCents int64, reference string) (chargeID string, err error)
	Refund(ctx context.Context, chargeID string) error
}

func charge(ctx context.Context, p Payments, customerID string, amountCents int64, reference string) (string, error) {
	id, err := p.Charge(ctx, customerID, amountCents, reference)
	if errors.Is(err, ErrDeclined) {
		obs.Printf("checkout: payment of %d cents for %s declined", amountCents, reference)
		return "", err
	}
	if err != nil {
		return "", fmt.Errorf("checkout: charge %s: %w", reference, err)
	}
	return id, nil
}

func refund(ctx context.Context, p Payments, chargeID string) {
	if err := p.Refund(ctx, chargeID); err != nil {
		obs.Printf("checkout: refund of charge %s failed, needs manual follow-up: %v", chargeID, err)
	}
}
