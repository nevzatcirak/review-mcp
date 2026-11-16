import type { Item } from "../catalog/types.js";
import type { Zone } from "../shipping/types.js";

export interface OrderItem {
  item: Item;
  quantity: number;
}

export interface Order {
  id: string;
  customerEmail: string;
  items: OrderItem[];
  zone: Zone;
}
