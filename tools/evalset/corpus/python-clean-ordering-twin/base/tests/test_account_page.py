from datetime import date

from billing.account_page import recent_open_invoices
from billing.models import Invoice
from billing.repository import InvoiceRepository


def test_newest_first_and_limited():
    repo = InvoiceRepository()
    for month in range(1, 8):
        repo.add(Invoice(f"I-{month}", "c-1", 100 * month, date(2024, month, 1), date(2024, month, 28)))

    rows = recent_open_invoices(repo, "c-1", limit=3)

    assert [row["number"] for row in rows] == ["I-7", "I-6", "I-5"]
    assert rows[0]["amount"] == "7.00 EUR"
