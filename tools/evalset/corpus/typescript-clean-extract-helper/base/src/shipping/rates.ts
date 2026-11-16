import type { Zone } from "./types.js";

export interface Rate {
  carrier: string;
  base: number;
  perKg: number;
}

/** Tariffs of the carriers per zone, in euros. */
export const RATES: Record<Zone, Rate> = {
  pickup: { carrier: "store", base: 0, perKg: 0 },
  domestic: { carrier: "parcel-post", base: 4.9, perKg: 0.35 },
  eu: { carrier: "parcel-post", base: 9.9, perKg: 0.8 },
  world: { carrier: "air-freight", base: 24.0, perKg: 2.1 },
};
