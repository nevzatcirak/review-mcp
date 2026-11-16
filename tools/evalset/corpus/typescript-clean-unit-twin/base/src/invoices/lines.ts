import type { Order } from "../orders/types.js";
import { parcelFor, quoteShipping } from "../shipping/quotes.js";
import type { ShippingQuote } from "../shipping/types.js";

export interface InvoiceLine {
  label: string;
  quantity: number;
  amount: number;
}

export function itemLines(order: Order): InvoiceLine[] {
  return order.items.map((line) => ({
    label: `${line.item.name} (${line.item.sku})`,
    quantity: line.quantity,
    amount: line.item.unitPrice * line.quantity,
  }));
}

/** The shipping quote of an order, or undefined when the customer picks it up. */
export function shippingQuoteFor(order: Order): ShippingQuote | undefined {
  if (order.zone === "pickup") {
    return undefined;
  }
  return quoteShipping(parcelFor(order.items), order.zone);
}
