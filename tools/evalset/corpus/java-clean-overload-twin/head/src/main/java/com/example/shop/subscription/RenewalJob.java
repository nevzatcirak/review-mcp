package com.example.shop.subscription;

import com.example.shop.domain.Cart;
import com.example.shop.domain.CartLine;
import com.example.shop.domain.Order;
import com.example.shop.service.OrderService;

import java.time.LocalDate;
import java.util.ArrayList;
import java.util.List;

/**
 * Places one order per subscription that is due, then moves the subscription
 * to its next renewal date. Runs once a day.
 */
public class RenewalJob {

    private final SubscriptionStore subscriptions;
    private final OrderService orderService;

    public RenewalJob(SubscriptionStore subscriptions, OrderService orderService) {
        this.subscriptions = subscriptions;
        this.orderService = orderService;
    }

    public List<Order> runFor(LocalDate day) {
        List<Order> placed = new ArrayList<>();
        for (Subscription subscription : subscriptions.dueOn(day)) {
            placed.add(renew(subscription));
        }
        return placed;
    }

    Order renew(Subscription subscription) {
        Cart cart = new Cart(subscription.customerId(),
                List.of(new CartLine(subscription.planSku(), 1, subscription.monthlyPrice())));
        Order order = orderService.place(cart, subscription.currency());
        subscriptions.update(subscription.renewed());
        return order;
    }
}
