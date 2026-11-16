package com.example.wholesale.pricing

import java.math.BigDecimal
import java.math.RoundingMode

/** Prices are quoted in whole cents, rounded half up. */
fun BigDecimal.toCents(): BigDecimal = setScale(2, RoundingMode.HALF_UP)
