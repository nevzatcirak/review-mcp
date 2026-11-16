package com.example.wallet.jobs;

import com.example.wallet.Wallet;
import com.example.wallet.WalletRegistry;

import java.time.Clock;
import java.time.LocalDate;
import java.util.concurrent.Executors;
import java.util.concurrent.ScheduledExecutorService;
import java.util.concurrent.TimeUnit;

/** Releases due pending credits of every wallet, every hour, on its own thread. */
public class PendingCreditReleaseJob implements Runnable {

    private final WalletRegistry wallets;
    private final Clock clock;

    public PendingCreditReleaseJob(WalletRegistry wallets, Clock clock) {
        this.wallets = wallets;
        this.clock = clock;
    }

    public ScheduledExecutorService schedule() {
        ScheduledExecutorService executor = Executors.newSingleThreadScheduledExecutor();
        executor.scheduleAtFixedRate(this, 0, 1, TimeUnit.HOURS);
        return executor;
    }

    @Override
    public void run() {
        LocalDate today = LocalDate.now(clock);
        for (Wallet wallet : wallets.all()) {
            wallet.releaseDue(today);
        }
    }
}
