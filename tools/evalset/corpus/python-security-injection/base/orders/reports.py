"""Figures for the customer service dashboard."""

from __future__ import annotations

import sqlite3

from orders.repository import OrderRow, list_orders


def largest_orders(conn: sqlite3.Connection, customer_id: str, count: int = 3) -> list[OrderRow]:
    return list_orders(conn, customer_id, order_by="total_cents DESC, created_at DESC", limit=count)


def lifetime_value_cents(conn: sqlite3.Connection, customer_id: str) -> int:
    row = conn.execute(
        "SELECT COALESCE(SUM(total_cents), 0) AS total FROM orders WHERE customer_id = ? AND status != 'cancelled'",
        (customer_id,),
    ).fetchone()
    return int(row["total"])
