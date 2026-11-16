import { describe, expect, it } from "vitest";
import { summarize } from "../src/checkout/summary.js";

const mug = { sku: "mug-1", name: "Mug", unitPrice: 8.5, weightKg: 0.4 };

describe("summarize", () => {
  it("adds shipping to the items", () => {
    expect(summarize([{ item: mug, quantity: 2 }], "domestic")).toEqual({
      items: "17.00 EUR",
      shipping: "5.25 EUR",
      total: "22.25 EUR",
    });
  });
});
