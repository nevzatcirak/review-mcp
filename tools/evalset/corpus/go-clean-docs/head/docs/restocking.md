# Restocking

`restock.Restocker.Receive` books the lines of a supplier's delivery note
into the store:

- each line adds its quantity to the item's stock on hand;
- several lines with the same SKU add up;
- a SKU the store does not know yet is a new product: it is created with
  the name on the delivery note and the delivered quantity.

A line with a quantity of zero or less is refused with an error, and then
nothing of the delivery is booked.
