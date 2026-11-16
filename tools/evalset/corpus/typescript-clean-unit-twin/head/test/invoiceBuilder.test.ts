import { describe, expect, it } from "vitest";
import { buildInvoice } from "../src/invoices/invoiceBuilder.js";

const mug = { sku: "mug-1", name: "Mug", unitPrice: 8.5, weightKg: 0.4 };

describe("buildInvoice", () => {
  it("lists the items and the total for a pickup order", () => {
    const invoice = buildInvoice(
      { id: "o-7", customerEmail: "ada@example.com", items: [{ item: mug, quantity: 2 }], zone: "pickup" },
      12,
    );

    expect(invoice.number).toBe("INV-O-7-0012");
    expect(invoice.lines).toHaveLength(1);
    expect(invoice.total).toBe("€17.00");
  });

  it("adds the shipping line in euros", () => {
    const invoice = buildInvoice(
      { id: "o-8", customerEmail: "bo@example.com", items: [{ item: mug, quantity: 2 }], zone: "domestic" },
      13,
    );

    expect(invoice.lines[1]).toEqual({ label: "Shipping (parcel-post)", quantity: 1, amount: "€5.25" });
    expect(invoice.total).toBe("€22.25");
  });
});
