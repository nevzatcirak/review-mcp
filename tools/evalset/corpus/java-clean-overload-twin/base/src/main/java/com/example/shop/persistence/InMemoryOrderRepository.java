package com.example.shop.persistence;

import com.example.shop.domain.Order;

import java.util.ArrayList;
import java.util.Comparator;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.concurrent.ConcurrentHashMap;

public class InMemoryOrderRepository implements OrderRepository {

    private final Map<String, Order> orders = new ConcurrentHashMap<>();

    public void save(Order order) {
        orders.put(order.id(), order);
    }

    public Optional<Order> findById(String id) {
        return Optional.ofNullable(orders.get(id));
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
