# Console order input supplementation

Update this file when `order_inputs.go` changes.

- POST `trade/orders/:order_id/input-uploads`, `input-uploads/confirm` and `supplement-inputs` use the existing Console session, CSRF and Commission access middleware. The authenticated buyer is the delegation subject; never accept a browser-supplied actor or Order ID.
- Bind each mutation's canonical path Order ID, body and idempotency key under `orders:inputs:write`, with operation `console.trade.orders.input_uploads.create`, `console.trade.orders.input_uploads.confirm` or `console.trade.orders.inputs.supplement`. Commission enforces buyer ownership, current version, pending question and object admission.
- Return only verified expiring HTTPS public OSS PUT grants. Require matching path, size and SHA-256; reject arbitrary hosts, credentials, fragments and unexpected headers. Verify Content-Length, then omit that browser-forbidden header; upload the original File/Blob with `credentials: omit` and `redirect: error`. Keep required OSS metadata and no-overwrite headers. Confirmed receipts skip PUT.
- Confirm only the requested decimal-string object ID. Supplement receipts must match the buyer, Order, next version, request and answer. Preserve int64 IDs as strings without floating-point conversion. Errors never return upstream diagnostics or delegation credentials; private responses use no-store.
- Preserve `INPUT_UPLOAD_NOT_FOUND` only from the confirm API's HTTP 404 and exact top-level `error_code`. Ordinary 404, validation failures and storage outages do not authorize another PUT.
- Deployment requires matching Commission APIs/migration, public OSS signing endpoint and bucket CORS allowing the Console origin, PUT and the returned upload headers. Verify a real browser upload, confirmation and resumed seller inspection; fixtures do not verify CORS.
