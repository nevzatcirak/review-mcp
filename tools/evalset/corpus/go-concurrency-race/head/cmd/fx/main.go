// Command fx serves currency conversions.
package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"example.com/fx/api"
	"example.com/fx/convert"
	"example.com/fx/rates"
)

func main() {
	source := &rates.HTTPSource{BaseURL: os.Getenv("RATES_URL"), Client: &http.Client{Timeout: 5 * time.Second}}
	mux := http.NewServeMux()
	mux.Handle("GET /convert", &api.Handler{Converter: &convert.Converter{Source: source, TTL: time.Minute}})
	log.Fatal(http.ListenAndServe("localhost:8082", mux))
}
