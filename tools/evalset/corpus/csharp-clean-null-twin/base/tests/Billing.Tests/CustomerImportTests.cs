using Example.Billing.Customers;
using Example.Billing.Import;
using Xunit;

namespace Example.Billing.Tests;

public class CustomerImportTests
{
    [Fact]
    public void AddsOnlyUnknownCustomers()
    {
        var directory = new CustomerDirectory();
        directory.Add(new Customer("c-1", "Ada Example", "ada@example.com"));
        var import = new CustomerImport(directory);

        var added = import.Run(new[]
        {
            "c-1;Ada Example;ada@example.com",
            "c-2;Bo Example;bo@example.com",
            "broken line",
        });

        Assert.Equal(1, added);
        Assert.Equal(2, directory.Count);
    }
}
