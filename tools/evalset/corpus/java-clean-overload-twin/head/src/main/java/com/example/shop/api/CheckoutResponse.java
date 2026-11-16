package com.example.shop.api;

import com.example.shop.domain.Currency;

import java.math.BigDecimal;

public record CheckoutResponse(String orderId, BigDecimal total, Currency currency) {
}
