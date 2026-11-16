from __future__ import annotations

from dataclasses import dataclass
from datetime import date


@dataclass(frozen=True)
class Invoice:
    number: str
    customer_id: str
    amount_cents: int
    issued_on: date
    due_on: date
    paid: bool = False


@dataclass(frozen=True)
class Reminder:
    customer_id: str
    invoice_number: str
    level: int
    total_overdue_cents: int
