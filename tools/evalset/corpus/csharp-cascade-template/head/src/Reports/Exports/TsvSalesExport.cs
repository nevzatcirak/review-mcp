using System.Globalization;
using Example.Reports.Sales;

namespace Example.Reports.Exports;

public sealed class TsvSalesExport : ExportJob
{
    public override string Name => "sales-tsv";

    public override string FileExtension => ".tsv";

    protected override string Separator => "\t";

    protected override string FormatRow(SalesRow row) =>
        string.Join(Separator,
            row.Date.ToString("yyyy-MM-dd", CultureInfo.InvariantCulture),
            row.Sku,
            row.Quantity.ToString(CultureInfo.InvariantCulture),
            row.Amount.ToString("0.00", CultureInfo.InvariantCulture));
}
