// Package api serves the inventory over HTTP.
package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"example.com/inventory/store"
)

// Handler serves GET /items/{sku}.
type Handler struct {
	Store *store.Store
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /items/{sku}", h.getItem)
}

func (h *Handler) getItem(w http.ResponseWriter, r *http.Request) {
	item, err := h.Store.Get(r.Context(), r.PathValue("sku"))
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "no such item", http.StatusNotFound)
		return
	}
	if err != nil {
		log.Printf("get item: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"sku": item.SKU, "name": item.Name, "available": item.Available()}); err != nil {
		log.Printf("encode item: %v", err)
	}
}
