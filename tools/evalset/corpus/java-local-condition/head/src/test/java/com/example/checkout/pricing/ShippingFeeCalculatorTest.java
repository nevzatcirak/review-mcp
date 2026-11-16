package com.example.checkout.pricing;

import com.example.checkout.domain.Basket;
import com.example.checkout.domain.BasketLine;
import com.example.checkout.domain.Customer;
import org.junit.jupiter.api.Test;

import java.math.BigDecimal;
import java.util.List;

import static org.junit.jupiter.api.Assertions.assertEquals;

class ShippingFeeCalculatorTest {

    private final ShippingFeeCalculator calculator = new ShippingFeeCalculator();
    private final Customer member = new Customer("c-1", "ada@example.com", true);
    private final Customer guest = new Customer("c-2", "bo@example.com", false);

    private static Basket basketOf(String amount) {
        return new Basket(List.of(new BasketLine("item", 1, new BigDecimal(amount))));
    }

    @Test
    void membersShipFreeFromFiftyEuros() {
        assertEquals(BigDecimal.ZERO, calculator.feeFor(basketOf("50.00"), member, Destination.DOMESTIC));
        assertEquals(BigDecimal.ZERO, calculator.feeFor(basketOf("72.30"), member, Destination.DOMESTIC));
    }

    @Test
    void guestsPayTheStandardFee() {
        assertEquals(new BigDecimal("4.90"), calculator.feeFor(basketOf("20.00"), guest, Destination.DOMESTIC));
    }

    @Test
    void internationalShippingIsNeverFree() {
        assertEquals(new BigDecimal("14.90"), calculator.feeFor(basketOf("80.00"), member, Destination.INTERNATIONAL));
    }
}
