import type { Address } from "./types.js";

const POSTAL_CODES: Record<string, RegExp> = {
  DE: /^\d{5}$/,
  NL: /^\d{4} ?[A-Z]{2}$/,
  FR: /^\d{5}$/,
};

/** Returns the names of the fields that are missing or malformed. */
export function invalidFields(address: Address): (keyof Address)[] {
  const invalid: (keyof Address)[] = [];
  for (const field of ["name", "street", "postalCode", "city", "country"] as const) {
    if (address[field].trim() === "") {
      invalid.push(field);
    }
  }
  const pattern = POSTAL_CODES[address.country];
  if (pattern && address.postalCode.trim() !== "" && !pattern.test(address.postalCode.trim())) {
    invalid.push("postalCode");
  }
  return invalid;
}
