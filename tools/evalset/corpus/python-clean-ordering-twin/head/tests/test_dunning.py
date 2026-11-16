from datetime import date

from billing.dunning import next_reminder, reminder_level, run
from billing.models import Invoice
from billing.repository import InvoiceRepository


def test_reminder_levels():
    assert [reminder_level(days) for days in (0, 1, 29, 30, 59, 60, 200)] == [0, 1, 1, 2, 2, 3, 3]


def test_one_reminder_per_customer_with_overdue_invoices():
    repo = InvoiceRepository()
    repo.add(Invoice("I-1", "c-1", 1500, date(2024, 1, 5), date(2024, 2, 4)))
    repo.add(Invoice("I-2", "c-2", 900, date(2024, 3, 1), date(2024, 3, 31)))

    reminders = run(repo, ["c-1", "c-2"], date(2024, 3, 10))

    assert len(reminders) == 1
    assert reminders[0].invoice_number == "I-1"
    assert reminders[0].level == 2
    assert reminders[0].total_overdue_cents == 1500


def test_no_reminder_without_overdue_invoices():
    assert next_reminder(InvoiceRepository(), "c-9", date(2024, 3, 10)) is None


def test_the_reminder_follows_the_oldest_overdue_invoice():
    repo = InvoiceRepository()
    repo.add(Invoice("I-1", "c-1", 1500, date(2024, 1, 5), date(2024, 1, 20)))
    repo.add(Invoice("I-2", "c-1", 700, date(2024, 2, 5), date(2024, 3, 1)))

    reminder = next_reminder(repo, "c-1", date(2024, 3, 25))

    assert reminder.invoice_number == "I-1"
    assert reminder.level == 3
    assert reminder.total_overdue_cents == 2200
