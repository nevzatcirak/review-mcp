from orders.db import connect
from orders.handlers import get_orders
from orders.http import Request
from orders.repository import OrderRow, insert_order


def seeded():
    conn = connect()
    insert_order(conn, OrderRow("o-1", "c-1", "paid", 1200, "2024-03-01T10:00:00"))
    insert_order(conn, OrderRow("o-2", "c-1", "shipped", 800, "2024-03-02T10:00:00"))
    insert_order(conn, OrderRow("o-3", "c-2", "paid", 5000, "2024-03-02T11:00:00"))
    return conn


def test_newest_first_and_only_the_customers_orders():
    response = get_orders(seeded(), Request("c-1"))

    assert response.status == 200
    assert [order["id"] for order in response.body] == ["o-2", "o-1"]


def test_limit_must_be_a_number():
    assert get_orders(seeded(), Request("c-1", {"limit": "ten"})).status == 400
