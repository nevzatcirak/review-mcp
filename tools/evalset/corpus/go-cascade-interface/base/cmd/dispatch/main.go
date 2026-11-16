// Command dispatch sends the invoices issued today, once a day.
package main

import (
	"context"
	"log"
	"os"
	"time"

	"example.com/billing/delivery"
	"example.com/billing/invoice"
	"example.com/billing/partner/edi"
)

func main() {
	ctx := context.Background()
	invs, err := loadIssued(os.Getenv("INVOICE_DIR"), time.Now())
	if err != nil {
		log.Fatal(err)
	}
	var toCustomers, toPartner []invoice.Invoice
	for _, inv := range invs {
		if inv.PartnerID != "" {
			toPartner = append(toPartner, inv)
		} else {
			toCustomers = append(toCustomers, inv)
		}
	}

	d := &delivery.Dispatcher{Log: log.Printf}
	email := &delivery.EmailChannel{Mailer: smtpMailer{host: os.Getenv("SMTP_HOST")}}
	partner := &edi.Channel{Sender: "SHOP", Uploader: sftpInbox{dir: os.Getenv("EDI_OUTBOX")}, Today: time.Now}
	if err := d.Send(ctx, email, toCustomers); err != nil {
		log.Fatal(err)
	}
	if err := d.Send(ctx, partner, toPartner); err != nil {
		log.Fatal(err)
	}
}
