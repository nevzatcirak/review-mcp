# fx

Currency conversion for the example shop's price display. `GET
/convert?amount=1250&from=EUR&to=USD` converts an amount in cents with the
current rate of the rates provider.

- `rates` – the rates provider client
- `convert` – the conversion
- `api` – the HTTP handler
- `cmd/fx` – the server
