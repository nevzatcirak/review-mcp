package com.example.orders.refunds

import com.example.orders.model.Order
import com.example.orders.model.OrderLine
import com.example.orders.model.OrderStatus
import java.math.BigDecimal
import kotlin.test.Test
import kotlin.test.assertEquals

class RefundPolicyTest {
    private val lines = listOf(OrderLine("sku-1", 2, BigDecimal("3.00")), OrderLine("sku-2", 1, BigDecimal("10.00")))
    private val policy = RefundPolicy()

    @Test
    fun `a paid order is refunded in full`() {
        assertEquals(BigDecimal("16.00"), policy.refundFor(Order("o-1", "a@example.com", lines, OrderStatus.PAID), emptyList()))
    }

    @Test
    fun `a shipped order refunds the returned lines`() {
        val order = Order("o-2", "a@example.com", lines, OrderStatus.SHIPPED)

        assertEquals(BigDecimal("10.00"), policy.refundFor(order, listOf(lines[1])))
    }

    @Test
    fun `a partially shipped order refunds what was not shipped`() {
        val order = Order("o-3", "a@example.com", lines, OrderStatus.PARTIALLY_SHIPPED, mapOf("sku-1" to 2))

        assertEquals(BigDecimal("10.00"), policy.refundFor(order, emptyList()))
    }
}
