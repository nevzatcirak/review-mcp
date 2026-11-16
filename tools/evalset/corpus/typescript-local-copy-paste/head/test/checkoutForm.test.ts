import { describe, expect, it } from "vitest";
import { InvalidFormError, toOrderInput, type CheckoutForm } from "../src/forms/checkoutForm.js";

const form: CheckoutForm = {
  email: "ada@example.com",
  shippingName: "Ada Example",
  shippingStreet: "Example Street 1",
  shippingPostalCode: "10115",
  shippingCity: "Berlin",
  shippingCountry: "de",
  billingSameAsShipping: true,
};

describe("toOrderInput", () => {
  it("bills the shipping address unless told otherwise", () => {
    const input = toOrderInput(form, [{ sku: "mug-1", quantity: 1 }]);

    expect(input.shippingAddress.country).toBe("DE");
    expect(input.billingAddress).toEqual(input.shippingAddress);
  });

  it("takes a separate billing address", () => {
    const input = toOrderInput(
      {
        ...form,
        billingSameAsShipping: false,
        billingName: "Example Ltd",
        billingStreet: "Office Road 5",
        billingPostalCode: "10117",
        billingCity: "Berlin",
        billingCountry: "DE",
      },
      [],
    );

    expect(input.billingAddress.name).toBe("Example Ltd");
    expect(input.billingAddress.street).toBe("Office Road 5");
    expect(input.shippingAddress.street).toBe("Example Street 1");
  });

  it("names the invalid fields", () => {
    expect(() => toOrderInput({ ...form, shippingPostalCode: "1011" }, [])).toThrow(InvalidFormError);
  });
});
