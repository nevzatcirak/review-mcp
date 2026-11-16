package com.example.wholesale.pricing

import java.math.BigDecimal
import kotlin.test.Test
import kotlin.test.assertEquals

class BulkDiscountTest {
    @Test
    fun `small lines pay the list price`() {
        assertEquals(BigDecimal("2.40"), unitPriceFor(BigDecimal("2.40"), 99))
    }

    @Test
    fun `large lines pay the bulk rate`() {
        assertEquals(BigDecimal("2.16"), unitPriceFor(BigDecimal("2.40"), 250))
    }
}
