import { describe, expect, it } from "vitest";
import { InvalidFormError, toOrderInput, type CheckoutForm } from "../src/forms/checkoutForm.js";

const form: CheckoutForm = {
  email: "ada@example.com",
  shippingName: "Ada Example",
  shippingStreet: "Example Street 1",
  shippingPostalCode: "10115",
  shippingCity: "Berlin",
  shippingCountry: "de",
};

describe("toOrderInput", () => {
  it("bills the shipping address", () => {
    const input = toOrderInput(form, [{ sku: "mug-1", quantity: 1 }]);

    expect(input.shippingAddress.country).toBe("DE");
    expect(input.billingAddress).toEqual(input.shippingAddress);
  });

  it("names the invalid fields", () => {
    expect(() => toOrderInput({ ...form, shippingPostalCode: "1011" }, [])).toThrow(InvalidFormError);
  });
});
