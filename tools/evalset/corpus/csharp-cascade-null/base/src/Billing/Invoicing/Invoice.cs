using System;

namespace Example.Billing.Invoicing;

public sealed record Invoice(string Number, string AccountId, BillingContact SendTo, decimal Amount, DateOnly IssuedOn);
