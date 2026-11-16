package com.example.shop.persistence;

import com.example.shop.domain.CartLine;
import com.example.shop.domain.Order;
import org.junit.jupiter.api.Test;

import java.math.BigDecimal;
import java.time.Instant;
import java.util.List;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

class InMemoryOrderRepositoryTest {

    private final InMemoryOrderRepository repository = new InMemoryOrderRepository();

    private static Order order(String id, String customerId, String createdAt) {
        return new Order(id, customerId, List.of(new CartLine("sku-1", 1, new BigDecimal("5.00"))),
                new BigDecimal("5.00"), Instant.parse(createdAt));
    }

    @Test
    void findsASavedOrderById() {
        Order order = order("o-1", "c-1", "2024-03-01T10:00:00Z");

        repository.save(order);

        assertEquals(order, repository.findById("o-1").orElseThrow());
        assertTrue(repository.findById("o-2").isEmpty());
    }

    @Test
    void savingAgainReplacesTheOrder() {
        repository.save(order("o-1", "c-1", "2024-03-01T10:00:00Z"));
        Order changed = order("o-1", "c-2", "2024-03-01T11:00:00Z");

        repository.save(changed);

        assertEquals(changed, repository.findById("o-1").orElseThrow());
    }

    @Test
    void listsTheOrdersOfACustomerOldestFirst() {
        repository.save(order("o-2", "c-1", "2024-03-02T10:00:00Z"));
        repository.save(order("o-1", "c-1", "2024-03-01T10:00:00Z"));
        repository.save(order("o-3", "c-2", "2024-03-01T09:00:00Z"));

        List<String> ids = repository.findByCustomer("c-1").stream().map(Order::id).toList();

        assertEquals(List.of("o-1", "o-2"), ids);
    }
}
