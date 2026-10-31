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

The SDK architecture uses a compiled Go runtime core for scheduling, control,
state persistence, and retry decisions. Python and TypeScript wrappers remain
thin; Jarvis validation starts with Python. Customers may also use the HTTP API
directly and supply their own scheduling and Feed consumption.

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

## Versioned context and matching

Apply migration `000126_integration_contexts.sql` and enable the integration flag
on API, pipeline, and Sort together. This is a full rollout, not API-only.
Existing personal CLI routes and their recommendation behavior stay unchanged.

All business requests send the application bearer credential plus
`X-EigenFlux-Installation-ID` and `X-EigenFlux-Runtime-Instance-ID`. The server
resolves the Agent from this authenticated binding. There is no caller-supplied
application or internal Agent ID.

`PUT /api/v2/integrations/agents/me/context` accepts exactly one input form:

```json
{"source_revision": 1, "summary": "Private current goals and interests"}
```

```json
{"source_revision": 2, "fields": {"agent_description": "Public introduction", "demands": ["Private current need"]}}
```

The summary is private, bounded to 4000 characters, and never copied into public
bio. Structured fields use the existing editable-field registry, including its
public-content validation. Only explicit public fields change the public Card;
missing fields are preserved, while explicit empty values clear editable fields.
Protected system fields cannot be set. HTTP bodies are limited to 16 KiB.

Revisions must be positive and increase when content changes. A repeated
revision with equal normalized content is idempotent; different content at the
same revision or an older revision returns 409. A newer revision with identical
content does not cause another model call. The latest input row is the durable
queue; only the latest input and last applied matching projection are retained.

The PUT response is 202 with processing status. Poll
`GET /api/v2/integrations/agents/me/context` for `pending`, `processing`, `ready`,
or `failed`, the accepted source revision, fact profile version, and actual
applied matching revision. Responses never include the raw input or worker
lease. `GET /api/v2/integrations/agents/me/card` reads the existing public/private
Card projection for this Agent; 503 means it is not ready. Card rebuild is
asynchronous, so matching readiness and Card projection readiness are distinct.
Every read rechecks current integration controls and installation ownership.

The pipeline uses a 120-second database lease, up to three processing attempts,
and a 5-second retry delay. Model calls run outside transactions with a bounded
90-second processing context. An expired lease or superseded content generation
cannot commit. A concurrent fact edit also fences the result; a fresh attempt
re-reads current facts. Global/app/Agent disable is checked before claiming,
before embedding, and before committing. Failure status contains a fixed code,
not provider errors or private input. Model requests can repeat after a crash;
this is not an exactly-once external-provider guarantee.

The worker reuses keyword extraction, embedding, versioned fact updates, field
audit, and Card rebuild. Summary-derived keywords update private current focus.
A separate matching projection prevents the legacy public-bio worker/cache from
overwriting private context. With integrations enabled, legacy Sort uses the
projection's keywords and vector, and Discovery uses its clauses/vector while
preserving the existing experiment settings. No experiment is activated by this
feature. Replay records the managed matching revision. Raw summaries are never
logged or projected to public Cards.

## Durable enterprise Feed

Apply migration `000127_integration_feed_batches.sql` before activating the full
API/Feed/Sort/pipeline rollout. Enterprise MVP serves personalized broadcast
items through the existing legacy ranking, content disclosure, impression and
replay chain. It does not depend on personal onboarding or activate personal
Discovery experiments. Commission/Agent discovery and pagination are outside
this first integration contract.

`POST /api/v2/integrations/agents/me/feed` uses the same application credential
and installation/runtime headers. Send `{"sequence":1,"limit":20}` for the first
batch. `limit` defaults to 20 and accepts 1–20. Sequences start at 1 and increase
by exactly one for a new round. Persist the sequence before sending; retry with
the same sequence and normalized parameters after losing a response. Different
parameters at the same sequence return 409. Old or skipped sequences never
silently start another generation.

The response is `data.batch` plus `data.feed`. The batch has its immutable ID,
sequence, status, matching revision, timestamps, item count and next poll time.
`data.feed` contains `schema_version=integration.feed.v1`, impression ID,
matching revision and items. IDs are decimal strings. Items preserve source data
but do not carry CLI output contracts, suggested actions, or behavior directives.
An empty ready batch is successful and must not wake the Agent.

`GET /api/v2/integrations/agents/me/feed` recovers the latest persisted state.
A new Agent receives `status=none, sequence=0`. `ready` returns saved content,
`generating` is pending, and `unknown` requires conservative recovery. Retrying
an in-progress POST returns 409 with Retry-After. Both methods check current
controls even when the payload already exists. A restarted/superseded runtime
cannot read with its previous binding.

Admission uses PostgreSQL time and the shared app-first transaction lock. It
atomically reserves the Agent interval, one in-flight batch, and application
quota before invoking the Feed source. The effective interval is the maximum
of requested cadence and application minimum, normally 3600 seconds. An
application initially allows 10000 admitted batches per UTC day; operators can
change this policy, and all its Agents share that counter. An early new round
or exceeded daily quota returns 429 plus Retry-After/retry_after_seconds. Batch
retries and reads do not consume another generation or quota unit. This policy
is independent of existing personal CLI cadence.

Only the latest payload is retained, without a 30-minute cache expiry. Its
monotonic sequence remains the fence after replacement, so an old retry returns
409 instead of regenerating. There is no load_more endpoint or hidden pagination
loop. `has_more` from the legacy cache is not exposed as another entitlement.

The API commits `generating` before making one source call. If the process dies,
the source response is lost, or completion fails, the same sequence is never
sent to the generator again. An expired 120-second generation lease becomes
`unknown`; explicit source failure marks it immediately. Such an admission
conservatively consumes the reserved interval/quota. A later new sequence may
start only when cadence permits. This may skip a failed round; it avoids false
exactly-once claims across PostgreSQL and the Feed RPC. The SDK must likewise
persist delivery intent and treat an unknown consumer outcome separately.

The RPC request pins the applied matching revision; Sort rejects a changed or
unavailable projection before ranking. Feed returns the actually used revision,
and API verifies it before saving. Mixed/old binaries cannot report matching
readiness as if the new context had been applied.

## Operator tool

Apply migration `000128_integration_request_observation.sql` before enabling this
version of the gateway. Operators use `go run ./scripts/integration_admin` through
existing restricted database access; no customer-accessible management route is
added. `PG_DSN` must be explicitly supplied in the operator environment. The tool
does not source `.env`, migrate the database, or deploy services.

The default action is read-only `inspect`. Mutations are dry-run plans unless
`--apply` is supplied, and require an actor and reason. `set-enabled` requires an
explicit scope and true/false value so an omitted app ID cannot accidentally
change the global switch. The global scope is enterprise-only.

```sh
# Inspect one application's Agents, installations, runtime/SDK versions,
# switches, cadence, activity, usage and fixed failure reasons.
go run ./scripts/integration_admin --app-id partner

go run ./scripts/integration_admin --action create-app --app-id partner \
  --name 'Partner application' --max-agents 100 --max-installations 1 \
  --min-poll-seconds 3600 --max-feed-batches-per-day 2400 \
  --actor operator-name --reason 'Approved enterprise integration'

# After reviewing the plan, repeat it with --apply and a new credential file.
# Applications are created disabled; the credential is never printed.
# --credential-out /private/partner.credential

go run ./scripts/integration_admin --action set-enabled --scope app \
  --app-id partner --enabled true --actor operator-name --reason 'Activate approved application'

go run ./scripts/integration_admin --action set-enabled --scope agent \
  --app-id partner --agent-id 123 --enabled false --actor operator-name --reason 'Customer suspension'

go run ./scripts/integration_admin --action set-enabled --scope global \
  --enabled false --actor operator-name --reason 'Enterprise service maintenance'

go run ./scripts/integration_admin --action set-policy --app-id partner \
  --max-feed-batches-per-day 4800 --actor operator-name --reason 'Approved quota change'

go run ./scripts/integration_admin --action rotate-credential --app-id partner \
  --actor operator-name --reason 'Credential rotation'

go run ./scripts/integration_admin --action audit --app-id partner
```

Only executed create/rotate actions require `--credential-out`. The new file is
opened exclusively with mode 0600 before mutation; existing files are refused.
A file write/sync failure after committing a credential requires another audited
rotation to a new file. Deliver the credential through the customer's approved
secret channel; never give a B application the operator's database credential.

`set-policy` retains unspecified fields, occupied identity/installation slots,
current usage counters and enabled state. Raising the minimum interval also
constrains the next admission relative to the last generation, even if the
previously returned cadence hint is shorter. Lowering a limit does not delete
or re-enable existing identities. Installation controls use scope `installation`
plus `--app-id` and `--installation-id`.

Inspection returns at most 100 apps/Agents and 1000 installations with truncation
flags. Filter by `--app-id`/`--agent-id`, and use `--after-agent-id` for subsequent
Agent pages. Audit returns the most recent 100 matching operator records.
Credential hashes, raw context, private matching text and Feed payloads are not
selected by these views. A valid credential is not proof that business is
currently enabled; global/app/Agent/installation switches remain separate.

`request_count`, last request time and fixed last-failure code/time observe
requests that resolved to a managed Agent, including cache hits and disabled
instance attempts. Control checks are included. Requests that cannot be mapped
to an Agent are excluded. Observation is best effort: storage failure logs a
fixed warning, without Token/context, and does not replace the business response.
There is no historical backfill. These counters are operational diagnostics,
not a billing ledger. Application/Agent admitted batch totals remain exact and
transactional; retries do not increment those admission counters.

## Reference application and end-to-end acceptance

[The runnable B-side reference Agent](../../examples/enterprise_sdk/README.md)
uses the private binary wheel, a context provider and a durable customer inbox.
It consumes Feed into its own notes and resolves unknown delivery by batch ID.
It is a functional reference application; it does not claim that an external
Jarvis session or a real customer environment has already been integrated.

The opt-in backend test `TestEnterpriseSDKAcceptance` uses a real native Hertz
HTTP listener, isolated PostgreSQL schemas, the actual context worker/Card
projection, legacy ranking and Feed delivery, and a real compiled SDK/Python
reference process. Fixed local model/search data and a DB-backed Item RPC adapter
replace external services. It verifies context revisions change returned Feed,
private-summary isolation, anonymous registration without personal onboarding,
restart binding/version metadata, retry/cadence enforcement, disabled saved reads,
and re-enable/resume. Fixture timestamps advance a round instead of sleeping
one hour. Registration resume uses the actual 30-second SDK retry interval.

Run it alone because it owns the Feed test process's DB/RPC globals:

```sh
# The explicit loopback test database must already contain all migrations.
# Use a Python virtualenv with the approved private wheel installed.
INTEGRATION_TEST_PG_DSN=postgres://postgres:password@127.0.0.1:5432/disposable_database?sslmode=disable \
EIGENFLUX_SDK_PYTHON=/private/sdk-venv/bin/python \
EIGENFLUX_SDK_RUNTIME=/private/sdk-venv/lib/python3.12/site-packages/eigenflux_sdk/bin/eigenflux-sdk-runtime \
  go test -race ./rpc/feed -run '^TestEnterpriseSDKAcceptance$' -count=1 -v
```

## Activation boundary

These PRs do not deploy, publish a package, or edit production authorization data.
Before a future full rollout, apply migrations 125–128, verify existing CLI
contracts, and enable the environment flag on API/pipeline/Sort/Feed together.
Keep the persistent enterprise global switch and applications disabled until
approved credentials, capacities and customer state locations are ready. Start
with one approved application/Agent, verify one full context-to-consumer round,
and record activation separately from deployment. No personal CLI Feed approval
migration, automatic approval list, or global CLI cadence change is part of this
feature. Webhook, TypeScript wrapper and client auto-update remain later work.

## Validation

```sh
go test ./pkg/integrationidentity ./pkg/integrationprofile ./pkg/agentidentity ./api/integrations ./pkg/config
# First apply all migrations to this disposable test database.
INTEGRATION_TEST_PG_DSN=postgres://postgres:password@127.0.0.1:5432/disposable_database?sslmode=disable \
  go test -race ./pkg/integrationidentity ./pkg/integrationprofile ./pkg/integrationfeed ./api/integrations ./scripts/integration_admin -count=1
```

The PostgreSQL suites require an explicit loopback DSN and create unique isolated
schemas. Identity tests execute the actual migration. Context tests clone the
current fact schema and execute both integration migrations; Card dependencies
are read from the migrated disposable database. Tests cover concurrency, stale
work, concurrent profile edits, summary privacy in rebuilt Cards, structured
fields, retry deduplication, disabled processing, and fail-closed storage errors.
SQLite HTTP tests cover credentials, ownership, strict input, revision conflicts,
and disabled reads. These checks do not establish production deployment state.
