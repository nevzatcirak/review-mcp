package notify

import (
	"context"

	"example.com/orders/checkout"
	"example.com/platform/obs"
)

// Mailer sends one email.
type Mailer interface {
	Send(ctx context.Context, to, subject, body string) error
}

// Notifier sends order emails. A failed email never fails the order.
type Notifier struct {
	Mailer Mailer
}

func (n *Notifier) OrderConfirmed(ctx context.Context, email string, o checkout.Order) {
	if err := n.Mailer.Send(ctx, email, "Your order "+o.ID, confirmationBody(o)); err != nil {
		obs.Printf("notify: confirmation of %s to customer %s failed: %v", o.ID, o.CustomerID, err)
	}
}
