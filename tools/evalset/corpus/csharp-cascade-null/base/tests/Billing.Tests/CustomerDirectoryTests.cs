using Example.Billing.Customers;
using Xunit;

namespace Example.Billing.Tests;

public class CustomerDirectoryTests
{
    [Fact]
    public void FindsByEmailIgnoringCase()
    {
        var directory = new CustomerDirectory();
        directory.Add(new Customer("c-1", "Ada Example", "ada@example.com"));

        Assert.Equal("c-1", directory.FindByEmail("ADA@example.com").Id);
    }

    [Fact]
    public void ThrowsForAnUnknownEmail()
    {
        var directory = new CustomerDirectory();

        Assert.Throws<CustomerNotFoundException>(() => directory.FindByEmail("nobody@example.com"));
    }
}
