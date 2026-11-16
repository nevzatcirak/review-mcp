package com.example.wallet;

import java.math.BigDecimal;

public class InsufficientFundsException extends Exception {

    public InsufficientFundsException(BigDecimal requested, BigDecimal available) {
        super("requested " + requested + " but only " + available + " is available");
    }
}
