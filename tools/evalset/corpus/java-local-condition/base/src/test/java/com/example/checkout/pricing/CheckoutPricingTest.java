package com.example.checkout.pricing;

import com.example.checkout.domain.Basket;
import com.example.checkout.domain.BasketLine;
import com.example.checkout.domain.Customer;
import org.junit.jupiter.api.Test;

import java.math.BigDecimal;
import java.util.List;

import static org.junit.jupiter.api.Assertions.assertEquals;

class CheckoutPricingTest {

    private final CheckoutPricing pricing = new CheckoutPricing(new ShippingFeeCalculator());
    private final Customer guest = new Customer("c-1", "ada@example.com", false);

    @Test
    void addsTheStandardFeeOfTheDestination() {
        Basket basket = new Basket(List.of(new BasketLine("mug-1", 2, new BigDecimal("8.50"))));

        PriceBreakdown breakdown = pricing.price(basket, guest, Destination.INTERNATIONAL);

        assertEquals(new BigDecimal("17.00"), breakdown.subtotal());
        assertEquals(new BigDecimal("31.90"), breakdown.total());
    }
}
