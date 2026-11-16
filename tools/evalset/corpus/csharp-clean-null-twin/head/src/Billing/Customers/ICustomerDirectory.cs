namespace Example.Billing.Customers;

public interface ICustomerDirectory
{
    /// <summary>Finds a customer by email address, ignoring case.</summary>
    /// <returns>The customer, or null when no customer has this email.</returns>
    Customer FindByEmail(string email);

    void Add(Customer customer);

    int Count { get; }
}
