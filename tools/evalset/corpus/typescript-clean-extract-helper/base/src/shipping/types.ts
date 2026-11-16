export type Zone = "pickup" | "domestic" | "eu" | "world";

export interface ParcelSpec {
  weightKg: number;
}

export interface ShippingQuote {
  carrier: string;
  /** The amount charged for shipping. */
  amount: number;
}
