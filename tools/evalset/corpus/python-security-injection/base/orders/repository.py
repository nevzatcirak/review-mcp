from __future__ import annotations

import sqlite3
from dataclasses import dataclass


@dataclass(frozen=True)
class OrderRow:
    id: str
    customer_id: str
    status: str
    total_cents: int
    created_at: str


def insert_order(conn: sqlite3.Connection, row: OrderRow) -> None:
    conn.execute(
        "INSERT INTO orders (id, customer_id, status, total_cents, created_at) VALUES (?, ?, ?, ?, ?)",
        (row.id, row.customer_id, row.status, row.total_cents, row.created_at),
    )


def list_orders(
    conn: sqlite3.Connection,
    customer_id: str,
    order_by: str = "created_at DESC",
    limit: int = 50,
) -> list[OrderRow]:
    """The customer's orders, sorted by the given ORDER BY expression."""
    sql = (
        "SELECT id, customer_id, status, total_cents, created_at FROM orders "
        f"WHERE customer_id = ? ORDER BY {order_by} LIMIT ?"
    )
    return [OrderRow(**dict(row)) for row in conn.execute(sql, (customer_id, limit))]
