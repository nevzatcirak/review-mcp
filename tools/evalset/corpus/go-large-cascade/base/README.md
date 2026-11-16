# shop

The example shop's back end as one repository with two Go modules:

- `inventory` – items, stock, reservations, the catalog read model and the
  supplier feed import; served by `inventoryd`
- `orders` – carts, sign-in, checkout and order emails; served by `ordersd`

`orders` uses the inventory catalog in-process through the Go workspace.

    go test ./inventory/... ./orders/...
