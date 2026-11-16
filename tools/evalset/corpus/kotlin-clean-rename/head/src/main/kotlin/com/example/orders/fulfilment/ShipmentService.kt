package com.example.orders.fulfilment

import com.example.orders.model.Order
import com.example.orders.model.OrderStatus
import com.example.orders.notifications.CustomerNotifier
import com.example.orders.repository.OrderRepository

class ShipmentService(
    private val orders: OrderRepository,
    private val notifier: CustomerNotifier,
) {
    fun ship(orderId: String, parcels: List<Parcel>): Order {
        val order = orders.find(orderId) ?: throw IllegalArgumentException("unknown order $orderId")
        check(order.status == OrderStatus.PAID) { "order $orderId is ${order.status}, not PAID" }
        require(parcels.isNotEmpty()) { "a shipment needs at least one parcel" }

        val shipped = order.withStatus(OrderStatus.SHIPPED)
        orders.save(shipped)
        notifier.notifyStatus(shipped)
        return shipped
    }
}
