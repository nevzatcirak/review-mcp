"""The daily dunning run: one reminder per customer with overdue invoices."""

from __future__ import annotations

from datetime import date

from billing.models import Invoice, Reminder
from billing.repository import InvoiceRepository

# Days past the due date at which each reminder level starts.
LEVELS = ((60, 3), (30, 2), (1, 1))


def reminder_level(days_overdue: int) -> int:
    for threshold, level in LEVELS:
        if days_overdue >= threshold:
            return level
    return 0


def overdue_invoices(repo: InvoiceRepository, customer_id: str, today: date) -> list[Invoice]:
    """The customer's open invoices past their due date, in repository order."""
    return [invoice for invoice in repo.open_invoices(customer_id) if invoice.due_on < today]


def next_reminder(repo: InvoiceRepository, customer_id: str, today: date) -> Reminder | None:
    overdue = overdue_invoices(repo, customer_id, today)
    if not overdue:
        return None
    oldest = min(overdue, key=lambda invoice: (invoice.due_on, invoice.number))
    level = reminder_level((today - oldest.due_on).days)
    total = sum(invoice.amount_cents for invoice in overdue)
    return Reminder(customer_id, oldest.number, level, total)


def run(repo: InvoiceRepository, customer_ids: list[str], today: date) -> list[Reminder]:
    reminders = []
    for customer_id in customer_ids:
        reminder = next_reminder(repo, customer_id, today)
        if reminder is not None:
            reminders.append(reminder)
    return reminders
