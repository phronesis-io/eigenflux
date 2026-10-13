# Commission backend boundaries

Read before changing Commission gateway or projection behavior; update this file with affected logic.

- `access.go` reads the existing Agent allowlist. Disabled enforcement ignores membership; an enabled empty list denies all, and malformed configuration prevents startup.
- `../main.go` applies the gate after authentication to Commission discovery and Console trade, earnings, payout, KYC and withdrawal routes. Non-Commission routes retain existing authorization. Existing browser callback authentication is unchanged.
- `../../Caddyfile.dev` and `../../Caddyfile.prod` forward only the canonical positive-ID GET `/api/v2/console/trade/orders/{id}` to Commission. Lists, other methods and subpaths retain existing routes. Commission verifies the Agent and order participation.
- Public KYC callback paths route to Commission and skip access logging. Preserve other production routes when applying the reviewed configuration.
- `../../pkg/featureindex/commission_document.go` recognizes deletion events. `../../pipeline/consumer/commission_index_consumer.go` stores inactive source snapshots as versioned tombstones; delayed events cannot restore deleted services. Statistics remain independently versioned.
- The optional `fulfillment_skill` Thrift field is backward-compatible metadata. EigenFlux search does not use it to run Agents; local dispatch reads the frozen order contract.

The maintenance endpoint is documented in [maintenance.md](../consolev2/maintenance.md).
This backend change introduces no database migration or CLI/Skills publication. Existing capability descriptions remain aligned with the released CLI.

Run the API allowlist, discovery and projection tests plus `python3 scripts/cloud/test_commission_routes.py`. Use local PostgreSQL for maintenance authorization/idempotency tests. Deployment must use reviewed `main`, and Caddy needs a separate validated reload; the shared proxy may close active WebSockets during reload.
