import type { Order } from "./types.js";

export interface OrderRepository {
  find(id: string): Promise<Order | undefined>;
  listForCustomer(customerId: string): Promise<Order[]>;
}

export class InMemoryOrderRepository implements OrderRepository {
  constructor(private readonly orders: Order[] = []) {}

  async find(id: string): Promise<Order | undefined> {
    return this.orders.find((order) => order.id === id);
  }

  async listForCustomer(customerId: string): Promise<Order[]> {
    return this.orders.filter((order) => order.customerId === customerId);
  }
}
