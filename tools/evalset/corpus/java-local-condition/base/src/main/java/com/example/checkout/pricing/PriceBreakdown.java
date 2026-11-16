package com.example.checkout.pricing;

import java.math.BigDecimal;

public record PriceBreakdown(BigDecimal subtotal, BigDecimal shipping) {

    public BigDecimal total() {
        return subtotal.add(shipping);
    }
}
