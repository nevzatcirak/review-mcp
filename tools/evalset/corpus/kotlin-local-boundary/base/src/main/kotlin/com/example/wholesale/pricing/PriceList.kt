package com.example.wholesale.pricing

import java.math.BigDecimal

interface PriceList {
    /** The list price of one unit, or null for a SKU that is not sold wholesale. */
    fun listPrice(sku: String): BigDecimal?
}

class MapPriceList(private val prices: Map<String, BigDecimal>) : PriceList {
    override fun listPrice(sku: String): BigDecimal? = prices[sku]
}
