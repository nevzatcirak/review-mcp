package com.example.checkout.pricing;

import java.math.BigDecimal;

public enum Destination {
    DOMESTIC(new BigDecimal("4.90")),
    INTERNATIONAL(new BigDecimal("14.90"));

    private final BigDecimal standardFee;

    Destination(BigDecimal standardFee) {
        this.standardFee = standardFee;
    }

    public BigDecimal standardFee() {
        return standardFee;
    }
}
