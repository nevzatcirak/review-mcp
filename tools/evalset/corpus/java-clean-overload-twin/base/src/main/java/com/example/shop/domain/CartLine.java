package com.example.shop.domain;

import java.math.BigDecimal;

public record CartLine(String sku, int quantity, BigDecimal unitPrice) {

    public CartLine {
        if (quantity <= 0) {
            throw new IllegalArgumentException("quantity must be positive");
        }
    }

    public BigDecimal amount() {
        return unitPrice.multiply(BigDecimal.valueOf(quantity));
    }
}
