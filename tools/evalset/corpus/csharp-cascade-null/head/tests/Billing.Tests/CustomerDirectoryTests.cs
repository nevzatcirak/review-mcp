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
    public void ReturnsNullForAnUnknownEmail()
    {
        var directory = new CustomerDirectory();

        Assert.Null(directory.FindByEmail("nobody@example.com"));
    }
}
