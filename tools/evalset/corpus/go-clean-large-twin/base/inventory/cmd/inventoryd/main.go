// Command inventoryd serves the inventory API.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"example.com/inventory/api"
	"example.com/inventory/catalog"
	"example.com/inventory/reserve"
	"example.com/inventory/store"
)

func main() {
	ctx := context.Background()
	items, reservations := store.NewItemStore(), store.NewReservations()
	cat := catalog.New(items, reservations, 30*time.Second)
	reserver := &reserve.Reserver{Items: items, Reservations: reservations, Catalog: cat, Hold: 15 * time.Minute, Now: time.Now}
	go reserver.RunExpiry(ctx, time.Minute)
	if err := cat.Warm(ctx); err != nil {
		log.Fatal(err)
	}

	addr := os.Getenv("INVENTORY_ADDR")
	if addr == "" {
		addr = "localhost:8080"
	}
	srv := &api.Server{Items: items, Catalog: cat, Reserver: reserver}
	log.Printf("inventoryd: listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, srv.Handler()))
}
