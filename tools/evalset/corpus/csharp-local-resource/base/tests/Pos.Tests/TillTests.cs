using System;
using System.Collections.Generic;
using System.Linq;
using Example.Pos.Printing;
using Example.Pos.Sales;
using Xunit;

namespace Example.Pos.Tests;

public class TillTests
{
    private sealed class MemoryPrinter : IPrinter
    {
        public List<string> Printed { get; } = new();

        public void Print(IEnumerable<string> lines) => Printed.AddRange(lines);
    }

    [Fact]
    public void CheckoutPrintsTheReceiptAndStartsANewOne()
    {
        var printer = new MemoryPrinter();
        var till = new Till("S01", printer, () => new DateTime(2024, 3, 1, 9, 30, 0));
        till.Scan("mug-1", "Mug", 8.50m, 2);

        var receipt = till.Checkout();

        Assert.Equal("S01-00001", receipt.Number);
        Assert.Equal(17.00m, receipt.Total);
        Assert.Contains(printer.Printed, l => l.StartsWith("TOTAL"));
        Assert.Throws<InvalidOperationException>(() => till.Checkout());
    }
}
