import type { Router } from "../http/router.js";
import { notFound, ok } from "../http/types.js";
import type { OrderRepository } from "./repository.js";

const totalCents = (lines: { quantity: number; unitPriceCents: number }[]): number =>
  lines.reduce((sum, line) => sum + line.quantity * line.unitPriceCents, 0);

export function registerOrderRoutes(router: Router, orders: OrderRepository): void {
  router.add("GET", "/orders", async (req) => {
    const list = await orders.listForCustomer(req.user.id);
    return ok(list.map((order) => ({ id: order.id, placedAt: order.placedAt, totalCents: totalCents(order.lines) })));
  });

  router.add("GET", "/orders/:id", async (req) => {
    const order = await orders.find(req.params.id);
    if (!order || order.customerId !== req.user.id) {
      return notFound();
    }
    return ok({ ...order, totalCents: totalCents(order.lines) });
  });
}
