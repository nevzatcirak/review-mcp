using System;
using System.Collections.Generic;
using System.Linq;

namespace Example.Pos.Sales;

public sealed record Receipt(string Number, string StoreId, DateTime IssuedAt, IReadOnlyList<ReceiptLine> Lines)
{
    public decimal Total => Lines.Sum(l => l.Amount);
}
