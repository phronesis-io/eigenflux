# Commission Agent Allowlist Design

## Problem

Commission capabilities are authenticated but currently available to every authenticated Agent whenever their feature dependencies are enabled. Rollout requires an API-layer allowlist so that authenticated Agents outside the configured set cannot invoke discovery, trading, earnings, payout, or withdrawal operations.

The authorization boundary must run after authentication, because the Agent ID must come from trusted server context rather than request input. It must run before any Sort RPC or Commission upstream request.

## Decision

Add a route-level Commission access middleware in the EigenFlux API Gateway. Parse a static Agent ID allowlist once during API startup and attach the gate only to Commission-backed routes.

This is preferred over checks inside each handler because one shared gate avoids duplicated policy and makes route coverage visible at registration. It is preferred over global path matching because Commission capabilities use several unrelated URL prefixes and two response contracts.

## Configuration

Add the environment variable `COMMISSION_AGENT_ID_WHITELIST`.

- Format: comma-separated positive decimal signed-64-bit Agent IDs.
- Surrounding whitespace around each entry is ignored.
- Duplicate IDs collapse to one entry.
- An unset or empty value produces an empty allowlist and denies every Commission request.
- Empty entries created by repeated, leading, or trailing commas are ignored.
- Any other malformed, zero, negative, or overflowing entry is an invalid startup configuration. The API must fail before registering or serving routes.

Example:

```env
COMMISSION_AGENT_ID_WHITELIST=9223372036854775001,9223372036854775002
```

The allowlist is immutable after startup. Requests do not reread environment variables or query a database.

## API Gate

A Commission-specific API component owns:

- the immutable membership set;
- extraction of `agent_id` from the trusted Hertz request context;
- a V1 middleware response adapter;
- a Console V2 BFF middleware response adapter.

The two adapters share the same membership decision but preserve their route families' existing response formats.

Middleware ordering is:

1. Existing authentication/session validation.
2. Commission Agent allowlist gate.
3. Existing Commission handler.

Consequences:

- Missing or invalid authentication remains `401` from the existing authentication layer.
- A valid authenticated Agent outside the allowlist receives `403`.
- A valid authenticated Agent in the allowlist reaches the unchanged handler.
- A rejected request cannot generate an impression ID, invoke Sort RPC, mint a Commission delegation token, or call the Commission service.

### V1 rejection

HTTP status: `403 Forbidden`

```json
{
  "code": 403,
  "msg": "commission access is not allowed"
}
```

### Console V2 BFF rejection

HTTP status: `403 Forbidden`

```json
{
  "error": {
    "code": "COMMISSION_ACCESS_FORBIDDEN",
    "message": "Commission access is not enabled for this Agent"
  }
}
```

The Console response retains `Cache-Control: private, no-store` through the existing BFF chain.

## Protected Routes

### Discovery facade

- `GET /api/v1/commissions/search`
- `GET /api/v1/commissions/recommendations`

### Console V2 Commission BFF

- `GET /api/v2/console/bff/trade/overview`
- `GET /api/v2/console/bff/trade/commissions`
- `GET /api/v2/console/bff/trade/orders`
- `GET /api/v2/console/bff/trade/orders/:order_id`
- `GET /api/v2/console/bff/earnings/summary`
- `GET /api/v2/console/bff/earnings/records`
- `GET /api/v2/console/bff/payout-method`
- `POST /api/v2/console/bff/payout-method/authorization`
- `POST /api/v2/console/bff/withdrawals`
- `GET /api/v2/console/bff/withdrawals/:withdrawal_id`

Non-Commission BFF routes remain unchanged. The private Commission integration diagnostics server remains protected by its existing integration token and is outside this Agent allowlist.

## Error Handling

Configuration errors are fatal startup errors and identify the invalid variable without logging the complete allowlist. Runtime denials are normal authorization outcomes and do not depend on Sort, Redis, Elasticsearch, or the Commission service.

The middleware fails closed if `agent_id` is absent or has an unexpected type. In normal route chains, the authentication middleware handles this first; the fallback prevents accidental bypass if registration ordering changes.

## Testing

Tests must be written before production code and observed failing for the missing behavior.

1. Configuration parsing:
   - unset and empty values produce an empty set;
   - whitespace and duplicates normalize correctly;
   - positive `int64` boundary values are accepted;
   - malformed, zero, negative, and overflowing values fail validation.
2. V1 middleware:
   - an allowlisted Agent reaches the downstream handler;
   - an unlisted Agent receives the V1 `403` envelope;
   - denial prevents downstream execution;
   - an empty allowlist denies all Agents.
3. Console middleware:
   - an allowlisted Agent reaches the downstream handler;
   - an unlisted Agent receives the Console `403` envelope and no-store header;
   - denial prevents downstream execution.
4. Route registration:
   - both discovery routes run authentication before the allowlist gate;
   - only the ten Commission-backed Console BFF routes receive the gate;
   - unrelated Console BFF routes remain accessible under their existing authorization rules.
5. Verification:
   - run focused unit tests for configuration, discovery, Console V2 route registration, and trade BFF;
   - run the core build script;
   - start the local services and exercise one allowlisted and one denied request for each response family when dependencies are available.

## Documentation

Update `.env.example`, `docs/dev/configuration.md`, and `docs/dev/api_endpoints.md` with the variable, fail-closed empty behavior, protected route families, and `403` contract.

No RPC IDL, generated code, database migration, Sort behavior, Commission service behavior, or CLI command changes are required.
