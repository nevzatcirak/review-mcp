package com.example.wallet;

import java.math.BigDecimal;
import java.time.LocalDate;

/** Credit that becomes spendable on availableOn, such as cashback after the return period. */
public record PendingCredit(BigDecimal amount, String reference, LocalDate availableOn) {
}
