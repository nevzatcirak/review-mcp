import type { Order } from "../orders/types.js";
import { formatEuro, invoiceNumber } from "./format.js";
import { itemLines, shippingQuoteFor, type InvoiceLine } from "./lines.js";

export interface Invoice {
  number: string;
  to: string;
  lines: { label: string; quantity: number; amount: string }[];
  total: string;
}

export function buildInvoice(order: Order, sequence: number): Invoice {
  const lines: InvoiceLine[] = itemLines(order);
  const shipping = shippingQuoteFor(order);
  if (shipping) {
    lines.push({ label: `Shipping (${shipping.carrier})`, quantity: 1, amount: shipping.amount });
  }
  const total = lines.reduce((sum, line) => sum + line.amount, 0);
  return {
    number: invoiceNumber(order.id, sequence),
    to: order.customerEmail,
    lines: lines.map((line) => ({ label: line.label, quantity: line.quantity, amount: formatEuro(line.amount) })),
    total: formatEuro(total),
  };
}
