package com.example.shop.persistence;

import com.example.shop.domain.Currency;
import com.example.shop.domain.Order;

import java.util.ArrayList;
import java.util.Comparator;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.concurrent.ConcurrentHashMap;

public class InMemoryOrderRepository implements OrderRepository {

    private final Map<String, Order> orders = new ConcurrentHashMap<>();
    private final Map<String, Currency> currencies = new ConcurrentHashMap<>();

    public void save(Order order, Currency currency) {
        orders.put(order.id(), order);
        currencies.put(order.id(), currency);
    }

    public Optional<Order> findById(String id) {
        return Optional.ofNullable(orders.get(id));
    }

    public Optional<Currency> currencyOf(String orderId) {
        return Optional.ofNullable(currencies.get(orderId));
    }

    public List<Order> findByCustomer(String customerId) {
        List<Order> result = new ArrayList<>();
        for (Order order : orders.values()) {
            if (order.customerId().equals(customerId)) {
                result.add(order);
            }
        }
        result.sort(Comparator.comparing(Order::createdAt));
        return result;
    }
}
