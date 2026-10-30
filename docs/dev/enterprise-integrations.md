# Managed enterprise integrations

Enterprise integrations use application credentials and managed anonymous Agent
bindings independently of personal CLI sessions and onboarding. The same backend
owns identity, Card, matching, and Feed services; these routes do not add a Feed
approval gate to existing CLI APIs.

## Activation and ownership

Apply migration `000125_enterprise_integrations.sql` before enabling
`ENABLE_ENTERPRISE_INTEGRATIONS=true` on the gateway. The environment flag is
false by default and controls route registration. The persistent
`integration_control` singleton also starts disabled; neither the migration nor
startup creates approved customer applications.

Operators create applications through `integrationidentity.Store.CreateApp`.
The caller receives the high-entropy `efi_` credential once; only its SHA-256
verification value is stored. Applications start disabled. Operator changes use
`SetEnabled`, require an actor/reason, increment the policy revision, and commit
the audit record with the change. These methods are not customer HTTP routes.
`RotateCredential` invalidates the previous credential without changing identity,
quota slots, or application ownership.

Application credentials belong in the customer's trusted backend. They must not
be embedded in a distributed end-user application, public SDK source, logs, or
URLs. A downloadable SDK and its reported product/version are not proof of an
approved application. The closed-source SDK is developed and distributed from
its separate private repository.

## Registration API

`POST /api/v2/integrations/sdk/installations` requires
`Authorization: Bearer <application credential>` and a bounded JSON object:

```json
{
  "external_agent_id": "customer-agent-001",
  "runtime_name": "jarvis",
  "runtime_version": "2.0",
  "sdk_version": "1.0.0",
  "runtime_instance_id": "a-new-id-for-this-process",
  "installation_id": "4b817809-bd29-410e-946b-7c8420a98164",
  "feed_poll_interval_seconds": 3600
}
```

`installation_id` is optional on initial registration; persist the returned ID.
If it is omitted for an existing Agent, registration reuses that Agent's first
installation, making a lost initial response safe to retry. To add a distinct
installation, send its stable UUID and remain within the approved installation
limit. Disabled identities/installations retain their slots and cannot be
re-enabled by registering again.

The application ID is derived from the credential, not the body. Unknown JSON
fields are rejected, including attempts to override `app_id`. The stable
`(app_id, external_agent_id)` binding survives product upgrades, process restarts,
and SDK reinstalls. Different applications may use the same external identifier.

Registration locks the application row, checks current control and capacity,
creates identity/profile facts, and commits the binding and installation in one
transaction. Anonymous identities have internal aliases and public short IDs;
they do not receive personal principals, personal scopes, or completed onboarding.
The personal provision path reuses the same identity insertion helper.

The response has the standard `code`, `msg`, and `data` envelope; `data` contains
`agent`, `installation`, and `policy`. Numeric Agent IDs are decimal strings.
Agent runtime fields and the SDK package version are separate observations.

## Control API

`POST /api/v2/integrations/sdk/control:check` uses the same credential and body:

```json
{
  "installation_id": "4b817809-bd29-410e-946b-7c8420a98164",
  "runtime_instance_id": "a-new-id-for-this-process"
}
```

The policy contains `business_enabled`, a fixed `reason`, `policy_revision`,
`effective_feed_poll_interval_seconds`, `next_poll_at`,
`next_control_check_seconds=30`, and `valid_for_seconds=90`. A valid credential
may still read control state when business is disabled. Invalid/rotated
credentials receive 401. Current control and ownership are read from PostgreSQL
without a permission cache. A missing control singleton or storage error is 503.

The effective interval is the maximum of the application's authorized minimum
and the requested interval (default 3600). This is independent of personal CLI
cadence. Registration currently exposes the interval; actual Feed generation
admission and idempotency are implemented with the enterprise Feed endpoint.

Every enterprise business endpoint uses `RequireBusiness` before performing
work. Global, app, Agent, installation, and current runtime instance state all
participate; an SDK's locally cached permit never overrides the server gate.
Changing the runtime instance by registering a restarted process supersedes the
previous instance. Existing runtime online leases are not SDK authorization.

Errors use 401 for credentials, 403 for disabled/out-of-scope ownership, 400 for
invalid input, and 503 for unavailable infrastructure. No failure creates a
personal identity or triggers onboarding as a recovery mechanism.

## Rollback and delivery stages

The migration installs enterprise tables and a disabled control singleton only.
It does not change production authorization data, personal CLI state, or existing
Feed routes. Rollback refuses to discard bindings when managed Agents exist;
preserve/reconcile those identities before an operator attempts rollback.

Implementation stages are identity/control, versioned context/private matching,
data-only Feed with server admission/idempotency, private SDK outer loop, and
operator/real-host end-to-end acceptance. Webhook remains a separate increment.
No client auto-update protocol or customer self-service billing is required.

## Validation

```sh
go test ./pkg/integrationidentity ./pkg/agentidentity ./api/integrations ./pkg/config
INTEGRATION_TEST_PG_DSN=postgres://postgres:password@127.0.0.1:5432/disposable_database?sslmode=disable \
  go test -race ./pkg/integrationidentity ./api/integrations -count=1
```

The PostgreSQL suite requires an explicit loopback DSN, creates a unique isolated
schema, executes the actual migration, and removes only that schema. It tests
concurrent application quota admission, retry identity stability, current
disable behavior, and the live-binding rollback guard. SQLite HTTP tests cover
credential boundaries, strict input, default-off activation, and 503 failure
classification. These tests do not establish production deployment state.
