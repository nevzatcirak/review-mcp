"""Data for the 'Your invoices' section of the customer account page."""

from __future__ import annotations

from billing.repository import InvoiceRepository


def format_cents(cents: int) -> str:
    return f"{cents // 100}.{cents % 100:02d} EUR"


def recent_open_invoices(repo: InvoiceRepository, customer_id: str, limit: int = 5) -> list[dict[str, str]]:
    """The newest open invoices first, at most limit of them."""
    newest_first = list(reversed(repo.open_invoices(customer_id)))
    return [
        {
            "number": invoice.number,
            "issued": invoice.issued_on.isoformat(),
            "due": invoice.due_on.isoformat(),
            "amount": format_cents(invoice.amount_cents),
        }
        for invoice in newest_first[:limit]
    ]
