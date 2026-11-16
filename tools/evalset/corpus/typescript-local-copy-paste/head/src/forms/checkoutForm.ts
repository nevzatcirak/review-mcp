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
  /** Unchecked when the customer enters a separate billing address. */
  billingSameAsShipping: boolean;
  billingName?: string;
  billingStreet?: string;
  billingPostalCode?: string;
  billingCity?: string;
  billingCountry?: string;
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

export function billingAddress(form: CheckoutForm): Address {
  if (form.billingSameAsShipping) {
    return shippingAddress(form);
  }
  return {
    name: (form.billingName ?? "").trim(),
    street: (form.billingStreet ?? "").trim(),
    postalCode: (form.billingPostalCode ?? "").trim(),
    city: (form.shippingCity ?? "").trim(),
    country: (form.billingCountry ?? "").trim().toUpperCase(),
  };
}

export function toOrderInput(form: CheckoutForm, items: OrderItemInput[]): OrderInput {
  const shipping = shippingAddress(form);
  const billing = billingAddress(form);
  const invalid = [
    ...invalidFields(shipping).map((field) => `shipping.${field}`),
    ...(form.billingSameAsShipping ? [] : invalidFields(billing).map((field) => `billing.${field}`)),
  ];
  if (!form.email.includes("@")) {
    invalid.push("email");
  }
  if (invalid.length > 0) {
    throw new InvalidFormError(invalid);
  }
  return { email: form.email.trim(), items, shippingAddress: shipping, billingAddress: billing };
}
