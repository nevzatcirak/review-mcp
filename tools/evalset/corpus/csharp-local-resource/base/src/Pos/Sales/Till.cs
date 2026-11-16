using System;
using System.Collections.Generic;
using Example.Pos.Printing;

namespace Example.Pos.Sales;

public sealed class Till
{
    private readonly string _storeId;
    private readonly IPrinter _printer;
    private readonly ReceiptFormatter _formatter = new();
    private readonly Func<DateTime> _now;
    private readonly List<ReceiptLine> _lines = new();
    private int _sequence;

    public Till(string storeId, IPrinter printer, Func<DateTime> now)
    {
        _storeId = storeId;
        _printer = printer;
        _now = now;
    }

    public void Scan(string sku, string name, decimal unitPrice, int quantity = 1)
    {
        if (quantity <= 0)
        {
            throw new ArgumentOutOfRangeException(nameof(quantity));
        }

        _lines.Add(new ReceiptLine(sku, name, quantity, unitPrice));
    }

    public Receipt Checkout()
    {
        if (_lines.Count == 0)
        {
            throw new InvalidOperationException("nothing scanned");
        }

        _sequence++;
        var receipt = new Receipt($"{_storeId}-{_sequence:D5}", _storeId, _now(), _lines.ToArray());
        _lines.Clear();
        _printer.Print(_formatter.Format(receipt));
        return receipt;
    }
}
