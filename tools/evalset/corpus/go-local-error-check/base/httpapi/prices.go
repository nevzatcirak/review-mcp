// Package httpapi serves prices over HTTP.
package httpapi

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"example.com/pricing/catalog"
)

func Register(mux *http.ServeMux, store *catalog.Store) {
	mux.HandleFunc("GET /prices/{sku}", func(w http.ResponseWriter, r *http.Request) {
		p, err := store.Get(r.PathValue("sku"))
		if errors.Is(err, catalog.ErrUnknownSKU) {
			http.Error(w, "no price", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(p); err != nil {
			log.Printf("encode price: %v", err)
		}
	})
}
