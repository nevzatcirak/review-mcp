using System.IO;
using System.Text;
using Example.Pos.Sales;

namespace Example.Pos.Archive;

/// <summary>
/// Keeps a copy of every receipt in one text file per day, which the
/// back office collects at closing time.
/// </summary>
public sealed class ReceiptArchive
{
    private const string Separator = "----";
    private readonly string _directory;
    private readonly ReceiptFormatter _formatter = new();

    public ReceiptArchive(string directory)
    {
        _directory = directory;
    }

    public string PathFor(Receipt receipt) =>
        Path.Combine(_directory, $"receipts-{receipt.StoreId}-{receipt.IssuedAt:yyyyMMdd}.txt");

    public void Append(Receipt receipt)
    {
        Directory.CreateDirectory(_directory);
        var writer = new StreamWriter(PathFor(receipt), append: true, new UTF8Encoding(false));
        foreach (var line in _formatter.Format(receipt))
        {
            writer.WriteLine(line);
        }

        writer.WriteLine(Separator);
        writer.Flush();
    }
}
