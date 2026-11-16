# shop-orders

The order service of the example shop: checkout, stored orders and monthly
subscription renewals.

## Layout

- `domain` – carts, orders and currencies
- `persistence` – the order repository (in memory here; a database in production)
- `service` – placing orders
- `api` – the checkout endpoint
- `subscription` – the renewal job that places one order per subscription and month

## Build

    mvn test
