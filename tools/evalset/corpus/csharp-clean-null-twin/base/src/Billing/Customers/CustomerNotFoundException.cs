using System;

namespace Example.Billing.Customers;

public sealed class CustomerNotFoundException : Exception
{
    public CustomerNotFoundException(string email)
        : base($"no customer with email {email}")
    {
        Email = email;
    }

    public string Email { get; }
}
