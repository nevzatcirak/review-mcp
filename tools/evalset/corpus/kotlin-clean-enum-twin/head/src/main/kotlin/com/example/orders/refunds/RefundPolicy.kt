package com.example.orders.refunds

import com.example.orders.model.Order
import com.example.orders.model.OrderLine
import com.example.orders.model.OrderStatus
import java.math.BigDecimal

/**
 * How much a customer gets back when an order is cancelled. Before anything
 * has left the warehouse the whole order is refunded; afterwards the lines
 * that were never shipped and the lines that came back.
 */
class RefundPolicy {
    fun refundFor(order: Order, returned: List<OrderLine>): BigDecimal {
        if (order.status == OrderStatus.CANCELLED || order.status == OrderStatus.NEW) {
            return BigDecimal.ZERO
        }
        if (order.status == OrderStatus.PAID) {
            return order.total
        }
        val returnedAmount = returned.fold(BigDecimal.ZERO) { sum, line -> sum + line.amount }
        if (order.status == OrderStatus.PARTIALLY_SHIPPED) {
            return unshippedAmount(order) + returnedAmount
        }
        return returnedAmount
    }

    private fun unshippedAmount(order: Order): BigDecimal =
        order.lines.fold(BigDecimal.ZERO) { sum, line ->
            val unshipped = (line.quantity - (order.shippedQuantities[line.sku] ?: 0)).coerceAtLeast(0)
            sum + line.unitPrice * unshipped.toBigDecimal()
        }
}
