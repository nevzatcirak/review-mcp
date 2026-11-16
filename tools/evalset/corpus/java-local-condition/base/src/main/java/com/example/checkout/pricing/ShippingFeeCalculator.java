package com.example.checkout.pricing;

import com.example.checkout.domain.Basket;
import com.example.checkout.domain.Customer;

import java.math.BigDecimal;

public class ShippingFeeCalculator {

    public BigDecimal feeFor(Basket basket, Customer customer, Destination destination) {
        return destination.standardFee();
    }
}
