import type { OrderItem } from "../orders/types.js";
import { sumBy } from "../util/sum.js";
import { RATES } from "./rates.js";
import type { ParcelSpec, ShippingQuote, Zone } from "./types.js";

export function parcelFor(items: OrderItem[]): ParcelSpec {
  const weightKg = sumBy(items, (line) => line.item.weightKg * line.quantity);
  return { weightKg };
}

export function quoteShipping(parcel: ParcelSpec, zone: Zone): ShippingQuote {
  const rate = RATES[zone];
  const billableKg = Math.ceil(parcel.weightKg);
  const amount = rate.base + rate.perKg * billableKg;
  return { carrier: rate.carrier, amount: Math.round(amount * 100) / 100 };
}
