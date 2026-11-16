package com.example.wallet;

import java.time.Clock;
import java.util.Collection;
import java.util.List;
import java.util.Map;
import java.util.concurrent.ConcurrentHashMap;

public class WalletRegistry {

    private final Map<String, Wallet> wallets = new ConcurrentHashMap<>();
    private final Clock clock;

    public WalletRegistry(Clock clock) {
        this.clock = clock;
    }

    public Wallet forCustomer(String customerId) {
        return wallets.computeIfAbsent(customerId, id -> new Wallet(id, clock));
    }

    public Collection<Wallet> all() {
        return List.copyOf(wallets.values());
    }
}
