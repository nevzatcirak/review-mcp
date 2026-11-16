package com.example.checkout.domain;

import java.math.BigDecimal;
import java.util.List;

public record Basket(List<BasketLine> lines) {

    public Basket {
        lines = List.copyOf(lines);
    }

    public BigDecimal subtotal() {
        return lines.stream().map(BasketLine::amount).reduce(BigDecimal.ZERO, BigDecimal::add);
    }
}
