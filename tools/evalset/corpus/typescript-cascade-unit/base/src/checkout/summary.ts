import type { OrderItem } from "../orders/types.js";
import { parcelFor, quoteShipping } from "../shipping/quotes.js";
import type { Zone } from "../shipping/types.js";

export interface Summary {
  items: string;
  shipping: string;
  total: string;
}

const euro = (amount: number): string => `${amount.toFixed(2)} EUR`;

export function summarize(items: OrderItem[], zone: Zone): Summary {
  const itemsTotal = items.reduce((sum, line) => sum + line.item.unitPrice * line.quantity, 0);
  const quote = quoteShipping(parcelFor(items), zone);
  return {
    items: euro(itemsTotal),
    shipping: euro(quote.amount),
    total: euro(itemsTotal + quote.amount),
  };
}
