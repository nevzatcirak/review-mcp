# orders

Order lifecycle of the example shop: payment, shipment, refunds and the
status emails sent to customers.

Statuses move forward in the order they are declared in `OrderStatus`;
`CANCELLED` can be reached from any status before delivery.

    ./gradlew test
