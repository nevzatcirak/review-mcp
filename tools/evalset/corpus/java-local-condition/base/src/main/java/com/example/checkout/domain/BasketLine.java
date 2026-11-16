package com.example.checkout.domain;

import java.math.BigDecimal;

public record BasketLine(String sku, int quantity, BigDecimal unitPrice) {

    public BigDecimal amount() {
        return unitPrice.multiply(BigDecimal.valueOf(quantity));
    }
}
