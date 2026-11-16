export function formatEuro(amount: number): string {
  return new Intl.NumberFormat("en-IE", { style: "currency", currency: "EUR" }).format(amount);
}

export function invoiceNumber(orderId: string, sequence: number): string {
  return `INV-${orderId.toUpperCase()}-${String(sequence).padStart(4, "0")}`;
}
