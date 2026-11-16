from orders.db import connect
from orders.reports import largest_orders, lifetime_value_cents
from orders.repository import OrderRow, insert_order


def test_largest_orders_and_lifetime_value():
    conn = connect()
    insert_order(conn, OrderRow("o-1", "c-1", "paid", 1200, "2024-03-01T10:00:00"))
    insert_order(conn, OrderRow("o-2", "c-1", "cancelled", 9000, "2024-03-02T10:00:00"))
    insert_order(conn, OrderRow("o-3", "c-1", "shipped", 3000, "2024-03-03T10:00:00"))

    assert [row.id for row in largest_orders(conn, "c-1", 2)] == ["o-2", "o-3"]
    assert lifetime_value_cents(conn, "c-1") == 4200
