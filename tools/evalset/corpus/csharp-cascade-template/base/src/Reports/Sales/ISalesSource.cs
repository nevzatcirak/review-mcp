using System;
using System.Collections.Generic;

namespace Example.Reports.Sales;

public interface ISalesSource
{
    IReadOnlyList<SalesRow> RowsFor(DateOnly day);
}
