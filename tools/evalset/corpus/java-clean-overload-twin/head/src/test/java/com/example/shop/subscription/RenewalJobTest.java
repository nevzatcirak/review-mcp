package com.example.shop.subscription;

import com.example.shop.domain.Currency;
import com.example.shop.domain.Order;
import com.example.shop.persistence.InMemoryOrderRepository;
import com.example.shop.service.OrderService;
import org.junit.jupiter.api.Test;

import java.math.BigDecimal;
import java.time.Clock;
import java.time.LocalDate;
import java.util.ArrayList;
import java.util.List;
import java.util.Optional;

import static org.junit.jupiter.api.Assertions.assertEquals;

class RenewalJobTest {

    @Test
    void placesOneOrderPerDueSubscription() {
        LocalDate day = LocalDate.of(2024, 3, 1);
        List<Subscription> stored = new ArrayList<>(List.of(
                new Subscription("s-1", "c-1", "plan-basic", new BigDecimal("9.90"), Currency.EUR, day)));
        SubscriptionStore store = new SubscriptionStore() {
            public List<Subscription> dueOn(LocalDate d) {
                return stored.stream().filter(s -> s.nextRenewal().equals(d)).toList();
            }

            public void update(Subscription subscription) {
                stored.replaceAll(s -> s.id().equals(subscription.id()) ? subscription : s);
            }
        };
        RenewalJob job = new RenewalJob(store, new OrderService(new InMemoryOrderRepository(), Clock.systemUTC()));

        List<Order> orders = job.runFor(day);

        assertEquals(1, orders.size());
        assertEquals(new BigDecimal("9.90"), orders.get(0).total());
        assertEquals(day.plusMonths(1), stored.get(0).nextRenewal());
    }

    @Test
    void storesTheRenewalInTheSubscriptionCurrency() {
        LocalDate day = LocalDate.of(2024, 3, 1);
        InMemoryOrderRepository repository = new InMemoryOrderRepository();
        Subscription subscription = new Subscription("s-2", "c-2", "plan-pro", new BigDecimal("19.00"),
                Currency.USD, day);
        SubscriptionStore store = new SubscriptionStore() {
            public List<Subscription> dueOn(LocalDate d) {
                return List.of(subscription);
            }

            public void update(Subscription renewed) {
            }
        };
        RenewalJob job = new RenewalJob(store, new OrderService(repository, Clock.systemUTC()));

        Order order = job.runFor(day).get(0);

        assertEquals(Optional.of(Currency.USD), repository.currencyOf(order.id()));
    }
}
