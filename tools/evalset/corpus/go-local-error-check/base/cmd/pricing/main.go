// Command pricing serves the sale prices.
package main

import (
	"flag"
	"log"
	"net/http"

	"example.com/pricing/catalog"
	"example.com/pricing/httpapi"
)

func main() {
	addr := flag.String("addr", "localhost:8081", "listen address")
	flag.Parse()

	store := catalog.NewStore()
	mux := http.NewServeMux()
	httpapi.Register(mux, store)
	log.Printf("listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
