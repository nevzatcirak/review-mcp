package com.example.shop.service;

import com.example.shop.domain.Cart;
import com.example.shop.domain.Order;
import com.example.shop.persistence.OrderRepository;

import java.time.Clock;
import java.util.UUID;

public class OrderService {

    private final OrderRepository repository;
    private final Clock clock;

    public OrderService(OrderRepository repository, Clock clock) {
        this.repository = repository;
        this.clock = clock;
    }

    public Order place(Cart cart) {
        if (cart.isEmpty()) {
            throw new IllegalArgumentException("cannot place an empty cart");
        }
        Order order = new Order(UUID.randomUUID().toString(), cart.customerId(), cart.lines(),
                cart.total(), clock.instant());
        repository.save(order);
        return order;
    }
}
