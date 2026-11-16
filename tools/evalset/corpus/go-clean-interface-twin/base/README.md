# billing

Sends the day's invoices to customers and partners. Each destination is a
`delivery.Channel`; the dispatcher hands every channel the invoices meant for
it.

- `invoice` – the invoice type
- `delivery` – channels, the dispatcher and the email channel
- `partner/edi` – the EDI channel of the wholesale partner
- `cmd/dispatch` – the daily job
