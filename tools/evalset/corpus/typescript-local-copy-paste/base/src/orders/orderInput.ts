import type { Address } from "../address/types.js";

export interface OrderItemInput {
  sku: string;
  quantity: number;
}

/** The request body of POST /orders on the orders service. */
export interface OrderInput {
  email: string;
  items: OrderItemInput[];
  shippingAddress: Address;
  billingAddress: Address;
}
