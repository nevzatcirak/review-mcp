package delivery

import (
	"context"
	"fmt"

	"example.com/billing/invoice"
)

// Mailer sends one email.
type Mailer interface {
	Send(ctx context.Context, to, subject, body string) error
}

// EmailChannel emails each invoice to the customer.
type EmailChannel struct {
	Mailer Mailer
}

func (c *EmailChannel) Name() string { return "email" }

func (c *EmailChannel) Deliver(ctx context.Context, inv invoice.Invoice) error {
	subject := fmt.Sprintf("Invoice %s", inv.Number)
	body := fmt.Sprintf("Dear %s,\n\nyour invoice %s over %d.%02d EUR is attached.\n",
		inv.Customer, inv.Number, inv.Total/100, inv.Total%100)
	return c.Mailer.Send(ctx, inv.Email, subject, body)
}

// DeliverBatch sends the emails one after another and stops at the first
// failure.
func (c *EmailChannel) DeliverBatch(ctx context.Context, invs []invoice.Invoice) error {
	for _, inv := range invs {
		if err := c.Deliver(ctx, inv); err != nil {
			return err
		}
	}
	return nil
}
