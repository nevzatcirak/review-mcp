using System;
using System.Collections.Generic;

namespace Example.Billing.Customers;

public sealed class CustomerDirectory : ICustomerDirectory
{
    private readonly Dictionary<string, Customer> _byEmail = new(StringComparer.OrdinalIgnoreCase);

    public int Count => _byEmail.Count;

    public Customer FindByEmail(string email)
    {
        _byEmail.TryGetValue(email.Trim(), out var customer);
        return customer;
    }

    public void Add(Customer customer)
    {
        _byEmail[customer.Email.Trim()] = customer;
    }
}
