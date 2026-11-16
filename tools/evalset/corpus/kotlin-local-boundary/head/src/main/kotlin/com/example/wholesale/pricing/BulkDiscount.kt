package com.example.wholesale.pricing

import java.math.BigDecimal

/** Lines of this many units or more get the bulk rate. */
const val BULK_QUANTITY = 100

/** The share of the list price paid at the bulk rate. */
val BULK_RATE = BigDecimal("0.90")

fun unitPriceFor(listPrice: BigDecimal, quantity: Int): BigDecimal =
    if (quantity > BULK_QUANTITY) (listPrice * BULK_RATE).toCents() else listPrice.toCents()
