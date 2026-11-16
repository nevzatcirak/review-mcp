using System.Collections.Generic;
using System.IO;
using Example.Reports.Sales;

namespace Example.Reports.Exports;

/// <summary>
/// An export of one day's sales rows. Subclasses decide the file name and
/// how a row is written.
/// </summary>
public abstract class ExportJob
{
    public abstract string Name { get; }

    public abstract string FileExtension { get; }

    public virtual void Run(TextWriter writer, IReadOnlyList<SalesRow> rows)
    {
        foreach (var row in rows)
        {
            writer.WriteLine(FormatRow(row));
        }
    }

    protected abstract string FormatRow(SalesRow row);
}
