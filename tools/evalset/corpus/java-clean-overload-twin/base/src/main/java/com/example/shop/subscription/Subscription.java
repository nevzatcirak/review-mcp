package com.example.shop.subscription;

import com.example.shop.domain.Currency;

import java.math.BigDecimal;
import java.time.LocalDate;

public record Subscription(String id, String customerId, String planSku, BigDecimal monthlyPrice,
                           Currency currency, LocalDate nextRenewal) {

    public Subscription renewed() {
        return new Subscription(id, customerId, planSku, monthlyPrice, currency, nextRenewal.plusMonths(1));
    }
}
