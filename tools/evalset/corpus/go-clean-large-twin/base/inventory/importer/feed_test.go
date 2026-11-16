package importer

import (
	"context"
	"strings"
	"testing"
	"time"

	"example.com/inventory/catalog"
	"example.com/inventory/store"
)

func TestApply(t *testing.T) {
	ctx := context.Background()
	items := store.NewItemStore()
	for _, item := range []store.Item{{SKU: "mug-1", Name: "Mug", OnHand: 2}, {SKU: "cup-3", Name: "Cup", OnHand: 1}} {
		if err := items.Put(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	feed := `[{"sku":"mug-1","name":"Blue mug","price_cents":900},
	          {"sku":"bowl-2","name":"Bowl","price_cents":1200},
	          {"sku":"cup-3","discontinued":true}]`

	res, err := Apply(ctx, strings.NewReader(feed), items, catalog.New(items, store.NewReservations(), time.Minute))
	if err != nil || res != (Result{Created: 1, Updated: 1, Removed: 1}) {
		t.Fatalf("Apply = %+v, %v", res, err)
	}
	mug, _ := items.Get(ctx, "mug-1")
	if mug.Name != "Blue mug" || mug.OnHand != 2 {
		t.Fatalf("mug = %+v", mug)
	}
}
