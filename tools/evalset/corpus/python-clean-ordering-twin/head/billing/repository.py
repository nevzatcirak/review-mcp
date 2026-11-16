from __future__ import annotations

from billing.models import Invoice


class InvoiceRepository:
    """Keeps invoices in memory; the production one reads the ledger database."""

    def __init__(self) -> None:
        self._invoices: dict[str, Invoice] = {}

    def add(self, invoice: Invoice) -> None:
        self._invoices[invoice.number] = invoice

    def get(self, number: str) -> Invoice | None:
        return self._invoices.get(number)

    def open_invoices(self, customer_id: str) -> list[Invoice]:
        """Returns the customer's unpaid invoices."""
        unpaid = [
            invoice
            for invoice in self._invoices.values()
            if invoice.customer_id == customer_id and not invoice.paid
        ]
        return sorted(unpaid, key=lambda invoice: (invoice.issued_on, invoice.number), reverse=True)
