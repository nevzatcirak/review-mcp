import { describe, expect, it } from "vitest";
import { quoteShipping } from "../src/shipping/quotes.js";

describe("quoteShipping", () => {
  it("charges the base rate plus each started kilogram", () => {
    expect(quoteShipping({ weightKg: 1.2 }, "domestic")).toEqual({ carrier: "parcel-post", amount: 5.6 });
  });

  it("is free for pickup", () => {
    expect(quoteShipping({ weightKg: 9 }, "pickup").amount).toBe(0);
  });
});
