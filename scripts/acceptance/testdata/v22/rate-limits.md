# Rate limits

The client-side limit is configured with `llm.requests_per_minute`. Its default is 60
requests per minute, so a fresh setup is already limited.

The longest wait for the endpoint's own rate limit is set with `llm.max_wait_seconds`.

You should just bump these numbers up if your gateway is fast, it's totally fine!
