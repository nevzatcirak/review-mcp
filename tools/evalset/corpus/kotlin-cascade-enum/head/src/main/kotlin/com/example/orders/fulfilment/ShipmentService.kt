package com.example.orders.fulfilment

import com.example.orders.model.Order
import com.example.orders.model.OrderStatus
import com.example.orders.notifications.StatusEmails
import com.example.orders.repository.OrderRepository

class ShipmentService(
    private val orders: OrderRepository,
    private val emails: StatusEmails,
) {
    /**
     * Records the parcels of a shipment. An order whose lines are not all
     * shipped yet stays PARTIALLY_SHIPPED until the remaining parcels follow.
     */
    fun ship(orderId: String, parcels: List<Parcel>): Order {
        val order = orders.find(orderId) ?: throw IllegalArgumentException("unknown order $orderId")
        check(order.status == OrderStatus.PAID || order.status == OrderStatus.PARTIALLY_SHIPPED) {
            "order $orderId is ${order.status}, not PAID or PARTIALLY_SHIPPED"
        }
        require(parcels.isNotEmpty()) { "a shipment needs at least one parcel" }

        val shippedNow = order.shippedQuantities.toMutableMap()
        for (parcel in parcels) {
            for ((sku, quantity) in parcel.quantities) {
                require(quantity > 0 && order.lines.any { it.sku == sku }) {
                    "parcel ${parcel.trackingCode}: $quantity x $sku is not part of order $orderId"
                }
                shippedNow.merge(sku, quantity, Int::plus)
            }
        }
        val complete = order.lines.all { line -> (shippedNow[line.sku] ?: 0) >= line.quantity }
        val next = if (complete) OrderStatus.SHIPPED else OrderStatus.PARTIALLY_SHIPPED

        val shipped = order.copy(status = next, shippedQuantities = shippedNow)
        orders.save(shipped)
        emails.statusChanged(shipped)
        return shipped
    }
}
