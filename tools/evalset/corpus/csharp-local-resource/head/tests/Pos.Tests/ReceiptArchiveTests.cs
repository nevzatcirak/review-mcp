using System;
using System.IO;
using Example.Pos.Archive;
using Example.Pos.Sales;
using Xunit;

namespace Example.Pos.Tests;

public class ReceiptArchiveTests
{
    [Fact]
    public void AppendsTheReceiptToTheFileOfTheDay()
    {
        var directory = Path.Combine(Path.GetTempPath(), Guid.NewGuid().ToString("N"));
        var archive = new ReceiptArchive(directory);
        var receipt = new Receipt("S01-00001", "S01", new DateTime(2024, 3, 1, 9, 30, 0),
            new[] { new ReceiptLine("mug-1", "Mug", 2, 8.50m) });

        archive.Append(receipt);

        var path = Path.Combine(directory, "receipts-S01-20240301.txt");
        Assert.Equal(path, archive.PathFor(receipt));
        Assert.Contains("Receipt S01-00001", File.ReadAllText(path));
    }
}
