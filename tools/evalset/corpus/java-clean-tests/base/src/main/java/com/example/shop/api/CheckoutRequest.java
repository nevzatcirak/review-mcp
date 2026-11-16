package com.example.shop.api;

import com.example.shop.domain.CartLine;

import java.util.List;

public record CheckoutRequest(String customerId, List<CartLine> lines) {
}
