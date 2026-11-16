using System;

namespace Example.Reports.Sales;

public sealed record SalesRow(DateOnly Date, string Sku, int Quantity, decimal Amount);
