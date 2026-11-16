from datetime import date

from billing.models import Invoice
from billing.repository import InvoiceRepository


def test_open_invoices_skips_paid_and_other_customers():
    repo = InvoiceRepository()
    repo.add(Invoice("I-1", "c-1", 1000, date(2024, 1, 5), date(2024, 2, 4)))
    repo.add(Invoice("I-2", "c-1", 2000, date(2024, 2, 5), date(2024, 3, 6), paid=True))
    repo.add(Invoice("I-3", "c-2", 3000, date(2024, 2, 5), date(2024, 3, 6)))
    repo.add(Invoice("I-4", "c-1", 4000, date(2024, 3, 5), date(2024, 4, 4)))

    assert [invoice.number for invoice in repo.open_invoices("c-1")] == ["I-4", "I-1"]
