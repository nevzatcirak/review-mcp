package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"example.com/inventory/store"
)

func TestGetItem(t *testing.T) {
	s := store.New()
	if err := s.Put(context.Background(), store.Item{SKU: "mug-1", Name: "Mug", OnHand: 5, Reserved: 2}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	(&Handler{Store: s}).Register(mux)

	for path, want := range map[string]int{"/items/mug-1": http.StatusOK, "/items/nope": http.StatusNotFound} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != want {
			t.Errorf("GET %s = %d, want %d", path, rec.Code, want)
		}
	}
}
