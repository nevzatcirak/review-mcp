package com.example.shop.domain;

import java.math.BigDecimal;
import java.time.Instant;
import java.util.List;

public record Order(String id, String customerId, List<CartLine> lines, BigDecimal total, Instant createdAt) {

    public Order {
        lines = List.copyOf(lines);
    }
}
