package com.example.shop.subscription;

import java.time.LocalDate;
import java.util.List;

public interface SubscriptionStore {

    List<Subscription> dueOn(LocalDate day);

    void update(Subscription subscription);
}
