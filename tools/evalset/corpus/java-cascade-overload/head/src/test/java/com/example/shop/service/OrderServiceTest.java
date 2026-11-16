package com.example.shop.service;

import com.example.shop.domain.Cart;
import com.example.shop.domain.CartLine;
import com.example.shop.domain.Currency;
import com.example.shop.domain.Order;
import com.example.shop.persistence.InMemoryOrderRepository;
import org.junit.jupiter.api.Test;

import java.math.BigDecimal;
import java.time.Clock;
import java.time.Instant;
import java.time.ZoneOffset;
import java.util.List;
import java.util.Optional;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

class OrderServiceTest {

    private final InMemoryOrderRepository repository = new InMemoryOrderRepository();
    private final OrderService service = new OrderService(repository,
            Clock.fixed(Instant.parse("2024-03-01T10:00:00Z"), ZoneOffset.UTC));

    @Test
    void placesAndStoresTheOrder() {
        Cart cart = new Cart("c-1", List.of(new CartLine("sku-1", 2, new BigDecimal("4.50"))));

        Order order = service.place(cart);

        assertEquals(new BigDecimal("9.00"), order.total());
        assertTrue(repository.findById(order.id()).isPresent());
    }

    @Test
    void storesTheCurrencyOfTheOrder() {
        Cart cart = new Cart("c-2", List.of(new CartLine("sku-1", 1, new BigDecimal("12.00"))));

        Order order = service.place(cart, Currency.USD);

        assertEquals(Optional.of(Currency.USD), repository.currencyOf(order.id()));
    }

    @Test
    void refusesAnEmptyCart() {
        assertThrows(IllegalArgumentException.class, () -> service.place(new Cart("c-1", List.of())));
    }
}
