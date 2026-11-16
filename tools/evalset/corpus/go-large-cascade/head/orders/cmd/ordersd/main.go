// Command ordersd serves the orders API, with the inventory catalog
// in-process.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"time"

	"example.com/inventory/catalog"
	"example.com/inventory/store"
	"example.com/orders/api"
	"example.com/orders/cart"
	"example.com/orders/checkout"
	"example.com/orders/session"
	"example.com/platform/obs"
)

func main() {
	obs.SetService("ordersd")
	items, reservations := store.NewItemStore(), store.NewReservations()
	cat := catalog.New(items, reservations, 30*time.Second)
	carts := cart.NewStore(time.Now)
	sessions := session.NewSessions(12*time.Hour, time.Now)
	srv := &api.Server{
		Carts:    carts,
		Catalog:  cat,
		Sessions: sessions,
		Login:    &session.Service{Accounts: noAccounts{}, Sessions: sessions, Merger: &cart.Merger{Carts: carts, Catalog: cat, MaxLines: 50}},
		Checkout: &checkout.Service{Carts: carts, Pricer: &checkout.Pricer{Catalog: cat}, Stock: cat, Payments: noPayments{}, NewID: newID},
	}
	go func() {
		for range time.Tick(time.Hour) {
			carts.Purge(30 * 24 * time.Hour)
		}
	}()

	addr := os.Getenv("ORDERS_ADDR")
	if addr == "" {
		addr = "localhost:8090"
	}
	obs.Printf("ordersd: listening on %s", addr)
	obs.Fatal(http.ListenAndServe(addr, srv.Handler()))
}

func newID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return "o-" + hex.EncodeToString(b[:])
}

// noAccounts and noPayments stand in for the account and payment services,
// which run elsewhere.
type noAccounts struct{}

func (noAccounts) Authenticate(context.Context, string, string) (string, error) {
	return "", errors.New("accounts service not configured")
}

type noPayments struct{}

func (noPayments) Charge(context.Context, string, int64, string) (string, error) {
	return "", errors.New("payment provider not configured")
}

func (noPayments) Refund(context.Context, string) error {
	return errors.New("payment provider not configured")
}
