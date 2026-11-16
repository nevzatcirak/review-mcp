namespace Example.Pos.Sales;

public sealed record ReceiptLine(string Sku, string Name, int Quantity, decimal UnitPrice)
{
    public decimal Amount => UnitPrice * Quantity;
}
