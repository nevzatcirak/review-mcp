package com.example.orders.notifications

import com.example.orders.model.Order
import com.example.orders.model.OrderStatus

fun interface Mailer {
    fun send(to: String, subject: String, body: String)
}

class CustomerNotifier(private val mailer: Mailer) {
    fun notifyStatus(order: Order) {
        val subject = when (order.status) {
            OrderStatus.NEW -> return
            OrderStatus.PAID -> "We received your payment"
            OrderStatus.SHIPPED -> "Your order is on its way"
            OrderStatus.DELIVERED -> "Your order was delivered"
            OrderStatus.CANCELLED -> "Your order was cancelled"
        }
        mailer.send(order.customerEmail, subject, "Order ${order.id}: $subject.")
    }
}
