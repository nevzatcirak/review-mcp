using System;
using System.Collections.Generic;
using System.IO;
using System.Text;
using Example.Reports.Sales;

namespace Example.Reports.Exports;

public sealed class ExportRunner
{
    private readonly ISalesSource _sales;
    private readonly IReadOnlyList<ExportJob> _jobs;
    private readonly string _outbox;

    public ExportRunner(ISalesSource sales, IReadOnlyList<ExportJob> jobs, string outbox)
    {
        _sales = sales;
        _jobs = jobs;
        _outbox = outbox;
    }

    /// <summary>Writes one file per job for the day and returns the paths written.</summary>
    public IReadOnlyList<string> RunAll(DateOnly day)
    {
        var rows = _sales.RowsFor(day);
        var written = new List<string>();
        foreach (var job in _jobs)
        {
            var path = Path.Combine(_outbox, $"{job.Name}-{day:yyyyMMdd}{job.FileExtension}");
            using (var writer = new StreamWriter(path, append: false, new UTF8Encoding(false)))
            {
                job.Run(writer, rows);
            }

            written.Add(path);
        }

        return written;
    }
}
