// Command pricing serves the sale prices.
package main

import (
	"flag"
	"log"
	"net/http"
	"os"

	"example.com/pricing/catalog"
	"example.com/pricing/httpapi"
	"example.com/pricing/importer"
)

func main() {
	addr := flag.String("addr", "localhost:8081", "listen address")
	priceList := flag.String("import", "", "load this CSV price list before serving")
	flag.Parse()

	store := catalog.NewStore()
	if *priceList != "" {
		f, err := os.Open(*priceList)
		if err != nil {
			log.Fatal(err)
		}
		prices, err := importer.ParseCSV(f)
		f.Close()
		if err != nil {
			log.Fatal(err)
		}
		store.Set(prices)
		log.Printf("imported %d prices", len(prices))
	}
	mux := http.NewServeMux()
	httpapi.Register(mux, store)
	log.Printf("listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
