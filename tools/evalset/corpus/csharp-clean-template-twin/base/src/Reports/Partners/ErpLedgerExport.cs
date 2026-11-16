using System.Collections.Generic;
using System.Globalization;
using System.IO;
using Example.Reports.Exports;
using Example.Reports.Sales;

namespace Example.Reports.Partners;

/// <summary>
/// The ledger file the bookkeeping partner imports. Their importer reads the
/// first line as the format marker and every following line as a booking:
/// date;account;sku;quantity;amount.
/// </summary>
public sealed class ErpLedgerExport : ExportJob
{
    private const string FormatMarker = "LEDGER;V2";
    private readonly string _account;

    public ErpLedgerExport(string account)
    {
        _account = account;
    }

    public override string Name => "erp-ledger";

    public override string FileExtension => ".txt";

    public override void Run(TextWriter writer, IReadOnlyList<SalesRow> rows)
    {
        writer.WriteLine(FormatMarker);
        base.Run(writer, rows);
    }

    protected override string FormatRow(SalesRow row) =>
        string.Join(";",
            row.Date.ToString("yyyyMMdd", CultureInfo.InvariantCulture),
            _account,
            row.Sku,
            row.Quantity.ToString(CultureInfo.InvariantCulture),
            row.Amount.ToString("0.00", CultureInfo.InvariantCulture));
}
