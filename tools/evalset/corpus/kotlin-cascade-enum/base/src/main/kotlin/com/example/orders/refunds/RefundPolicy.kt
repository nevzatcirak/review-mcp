package com.example.orders.refunds

import com.example.orders.model.Order
import com.example.orders.model.OrderLine
import com.example.orders.model.OrderStatus
import java.math.BigDecimal

/**
 * How much a customer gets back when an order is cancelled. Before anything
 * has left the warehouse the whole order is refunded; afterwards only the
 * lines that came back.
 */
class RefundPolicy {
    fun refundFor(order: Order, returned: List<OrderLine>): BigDecimal {
        if (order.status == OrderStatus.CANCELLED || order.status == OrderStatus.NEW) {
            return BigDecimal.ZERO
        }
        if (order.status < OrderStatus.SHIPPED) {
            return order.total
        }
        return returned.fold(BigDecimal.ZERO) { sum, line -> sum + line.amount }
    }
}
