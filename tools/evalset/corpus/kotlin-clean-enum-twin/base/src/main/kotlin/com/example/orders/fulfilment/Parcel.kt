package com.example.orders.fulfilment

/** A parcel handed to the carrier, with the quantity shipped per SKU. */
data class Parcel(val trackingCode: String, val quantities: Map<String, Int>)
