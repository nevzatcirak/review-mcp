using System;
using System.Collections.Generic;
using Example.Billing.Customers;

namespace Example.Billing.Import;

/// <summary>Adds the customers of a sign-up export that the directory does not know yet.</summary>
public sealed class CustomerImport
{
    private readonly ICustomerDirectory _directory;

    public CustomerImport(ICustomerDirectory directory)
    {
        _directory = directory;
    }

    /// <summary>Imports lines of the form <c>id;name;email</c> and returns how many customers were added.</summary>
    public int Run(IEnumerable<string> lines)
    {
        var added = 0;
        foreach (var line in lines)
        {
            var parts = line.Split(';');
            if (parts.Length != 3)
            {
                continue;
            }

            var email = parts[2].Trim();
            try
            {
                _directory.FindByEmail(email);
            }
            catch (CustomerNotFoundException)
            {
                _directory.Add(new Customer(parts[0].Trim(), parts[1].Trim(), email));
                added++;
            }
        }

        return added;
    }
}
