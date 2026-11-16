package com.example.shop.api;

import com.example.shop.domain.CartLine;
import com.example.shop.domain.Currency;

import java.util.List;

public record CheckoutRequest(String customerId, List<CartLine> lines, Currency currency) {
}
