import type { OrderItem } from "../orders/types.js";
import { parcelFor, quoteShipping } from "../shipping/quotes.js";
import type { Zone } from "../shipping/types.js";
import { sumBy } from "../util/sum.js";

export interface Summary {
  items: string;
  shipping: string;
  total: string;
}

const euro = (amount: number): string => `${amount.toFixed(2)} EUR`;

export function summarize(items: OrderItem[], zone: Zone): Summary {
  const itemsTotal = sumBy(items, (line) => line.item.unitPrice * line.quantity);
  const quote = quoteShipping(parcelFor(items), zone);
  return {
    items: euro(itemsTotal),
    shipping: euro(quote.amount),
    total: euro(itemsTotal + quote.amount),
  };
}
