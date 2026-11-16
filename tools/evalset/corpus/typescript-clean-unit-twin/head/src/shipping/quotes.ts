import type { OrderItem } from "../orders/types.js";
import { RATES } from "./rates.js";
import type { ParcelSpec, ShippingQuote, Zone } from "./types.js";

export function parcelFor(items: OrderItem[]): ParcelSpec {
  const weightKg = items.reduce((sum, line) => sum + line.item.weightKg * line.quantity, 0);
  return { weightKg };
}

export function quoteShipping(parcel: ParcelSpec, zone: Zone): ShippingQuote {
  const rate = RATES[zone];
  const billableKg = Math.ceil(parcel.weightKg);
  const cents = Math.round(rate.base * 100) + Math.round(rate.perKg * 100) * billableKg;
  return { carrier: rate.carrier, amount: cents };
}
