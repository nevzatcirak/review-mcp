package delivery

import (
	"context"
	"fmt"

	"example.com/billing/invoice"
)

// Dispatcher sends invoices through a channel.
type Dispatcher struct {
	Log func(format string, args ...any)
}

// Send delivers invs through ch, as one batch when the channel supports it.
func (d *Dispatcher) Send(ctx context.Context, ch Channel, invs []invoice.Invoice) error {
	if len(invs) == 0 {
		return nil
	}
	if b, ok := ch.(BatchChannel); ok {
		sent, err := b.DeliverAll(ctx, invs)
		if err != nil {
			d.logf("%s: delivered %d of %d invoices before the failure", ch.Name(), sent, len(invs))
			return fmt.Errorf("%s: batch of %d: %w", ch.Name(), len(invs), err)
		}
		d.logf("%s: delivered %d invoices as one batch", ch.Name(), sent)
		return nil
	}
	for i, inv := range invs {
		if err := ch.Deliver(ctx, inv); err != nil {
			d.logf("%s: delivered %d of %d invoices before the failure", ch.Name(), i, len(invs))
			return fmt.Errorf("%s: invoice %s: %w", ch.Name(), inv.Number, err)
		}
	}
	d.logf("%s: delivered %d invoices", ch.Name(), len(invs))
	return nil
}

func (d *Dispatcher) logf(format string, args ...any) {
	if d.Log != nil {
		d.Log(format, args...)
	}
}
