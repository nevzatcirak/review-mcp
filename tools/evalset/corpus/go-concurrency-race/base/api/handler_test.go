package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"example.com/fx/convert"
)

type oneRate float64

func (r oneRate) Rate(context.Context, string, string) (float64, error) { return float64(r), nil }

func TestServeHTTP(t *testing.T) {
	h := &Handler{Converter: &convert.Converter{Source: oneRate(2)}}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/convert?amount=150&from=EUR&to=USD", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"amount":300`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/convert?amount=1.5&from=EUR&to=USD", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("fractional amount: got %d", rec.Code)
	}
}
