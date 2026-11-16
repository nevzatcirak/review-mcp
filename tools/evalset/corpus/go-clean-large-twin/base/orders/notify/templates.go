// Package notify emails customers about their orders.
package notify

import (
	"fmt"
	"strings"

	"example.com/orders/checkout"
)

func confirmationBody(o checkout.Order) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Thank you for your order %s.\n\n", o.ID)
	for _, l := range o.Lines {
		fmt.Fprintf(&b, "%3d x %-30s %8s\n", l.Quantity, l.Name, cents(l.PriceCents*int64(l.Quantity)))
	}
	fmt.Fprintf(&b, "\n%-36s %8s\n", "Total", cents(o.TotalCents))
	return b.String()
}

func cents(c int64) string {
	return fmt.Sprintf("%d.%02d", c/100, c%100)
}
