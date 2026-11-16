using System;
using Example.Billing.Customers;
using Example.Billing.Invoicing;
using Xunit;

namespace Example.Billing.Tests;

public class InvoiceServiceTests
{
    [Fact]
    public void SendsTheInvoiceToTheBillingCustomer()
    {
        var directory = new CustomerDirectory();
        directory.Add(new Customer("c-9", "Accounts Team", "accounts@example.com"));
        var service = new InvoiceService(new BillingContactResolver(directory), 41);
        var account = new Account("a-1", "Ada Example", "ada@example.com", "accounts@example.com");

        var invoice = service.Issue(account, 120m, new DateOnly(2024, 3, 1));

        Assert.Equal("INV-000042", invoice.Number);
        Assert.Equal("accounts@example.com", invoice.SendTo.Email);
    }

    [Fact]
    public void FallsBackToTheOwner()
    {
        var service = new InvoiceService(new BillingContactResolver(new ThrowingDirectory()), 0);
        var account = new Account("a-2", "Bo Example", "bo@example.com", "old-billing@example.com");

        var invoice = service.Issue(account, 10m, new DateOnly(2024, 3, 1));

        Assert.Equal("bo@example.com", invoice.SendTo.Email);
    }

    private sealed class ThrowingDirectory : ICustomerDirectory
    {
        public int Count => 0;

        public Customer FindByEmail(string email) => throw new CustomerNotFoundException(email);

        public void Add(Customer customer)
        {
        }
    }
}
