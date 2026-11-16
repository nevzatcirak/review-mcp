package com.example.shop.api;

import com.example.shop.domain.Cart;
import com.example.shop.domain.Order;
import com.example.shop.service.OrderService;

public class CheckoutController {

    private final OrderService orderService;

    public CheckoutController(OrderService orderService) {
        this.orderService = orderService;
    }

    public CheckoutResponse submit(CheckoutRequest request) {
        Cart cart = new Cart(request.customerId(), request.lines());
        Order order = orderService.place(cart);
        return new CheckoutResponse(order.id(), order.total());
    }
}
