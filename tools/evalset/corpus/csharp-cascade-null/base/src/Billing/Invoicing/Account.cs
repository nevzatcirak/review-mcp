namespace Example.Billing.Invoicing;

/// <summary>A billing account. The billing email may belong to a customer other than the owner.</summary>
public sealed record Account(string Id, string OwnerName, string OwnerEmail, string BillingEmail);
