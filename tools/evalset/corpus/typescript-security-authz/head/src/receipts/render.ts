import type { Order } from "../orders/types.js";

const euros = (cents: number): string => `${Math.floor(cents / 100)}.${String(cents % 100).padStart(2, "0")} EUR`;

/** A plain-text receipt for printing or attaching to an email. */
export function renderReceipt(order: Order): string {
  const rows = order.lines.map(
    (line) => `${String(line.quantity).padStart(3)} x ${line.name.padEnd(24)} ${euros(line.quantity * line.unitPriceCents).padStart(12)}`,
  );
  const total = order.lines.reduce((sum, line) => sum + line.quantity * line.unitPriceCents, 0);
  return [
    `Receipt for order ${order.id}`,
    `Placed ${order.placedAt}`,
    `${order.billingName}, ${order.billingAddress}`,
    "",
    ...rows,
    "",
    `${"Total".padEnd(30)} ${euros(total).padStart(12)}`,
  ].join("\n");
}
