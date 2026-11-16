package com.example.checkout.pricing;

import com.example.checkout.domain.Basket;
import com.example.checkout.domain.Customer;

public class CheckoutPricing {

    private final ShippingFeeCalculator shipping;

    public CheckoutPricing(ShippingFeeCalculator shipping) {
        this.shipping = shipping;
    }

    public PriceBreakdown price(Basket basket, Customer customer, Destination destination) {
        if (basket.lines().isEmpty()) {
            throw new IllegalArgumentException("cannot price an empty basket");
        }
        return new PriceBreakdown(basket.subtotal(), shipping.feeFor(basket, customer, destination));
    }
}
