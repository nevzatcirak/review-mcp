# account-api

The customer account API of the example shop. Every route runs after the
session middleware, so `req.user` is the signed-in customer.

    npm test
