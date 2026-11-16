package com.example.orders.model

import java.math.BigDecimal

data class OrderLine(val sku: String, val quantity: Int, val unitPrice: BigDecimal) {
    val amount: BigDecimal get() = unitPrice * quantity.toBigDecimal()
}

data class Order(
    val id: String,
    val customerEmail: String,
    val lines: List<OrderLine>,
    val status: OrderStatus,
    val shippedQuantities: Map<String, Int> = emptyMap(),
) {
    val total: BigDecimal get() = lines.fold(BigDecimal.ZERO) { sum, line -> sum + line.amount }

    fun withStatus(next: OrderStatus): Order = copy(status = next)
}
