package com.example.wholesale.pricing

import com.example.wholesale.model.QuoteRequestLine
import java.math.BigDecimal
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith

class QuoterTest {
    private val quoter = Quoter(MapPriceList(mapOf("mug-1" to BigDecimal("2.40"), "bowl-2" to BigDecimal("3.333"))))

    @Test
    fun `prices each line from the list`() {
        val quote = quoter.quote("w-1", listOf(QuoteRequestLine("mug-1", 10), QuoteRequestLine("bowl-2", 3)))

        assertEquals(BigDecimal("3.33"), quote.lines[1].unitPrice)
        assertEquals(BigDecimal("33.99"), quote.total)
    }

    @Test
    fun `refuses an unknown SKU`() {
        assertFailsWith<UnknownSkuException> { quoter.quote("w-1", listOf(QuoteRequestLine("lamp-9", 1))) }
    }
}
