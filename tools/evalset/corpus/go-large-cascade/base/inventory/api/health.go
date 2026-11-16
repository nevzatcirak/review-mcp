package api

import (
	"log"
	"net/http"
)

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if _, err := s.Items.List(r.Context()); err != nil {
		log.Printf("api: health: %v", err)
		http.Error(w, "unhealthy", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
