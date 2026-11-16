# inventory

Stock levels of the example shop's warehouse.

- `store` – the item store
- `api` – the HTTP API used by the shop front end ([docs/api.md](docs/api.md))
- `restock` – booking supplier deliveries into stock ([docs/restocking.md](docs/restocking.md))
- `cmd/inventory` – the server

## Running

    go run ./cmd/inventory

The server listens on `localhost:8080`; set `INVENTORY_ADDR` to change it.
The store is in memory, so stock is empty after every start.

## Tests

    go test ./...
