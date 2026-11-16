package com.example.shop.domain;

import org.junit.jupiter.api.Test;

import java.math.BigDecimal;
import java.util.ArrayList;
import java.util.List;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

class CartTest {

    @Test
    void totalAddsUpTheLines() {
        Cart cart = new Cart("c-1", List.of(
                new CartLine("sku-1", 3, new BigDecimal("2.50")),
                new CartLine("sku-2", 1, new BigDecimal("10.00"))));

        assertEquals(new BigDecimal("17.50"), cart.total());
    }

    @Test
    void anEmptyCartTotalsZero() {
        Cart cart = new Cart("c-1", List.of());

        assertTrue(cart.isEmpty());
        assertEquals(BigDecimal.ZERO, cart.total());
    }

    @Test
    void theCartKeepsItsOwnCopyOfTheLines() {
        List<CartLine> lines = new ArrayList<>(List.of(new CartLine("sku-1", 1, BigDecimal.ONE)));
        Cart cart = new Cart("c-1", lines);

        lines.clear();

        assertEquals(1, cart.lines().size());
    }

    @Test
    void aLineNeedsAPositiveQuantity() {
        assertThrows(IllegalArgumentException.class, () -> new CartLine("sku-1", 0, BigDecimal.ONE));
    }
}
