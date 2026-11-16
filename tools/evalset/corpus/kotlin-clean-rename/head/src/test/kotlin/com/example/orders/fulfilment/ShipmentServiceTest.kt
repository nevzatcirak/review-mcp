package com.example.orders.fulfilment

import com.example.orders.model.Order
import com.example.orders.model.OrderLine
import com.example.orders.model.OrderStatus
import com.example.orders.notifications.CustomerNotifier
import com.example.orders.repository.InMemoryOrderRepository
import java.math.BigDecimal
import kotlin.test.Test
import kotlin.test.assertEquals

class ShipmentServiceTest {
    private val repository = InMemoryOrderRepository()
    private val sent = mutableListOf<String>()
    private val service = ShipmentService(repository, CustomerNotifier { _, subject, _ -> sent += subject })

    @Test
    fun `shipping a paid order marks it shipped`() {
        repository.save(Order("o-1", "ada@example.com", listOf(OrderLine("sku-1", 2, BigDecimal("3.00"))), OrderStatus.PAID))

        val shipped = service.ship("o-1", listOf(Parcel("T-1", mapOf("sku-1" to 2))))

        assertEquals(OrderStatus.SHIPPED, shipped.status)
        assertEquals(listOf("Your order is on its way"), sent)
    }
}
