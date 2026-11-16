package store

import (
	"context"
	"testing"
)

func TestGetAndPut(t *testing.T) {
	s := New()
	ctx := context.Background()
	if err := s.Put(ctx, Item{SKU: "mug-1", Name: "Mug", OnHand: 4}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, "mug-1")
	if err != nil || got.OnHand != 4 {
		t.Fatalf("Get = %+v, %v", got, err)
	}
}

func TestGetUnknown(t *testing.T) {
	_, err := New().Get(context.Background(), "nope")
	if err != ErrNotFound {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
