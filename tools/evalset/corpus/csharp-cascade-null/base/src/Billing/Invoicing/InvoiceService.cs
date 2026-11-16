using System;
using System.Threading;

namespace Example.Billing.Invoicing;

public sealed class InvoiceService
{
    private readonly BillingContactResolver _contacts;
    private int _sequence;

    public InvoiceService(BillingContactResolver contacts, int lastNumber)
    {
        _contacts = contacts;
        _sequence = lastNumber;
    }

    public Invoice Issue(Account account, decimal amount, DateOnly day)
    {
        if (amount <= 0)
        {
            throw new ArgumentOutOfRangeException(nameof(amount), "an invoice needs a positive amount");
        }

        var number = Interlocked.Increment(ref _sequence);
        var sendTo = _contacts.Resolve(account);
        return new Invoice($"INV-{number:D6}", account.Id, sendTo, amount, day);
    }
}
