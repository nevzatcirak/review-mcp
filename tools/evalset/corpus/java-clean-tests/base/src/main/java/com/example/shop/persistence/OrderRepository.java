package com.example.shop.persistence;

import com.example.shop.domain.Order;

import java.util.List;
import java.util.Optional;

public interface OrderRepository {

    void save(Order order);

    Optional<Order> findById(String id);

    List<Order> findByCustomer(String customerId);
}
