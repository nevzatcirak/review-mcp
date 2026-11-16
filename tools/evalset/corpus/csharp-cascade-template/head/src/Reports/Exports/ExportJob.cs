using System.Collections.Generic;
using System.IO;
using Example.Reports.Sales;

namespace Example.Reports.Exports;

/// <summary>
/// An export of one day's sales rows: a header row with the column names,
/// then one line per row. Subclasses decide the file name and how a row is
/// written.
/// </summary>
public abstract class ExportJob
{
    public abstract string Name { get; }

    public abstract string FileExtension { get; }

    /// <summary>The column names of the header row, in the order FormatRow writes the values.</summary>
    protected virtual IReadOnlyList<string> Columns { get; } = new[] { "date", "sku", "quantity", "amount" };

    protected virtual string Separator => ",";

    public virtual void Run(TextWriter writer, IReadOnlyList<SalesRow> rows)
    {
        WriteHeader(writer);
        foreach (var row in rows)
        {
            writer.WriteLine(FormatRow(row));
        }
    }

    protected virtual void WriteHeader(TextWriter writer)
    {
        writer.WriteLine(string.Join(Separator, Columns));
    }

    protected abstract string FormatRow(SalesRow row);
}
