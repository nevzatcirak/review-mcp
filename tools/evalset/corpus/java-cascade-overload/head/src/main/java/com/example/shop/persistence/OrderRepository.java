package com.example.shop.persistence;

import com.example.shop.domain.Currency;
import com.example.shop.domain.Order;

import java.util.List;
import java.util.Optional;

public interface OrderRepository {

    void save(Order order, Currency currency);

    /**
     * Stores an order in the shop's original currency. Kept for callers that
     * predate multi-currency checkout.
     */
    default void save(Order order) {
        save(order, Currency.EUR);
    }

    Optional<Order> findById(String id);

    Optional<Currency> currencyOf(String orderId);

    List<Order> findByCustomer(String customerId);
}
