"""GET /account/orders: the signed-in customer's order history."""

from __future__ import annotations

import sqlite3

from orders.http import Request, Response
from orders.repository import list_orders

MAX_LIMIT = 100
DEFAULT_SORT = "created_at DESC"


def get_orders(conn: sqlite3.Connection, request: Request) -> Response:
    """Query parameters: limit (1-100, default 20) and sort, for example
    "total_cents DESC" for the most expensive orders first."""
    try:
        limit = int(request.query.get("limit", "20"))
    except ValueError:
        return Response(400, {"error": "limit must be a number"})
    limit = max(1, min(limit, MAX_LIMIT))
    sort = request.query.get("sort", DEFAULT_SORT).strip() or DEFAULT_SORT
    rows = list_orders(conn, request.customer_id, order_by=sort, limit=limit)
    return Response(200, [
        {"id": row.id, "status": row.status, "total_cents": row.total_cents, "created_at": row.created_at}
        for row in rows
    ])
