import { describe, expect, it } from "vitest";
import { sumBy } from "../src/util/sum.js";

describe("sumBy", () => {
  it("adds up the values", () => {
    expect(sumBy([{ n: 1 }, { n: 2.5 }], (x) => x.n)).toBe(3.5);
  });

  it("is 0 for no items", () => {
    expect(sumBy([], () => 1)).toBe(0);
  });
});
