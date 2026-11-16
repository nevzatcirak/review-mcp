package com.example.wholesale.pricing

import com.example.wholesale.model.Quote
import com.example.wholesale.model.QuoteLine
import com.example.wholesale.model.QuoteRequestLine

class UnknownSkuException(sku: String) : IllegalArgumentException("no wholesale price for $sku")

class Quoter(private val prices: PriceList) {
    fun quote(customerId: String, request: List<QuoteRequestLine>): Quote {
        require(request.isNotEmpty()) { "a quote needs at least one line" }
        val lines = request.map { line ->
            require(line.quantity > 0) { "quantity of ${line.sku} must be positive" }
            val listPrice = prices.listPrice(line.sku) ?: throw UnknownSkuException(line.sku)
            QuoteLine(line.sku, line.quantity, unitPriceFor(listPrice, line.quantity))
        }
        return Quote(customerId, lines)
    }
}
