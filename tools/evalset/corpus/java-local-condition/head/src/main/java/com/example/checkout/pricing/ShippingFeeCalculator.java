package com.example.checkout.pricing;

import com.example.checkout.domain.Basket;
import com.example.checkout.domain.Customer;

import java.math.BigDecimal;

public class ShippingFeeCalculator {

    static final BigDecimal MEMBER_FREE_SHIPPING_FROM = new BigDecimal("50.00");

    public BigDecimal feeFor(Basket basket, Customer customer, Destination destination) {
        if (destination == Destination.DOMESTIC
                && (customer.member() || basket.subtotal().compareTo(MEMBER_FREE_SHIPPING_FROM) >= 0)) {
            return BigDecimal.ZERO;
        }
        return destination.standardFee();
    }
}
