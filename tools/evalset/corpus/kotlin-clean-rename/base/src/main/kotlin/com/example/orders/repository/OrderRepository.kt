package com.example.orders.repository

import com.example.orders.model.Order

interface OrderRepository {
    fun find(id: String): Order?
    fun save(order: Order)
}

class InMemoryOrderRepository : OrderRepository {
    private val orders = mutableMapOf<String, Order>()

    override fun find(id: String): Order? = orders[id]

    override fun save(order: Order) {
        orders[order.id] = order
    }
}
