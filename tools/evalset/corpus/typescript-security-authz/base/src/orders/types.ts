export interface OrderLine {
  sku: string;
  name: string;
  quantity: number;
  unitPriceCents: number;
}

export interface Order {
  id: string;
  customerId: string;
  placedAt: string;
  billingName: string;
  billingAddress: string;
  lines: OrderLine[];
}
