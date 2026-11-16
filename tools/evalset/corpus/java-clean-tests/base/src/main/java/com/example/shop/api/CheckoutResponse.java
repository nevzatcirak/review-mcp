package com.example.shop.api;

import java.math.BigDecimal;

public record CheckoutResponse(String orderId, BigDecimal total) {
}
