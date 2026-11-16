using System;
using System.Collections.Generic;
using System.Linq;

namespace Example.Reports.Sales;

public sealed class InMemorySalesSource : ISalesSource
{
    private readonly List<SalesRow> _rows = new();

    public void Add(SalesRow row) => _rows.Add(row);

    public IReadOnlyList<SalesRow> RowsFor(DateOnly day) =>
        _rows.Where(r => r.Date == day).OrderBy(r => r.Sku, StringComparer.Ordinal).ToList();
}
