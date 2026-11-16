package com.example.shop.domain;

import java.math.BigDecimal;
import java.util.List;

public record Cart(String customerId, List<CartLine> lines) {

    public Cart {
        lines = List.copyOf(lines);
    }

    public boolean isEmpty() {
        return lines.isEmpty();
    }

    public BigDecimal total() {
        return lines.stream()
                .map(CartLine::amount)
                .reduce(BigDecimal.ZERO, BigDecimal::add);
    }
}
