package com.example.shop.api;

import com.example.shop.domain.CartLine;
import com.example.shop.persistence.InMemoryOrderRepository;
import com.example.shop.service.OrderService;
import org.junit.jupiter.api.Test;

import java.math.BigDecimal;
import java.time.Clock;
import java.util.List;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

class CheckoutControllerTest {

    @Test
    void submitPlacesTheOrderAndReturnsItsTotal() {
        InMemoryOrderRepository repository = new InMemoryOrderRepository();
        CheckoutController controller = new CheckoutController(new OrderService(repository, Clock.systemUTC()));

        CheckoutResponse response = controller.submit(
                new CheckoutRequest("c-1", List.of(new CartLine("sku-1", 2, new BigDecimal("4.25")))));

        assertEquals(new BigDecimal("8.50"), response.total());
        assertTrue(repository.findById(response.orderId()).isPresent());
    }
}
