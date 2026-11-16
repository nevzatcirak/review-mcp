package com.example.orders.fulfilment

import com.example.orders.model.Order
import com.example.orders.model.OrderLine
import com.example.orders.model.OrderStatus
import com.example.orders.notifications.StatusEmails
import com.example.orders.repository.InMemoryOrderRepository
import java.math.BigDecimal
import kotlin.test.Test
import kotlin.test.assertEquals

class ShipmentServiceTest {
    private val repository = InMemoryOrderRepository()
    private val sent = mutableListOf<String>()
    private val service = ShipmentService(repository, StatusEmails { _, subject, _ -> sent += subject })

    @Test
    fun `shipping a paid order marks it shipped`() {
        repository.save(Order("o-1", "ada@example.com", listOf(OrderLine("sku-1", 2, BigDecimal("3.00"))), OrderStatus.PAID))

        val shipped = service.ship("o-1", listOf(Parcel("T-1", mapOf("sku-1" to 2))))

        assertEquals(OrderStatus.SHIPPED, shipped.status)
        assertEquals(listOf("Your order is on its way"), sent)
    }

    @Test
    fun `a partial shipment waits for the remaining parcels`() {
        val lines = listOf(OrderLine("sku-1", 2, BigDecimal("3.00")), OrderLine("sku-2", 1, BigDecimal("8.00")))
        repository.save(Order("o-2", "bo@example.com", lines, OrderStatus.PAID))

        val first = service.ship("o-2", listOf(Parcel("T-2", mapOf("sku-1" to 2))))
        val second = service.ship("o-2", listOf(Parcel("T-3", mapOf("sku-2" to 1))))

        assertEquals(OrderStatus.PARTIALLY_SHIPPED, first.status)
        assertEquals(OrderStatus.SHIPPED, second.status)
        assertEquals(listOf("Part of your order is on its way", "Your order is on its way"), sent)
    }
}
