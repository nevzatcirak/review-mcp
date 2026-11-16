# Inventory API

## GET /items/{sku}

Returns the item with the given SKU.

`200 OK`, `Content-Type: application/json`:

```json
{"available": 3, "name": "Mug", "sku": "mug-1"}
```

`available` is the stock on hand minus the reserved stock, and never less
than zero.

`404 Not Found` with the text `no such item` when the store does not know
the SKU. `500 Internal Server Error` with the text `internal error` for any
other failure; the cause is logged, not returned.
