package com.example.wallet;

import java.math.BigDecimal;
import java.time.Clock;
import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.locks.ReentrantLock;

/**
 * A customer's store credit. Safe for concurrent use: every method takes
 * the wallet's lock.
 */
public class Wallet {

    private final String customerId;
    private final Clock clock;
    private final ReentrantLock lock = new ReentrantLock();
    private final List<Transaction> history = new ArrayList<>();
    private BigDecimal balance = BigDecimal.ZERO;

    public Wallet(String customerId, Clock clock) {
        this.customerId = customerId;
        this.clock = clock;
    }

    public String customerId() {
        return customerId;
    }

    public void deposit(BigDecimal amount, String reference) {
        requirePositive(amount);
        lock.lock();
        try {
            balance = balance.add(amount);
            history.add(new Transaction(Transaction.Kind.DEPOSIT, amount, reference, clock.instant()));
        } finally {
            lock.unlock();
        }
    }

    public void withdraw(BigDecimal amount, String reference) throws InsufficientFundsException {
        requirePositive(amount);
        lock.lock();
        try {
            if (balance.compareTo(amount) < 0) {
                throw new InsufficientFundsException(amount, balance);
            }
            balance = balance.subtract(amount);
            history.add(new Transaction(Transaction.Kind.WITHDRAWAL, amount, reference, clock.instant()));
        } finally {
            lock.unlock();
        }
    }

    public BigDecimal balance() {
        lock.lock();
        try {
            return balance;
        } finally {
            lock.unlock();
        }
    }

    public List<Transaction> history() {
        lock.lock();
        try {
            return List.copyOf(history);
        } finally {
            lock.unlock();
        }
    }

    private static void requirePositive(BigDecimal amount) {
        if (amount.signum() <= 0) {
            throw new IllegalArgumentException("amount must be positive");
        }
    }
}
