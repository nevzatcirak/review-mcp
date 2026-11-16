// Package api serves conversions over HTTP.
package api

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"

	"example.com/fx/convert"
)

// Handler serves GET /convert. Requests are served concurrently.
type Handler struct {
	Converter *convert.Converter
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	amount, err := strconv.ParseInt(q.Get("amount"), 10, 64)
	if err != nil {
		http.Error(w, "amount must be a whole number of cents", http.StatusBadRequest)
		return
	}
	from, to := q.Get("from"), q.Get("to")
	if len(from) != 3 || len(to) != 3 {
		http.Error(w, "from and to must be currency codes", http.StatusBadRequest)
		return
	}
	converted, err := h.Converter.Convert(r.Context(), amount, from, to)
	if err != nil {
		log.Printf("convert: %v", err)
		http.Error(w, "rate unavailable", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"amount": converted, "currency": to}); err != nil {
		log.Printf("encode: %v", err)
	}
}
