import { Router } from "./http/router.js";
import type { OrderRepository } from "./orders/repository.js";
import { registerOrderRoutes } from "./orders/routes.js";

export function createApp(orders: OrderRepository): Router {
  const router = new Router();
  registerOrderRoutes(router, orders);
  return router;
}
