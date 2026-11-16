package com.example.wholesale.model

import java.math.BigDecimal

data class QuoteRequestLine(val sku: String, val quantity: Int)

data class QuoteLine(val sku: String, val quantity: Int, val unitPrice: BigDecimal) {
    val amount: BigDecimal get() = unitPrice * quantity.toBigDecimal()
}

data class Quote(val customerId: String, val lines: List<QuoteLine>) {
    val total: BigDecimal get() = lines.fold(BigDecimal.ZERO) { sum, line -> sum + line.amount }
}
