// Package edi delivers invoices to the wholesale partner as EDI interchanges.
package edi

import (
	"fmt"
	"strings"
	"time"

	"example.com/billing/invoice"
)

// interchange renders invoices as one interchange: a header segment with the
// control number, one segment per invoice, and a trailer with the count.
func interchange(sender string, control int, day time.Time, invs []invoice.Invoice) string {
	var b strings.Builder
	fmt.Fprintf(&b, "UNB+%s+%s+%09d'\n", sender, day.Format("20060102"), control)
	for _, inv := range invs {
		fmt.Fprintf(&b, "INV+%s+%s+%d'\n", inv.Number, inv.PartnerID, inv.Total)
	}
	fmt.Fprintf(&b, "UNZ+%d+%09d'\n", len(invs), control)
	return b.String()
}
