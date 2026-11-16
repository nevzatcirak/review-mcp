"""The daily dunning run: one reminder per customer with overdue invoices."""

from __future__ import annotations

from datetime import date

from billing.models import Invoice, Reminder
from billing.repository import InvoiceRepository

# Days past the due date at which each reminder level starts, highest first.
LEVELS = ((60, 3), (30, 2), (1, 1))


def reminder_level(days_overdue: int) -> int:
    return next((level for threshold, level in LEVELS if days_overdue >= threshold), 0)


def overdue_invoices(repo: InvoiceRepository, customer_id: str, today: date) -> list[Invoice]:
    """The customer's open invoices past their due date, in repository order."""
    return [invoice for invoice in repo.open_invoices(customer_id) if invoice.due_on < today]


def next_reminder(repo: InvoiceRepository, customer_id: str, today: date) -> Reminder | None:
    overdue = overdue_invoices(repo, customer_id, today)
    if not overdue:
        return None
    oldest = overdue[0]
    return Reminder(
        customer_id=customer_id,
        invoice_number=oldest.number,
        level=reminder_level((today - oldest.due_on).days),
        total_overdue_cents=sum(invoice.amount_cents for invoice in overdue),
    )


def run(repo: InvoiceRepository, customer_ids: list[str], today: date) -> list[Reminder]:
    reminders = (next_reminder(repo, customer_id, today) for customer_id in customer_ids)
    return [reminder for reminder in reminders if reminder is not None]
