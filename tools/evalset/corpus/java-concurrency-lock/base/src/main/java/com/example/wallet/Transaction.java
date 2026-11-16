package com.example.wallet;

import java.math.BigDecimal;
import java.time.Instant;

public record Transaction(Kind kind, BigDecimal amount, String reference, Instant at) {

    public enum Kind {
        DEPOSIT,
        WITHDRAWAL
    }
}
