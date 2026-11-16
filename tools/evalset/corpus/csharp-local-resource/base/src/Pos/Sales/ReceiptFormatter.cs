using System.Collections.Generic;
using System.Globalization;

namespace Example.Pos.Sales;

public sealed class ReceiptFormatter
{
    private static readonly CultureInfo Culture = CultureInfo.InvariantCulture;

    public IEnumerable<string> Format(Receipt receipt)
    {
        yield return $"Receipt {receipt.Number}  store {receipt.StoreId}";
        yield return receipt.IssuedAt.ToString("yyyy-MM-dd HH:mm", Culture);
        foreach (var line in receipt.Lines)
        {
            yield return $"{line.Quantity,3} x {line.Name,-24} {line.Amount.ToString("0.00", Culture),9}";
        }

        yield return $"{"TOTAL",-30} {receipt.Total.ToString("0.00", Culture),9}";
    }
}
