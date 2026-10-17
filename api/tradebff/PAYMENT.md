# Console pending-order payment

Update this file when this payment delegation changes.

TradeOrderPayment accepts only page/wap plus the canonical path Order ID. It
requires the Console session, CSRF and idempotency key, and delegates the signed
resource and subject to Commission. The browser cannot choose amount, buyer,
return URL or arbitrary provider payload. Commission verifies ownership, payable
state and deadline, then returns an expiring Alipay redirect. This is an on-demand
checkout link, not a qr_code action. Alipay's cashier renders the payment code.

The Console can retain the redirect for explicit navigation, but must reject
stale identity/order results and expired links. Only authoritative Order state
confirms payment. No new API or schema migration is needed to expose the existing
payment entry. Payment tests cover canonical IDs, channels and untrusted fields.
