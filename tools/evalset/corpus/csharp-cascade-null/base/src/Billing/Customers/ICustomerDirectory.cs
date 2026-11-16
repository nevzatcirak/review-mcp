namespace Example.Billing.Customers;

public interface ICustomerDirectory
{
    /// <summary>Finds a customer by email address, ignoring case.</summary>
    /// <exception cref="CustomerNotFoundException">No customer has this email.</exception>
    Customer FindByEmail(string email);

    void Add(Customer customer);

    int Count { get; }
}
