// Command inventory serves the inventory API.
package main

import (
	"log"
	"net/http"
	"os"

	"example.com/inventory/api"
	"example.com/inventory/store"
)

func main() {
	addr := os.Getenv("INVENTORY_ADDR")
	if addr == "" {
		addr = "localhost:8080"
	}
	mux := http.NewServeMux()
	(&api.Handler{Store: store.New()}).Register(mux)
	log.Printf("listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
