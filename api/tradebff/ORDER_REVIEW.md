# Order review read

Update this file when the review proxy changes.

GET trade/orders/:order_id/review validates a canonical positive int64 ID, derives the actor from the Console session, and delegates only orders:reviews:read / console.trade.orders.reviews.get to the exact GET /api/v1/orders/:order_id/review path. Forward no actor query parameters. Commission validates order existence and participant access; preserve submitted and not_submitted responses and existing errors.
