import { describe, expect, it } from "vitest";
import { createApp } from "../src/app.js";
import { InMemoryOrderRepository } from "../src/orders/repository.js";

const ada = { id: "c-1", email: "ada@example.com" };
const bo = { id: "c-2", email: "bo@example.com" };
const app = createApp(
  new InMemoryOrderRepository([
    {
      id: "o-1",
      customerId: "c-1",
      placedAt: "2024-03-01T10:00:00Z",
      billingName: "Ada Example",
      billingAddress: "Example Street 1, 10115 Berlin",
      lines: [{ sku: "mug-1", name: "Mug", quantity: 2, unitPriceCents: 850 }],
    },
  ]),
);

describe("order routes", () => {
  it("shows a customer their own order", async () => {
    const res = await app.handle({ method: "GET", path: "/orders/o-1", user: ada });

    expect(res.status).toBe(200);
    expect(res.body).toMatchObject({ id: "o-1", totalCents: 1700 });
  });

  it("hides other customers' orders", async () => {
    expect((await app.handle({ method: "GET", path: "/orders/o-1", user: bo })).status).toBe(404);
  });

  it("renders the receipt of an order", async () => {
    const res = await app.handle({ method: "GET", path: "/orders/o-1/receipt", user: ada });

    expect(res.status).toBe(200);
    expect(res.headers).toEqual({ "Content-Type": "text/plain; charset=utf-8" });
    expect(res.body).toContain("Receipt for order o-1");
    expect(res.body).toContain("17.00 EUR");
  });

  it("answers 404 for an unknown order's receipt", async () => {
    expect((await app.handle({ method: "GET", path: "/orders/o-9/receipt", user: ada })).status).toBe(404);
  });
});
