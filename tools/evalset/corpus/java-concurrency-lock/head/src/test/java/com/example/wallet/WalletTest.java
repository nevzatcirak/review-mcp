package com.example.wallet;

import org.junit.jupiter.api.Test;

import java.math.BigDecimal;
import java.time.Clock;
import java.time.LocalDate;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.TimeUnit;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;

class WalletTest {

    private final Wallet wallet = new Wallet("c-1", Clock.systemUTC());

    @Test
    void depositAndWithdraw() throws Exception {
        wallet.deposit(new BigDecimal("20.00"), "gift-card");
        wallet.withdraw(new BigDecimal("7.50"), "order-1");

        assertEquals(new BigDecimal("12.50"), wallet.balance());
        assertEquals(2, wallet.history().size());
    }

    @Test
    void refusesToOverdraw() {
        assertThrows(InsufficientFundsException.class, () -> wallet.withdraw(BigDecimal.ONE, "order-2"));
    }

    @Test
    void pendingCreditIsReleasedOnItsDay() {
        wallet.addPending(new PendingCredit(new BigDecimal("3.00"), "cashback-order-1", LocalDate.of(2024, 3, 15)));

        assertEquals(0, wallet.releaseDue(LocalDate.of(2024, 3, 14)));
        assertEquals(1, wallet.releaseDue(LocalDate.of(2024, 3, 15)));
        assertEquals(new BigDecimal("3.00"), wallet.balance());
        assertEquals(0, wallet.releaseDue(LocalDate.of(2024, 3, 16)));
    }

    @Test
    void concurrentDepositsAreNotLost() throws Exception {
        ExecutorService pool = Executors.newFixedThreadPool(8);
        for (int i = 0; i < 1000; i++) {
            pool.submit(() -> wallet.deposit(BigDecimal.ONE, "bonus"));
        }
        pool.shutdown();
        pool.awaitTermination(10, TimeUnit.SECONDS);

        assertEquals(new BigDecimal("1000"), wallet.balance());
    }
}
