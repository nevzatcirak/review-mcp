import type { Address } from "../address/types.js";
import { invalidFields } from "../address/validate.js";
import type { OrderInput, OrderItemInput } from "../orders/orderInput.js";

/** The raw fields of the checkout form, as the browser posts them. */
export interface CheckoutForm {
  email: string;
  shippingName: string;
  shippingStreet: string;
  shippingPostalCode: string;
  shippingCity: string;
  shippingCountry: string;
}

export class InvalidFormError extends Error {
  constructor(readonly fields: string[]) {
    super(`invalid fields: ${fields.join(", ")}`);
  }
}

export function shippingAddress(form: CheckoutForm): Address {
  return {
    name: form.shippingName.trim(),
    street: form.shippingStreet.trim(),
    postalCode: form.shippingPostalCode.trim(),
    city: form.shippingCity.trim(),
    country: form.shippingCountry.trim().toUpperCase(),
  };
}

export function toOrderInput(form: CheckoutForm, items: OrderItemInput[]): OrderInput {
  const shipping = shippingAddress(form);
  const invalid = invalidFields(shipping).map((field) => `shipping.${field}`);
  if (!form.email.includes("@")) {
    invalid.push("email");
  }
  if (invalid.length > 0) {
    throw new InvalidFormError(invalid);
  }
  return { email: form.email.trim(), items, shippingAddress: shipping, billingAddress: shipping };
}
