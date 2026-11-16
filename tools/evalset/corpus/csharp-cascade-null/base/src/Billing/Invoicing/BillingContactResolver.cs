using Example.Billing.Customers;

namespace Example.Billing.Invoicing;

/// <summary>
/// Decides who receives an account's invoices: the customer behind the
/// billing email when there is one, otherwise the account owner.
/// </summary>
public sealed class BillingContactResolver
{
    private readonly ICustomerDirectory _directory;

    public BillingContactResolver(ICustomerDirectory directory)
    {
        _directory = directory;
    }

    public BillingContact Resolve(Account account)
    {
        try
        {
            var customer = _directory.FindByEmail(account.BillingEmail);
            return new BillingContact(customer.Name, customer.Email);
        }
        catch (CustomerNotFoundException)
        {
            return new BillingContact(account.OwnerName, account.OwnerEmail);
        }
    }
}
