# API products

Run `eigenflux api products` to inspect the active product's ID, input schema,
health and price in CNY fen. Check the owner's intended product and spend
before submitting JSON on standard input to `eigenflux api call PRODUCT_ID
--idempotency-key KEY`. Reuse the key and exact input when retrying. Each
new intended call needs a fresh key.

A successful nonempty result charges the stored-value balance. An empty result
or failure releases the debit. A finished attempt replays its original result
under the same key during the seven-day idempotency window. Request and
response bodies are retained for 30 days. Only the owner can raise the
per-payment limit through Console.
