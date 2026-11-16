package com.example.orders.refunds

import com.example.orders.model.Order
import com.example.orders.model.OrderLine
import com.example.orders.model.OrderStatus
import com.example.orders.notifications.CustomerNotifier
import com.example.orders.repository.OrderRepository
import java.math.BigDecimal

fun interface Payments {
    fun refund(orderId: String, amount: BigDecimal)
}

class RefundService(
    private val orders: OrderRepository,
    private val policy: RefundPolicy,
    private val payments: Payments,
    private val notifier: CustomerNotifier,
) {
    fun cancel(orderId: String, returned: List<OrderLine>): BigDecimal {
        val order = orders.find(orderId) ?: throw IllegalArgumentException("unknown order $orderId")
        check(order.status != OrderStatus.DELIVERED) { "a delivered order is returned, not cancelled" }

        val amount = policy.refundFor(order, returned)
        if (amount.signum() > 0) {
            payments.refund(order.id, amount)
        }
        val cancelled = order.withStatus(OrderStatus.CANCELLED)
        orders.save(cancelled)
        notifier.notifyStatus(cancelled)
        return amount
    }
}
