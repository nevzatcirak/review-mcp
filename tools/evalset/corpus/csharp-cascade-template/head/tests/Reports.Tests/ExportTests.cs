using System;
using System.IO;
using Example.Reports.Exports;
using Example.Reports.Partners;
using Example.Reports.Sales;
using Xunit;

namespace Example.Reports.Tests;

public class ExportTests
{
    private static readonly SalesRow[] Rows =
    {
        new(new DateOnly(2024, 3, 1), "SKU-1", 2, 19.80m),
        new(new DateOnly(2024, 3, 1), "SKU-2", 1, 5m),
    };

    [Fact]
    public void CsvWritesOneLinePerRow()
    {
        var writer = new StringWriter();

        new CsvSalesExport().Run(writer, Rows);

        Assert.Contains("2024-03-01,SKU-1,2,19.80", writer.ToString());
        Assert.Contains("2024-03-01,SKU-2,1,5.00", writer.ToString());
    }

    [Fact]
    public void CsvAndTsvStartWithTheHeaderRow()
    {
        var csv = new StringWriter();
        var tsv = new StringWriter();

        new CsvSalesExport().Run(csv, Rows);
        new TsvSalesExport().Run(tsv, Rows);

        Assert.StartsWith("date,sku,quantity,amount" + Environment.NewLine, csv.ToString());
        Assert.StartsWith("date\tsku\tquantity\tamount" + Environment.NewLine, tsv.ToString());
    }

    [Fact]
    public void LedgerStartsWithTheFormatMarker()
    {
        var writer = new StringWriter();

        new ErpLedgerExport("4400").Run(writer, Rows);

        Assert.StartsWith("LEDGER;V2", writer.ToString());
        Assert.Contains("20240301;4400;SKU-1;2;19.80", writer.ToString());
    }
}
