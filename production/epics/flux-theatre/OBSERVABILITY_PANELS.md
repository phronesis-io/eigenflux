# Flux Theatre — Observability Panels (S4 archive + S5 governance)

**Status:** self-contained client slice, ready for the lead to wire in.
**Scope:** `fluxspace_client/lib/panels/**`, `fluxspace_client/test/panels/**` only.
No existing file was modified; no new dependency was added (`dio` and
`flutter_riverpod` already exist; this slice uses `dio` + `flutter` only).
All documentation is in English.

## What this is

The S4 archive (immutable event-source, six view projections, fail-closed
consent ledger, same-spec replay, divergence point) and S5 governance (monthly
budget with an explicit `downgrade:"rule_engine"`, three permission tiers with
default-deny, one-shot high-impact approval, seq+prev_hash audit chain,
model-version consistency) exist **only in the Go Hub backend**. This slice adds
a read-only Flutter surface that makes them visible.

Design rules:

- **Read-only.** Only `GET` endpoints are called. A write/consume route is never
  invoked just to observe state, because doing so would consume a one-shot
  approval, book token spend, or append an audit entry.
- **Nothing is invented.** A field the Hub did not record stays UNKNOWN and
  renders as the literal text `not recorded`. See "The unknown-field invariant"
  below. This follows ADR-0005 ("do not invent metadata") and ADR-0008 §5.
- **A degraded surface is an error state, not an empty list.** A 503
  (`GOVERNANCE_DEGRADED`, `GOVERNANCE_UNCONFIGURED`,
  `ARCHIVE_STORE_UNCONFIGURED`) renders a prominent banner carrying the domain
  code; it never collapses into "no rows".
- **No fabricated endpoints.** Where backend state has no read endpoint, this
  document says so instead of faking a value (last section).

## Files

| File | Role |
| --- | --- |
| `lib/panels/panels_models.dart` | Typed wire models, the `Field<T>` tri-state, `Section<T>`, `PanelsException`, aggregate results. |
| `lib/panels/panels_api.dart` | `PanelsApi` — Dio-backed read-only calls + injectable `PanelsFetcher` for tests. |
| `lib/panels/panels_ui.dart` | Shared overflow-safe widgets (`PanelCard`, `Kv`, `Badge`, `DegradedBanner`, helpers). |
| `lib/panels/governance_panel.dart` | S5 governance panel. |
| `lib/panels/archive_panel.dart` | S4 archive panel. |
| `lib/panels/panels_screen.dart` | `PanelsScreen` — the drop-in composed widget. |
| `test/panels/support/panels_fakes.dart` | Network-free canned JSON + scripted fetcher. |
| `test/panels/panels_models_test.dart`, `panels_api_test.dart`, `panels_widget_test.dart` | Parsing, degraded state, empty lists, widget smoke, 800x600 no-overflow. |

## The unknown-field invariant

`GET` responses are parsed defensively into `Field<T>` (recorded vs unknown).
An unknown/missing scalar renders `not recorded`; it never becomes `0`, `false`,
`""`, an invented tier, a fabricated `downgrade:"rule_engine"`, or an assumed
`allowed`/`denied` audit outcome. Go marshals a nil slice as `null`, which is
treated as a recorded **empty** list (an honest "none"), while a missing key is
unknown. Collections that are genuinely empty render an explicit "no X
recorded (empty list)" state, distinct from a 503.

Concretely:

- `downgrade` in budget — `GET /budget` has no such key; shown as `not recorded`.
- `missing_fields` in archives — each named field is shown as `not recorded`.
- denied operations in the audit — only entries whose `outcome` is explicitly
  recorded as `"denied"` count; an absent outcome is `not recorded`.
- `model_version` absent on a seat — `not recorded`, never a guessed version.

## Endpoints consumed (exact paths, from the Hub source)

The Hub error envelope is `{"error":{"code":..,"message":..}}`
(`hub/api/sessions.go: writeDomainError`). `PanelsApi` maps non-2xx onto
`PanelsException{status, code, message}` and branches on `code`, never prose.
All paths are exposed as `PanelsApi` static helpers (e.g.
`PanelsApi.budgetPath(id)`).

### Governance reads — `hub/api/governance.go: RegisterGovernance`

| Method + path | Response shape (JSON fields) | Visible as |
| --- | --- | --- |
| `GET /hub/companions/{id}/permissions` | `PermissionConfig`: `companion_id`, `tier`, `revoked`, `updated_at` | Tier, revoked flag, three-tier legend, default-deny note. |
| `GET /hub/companions/{id}/budget` | `companion_id`, `month`, `monthly_tokens`, `spent_tokens`, `remaining_tokens`, `configured` | Limit / spent / remaining, configured-vs-default-cap, downgrade indicator (`not recorded`). |
| `GET /hub/audit?companion_id=` | `{entries: [AuditEntry], count}`; `AuditEntry`: `seq`, `owner_id?`, `companion_id`, `operation`, `tier`, `outcome`, `detail?`, `at`, `prev_hash`, `hash` | Audit list with seq / prev_hash / hash badges, denied-operation chips. |
| `GET /hub/audit/verify` | `{verified: true, entries: N}`; or `409 AUDIT_CHAIN_BROKEN` | Chain-verified / chain-broken / unavailable badge. |
| `GET /hub/archives/{id}/model-consistency` | `ModelConsistency`: `consistent`, `code` (`consistent` / `mixed_versions` / `version_unknown` / `no_participants`), `versions`, `seats: [ModelUsage]`, `reason` | Consistency status, code, versions, per-seat model versions. |

### Archive reads — `hub/api/archives.go: RegisterArchives`

| Method + path | Response shape (JSON fields) | Visible as |
| --- | --- | --- |
| `GET /hub/archives` | `{archives: [Summary]}`; `Summary`: `id`, `session_id`, `outcome`, `rounds_played`, `event_count`, `content_hash`, `skeleton_hash`, `verified`, `complete`, `created_at`, `public`, `replay_policy` | Selectable session list. |
| `GET /hub/archives/{id}` | `archiveRecord`: `summary`, `integrity`, `published`, `published_at?`, `consent` (`PolicyDecision`), `config` (`total_rounds`, `seat_count`, `seats: [SeatConfig]`, `replay_consent`) | Archive detail, integrity block, consent decision, seats. |
| `GET /hub/archives/{id}/verify` | `{verified, integrity}` | Integrity badge. |
| `GET /hub/archives/{id}/views/{mode}` | `View`: `mode`, `archive_id`, `session_id`, `speed?`, `seat_id?`, `duration_ms?`, `event_count?`, `events?`, `rounds?`, `moments?`, `comparison?` | Six-view selector and replay stream. |
| `GET /hub/archives/{id}/reprises` | `{reprises: [Reprise]}` | Same-spec reprise list, each with status and divergence. |
| `GET /hub/reprises/{id}` | `Reprise` (`id`, `archive_id`, `type`, `status`, `created_at`, `consent`, `comparison?`, `new_session_id?`) | Exposed as `PanelsApi.fetchReprise`; not called by the default UI. |

The six view modes (`hub/archive/views.go: Modes()`), in order: `full`,
`accelerated`, `single_character`, `omniscient_comparison`, `key_moments`,
`same_spec_comparison`. Query parameters passed through by the panel:
`speed=2` for `accelerated`, `seat_id` for `single_character` (taken from the
archive config seats), `compare_with=<second archive id>` for
`same_spec_comparison`.

`GET /hub/archives/{id}` deliberately returns the metadata record only — it
strips the `events` stream. The six views are projections over that stream
(timeline / round comparison / key moments), not the raw immutable payloads.

## What is visible

**GovernancePanel** — monthly limit / spent / remaining; configured vs default
cap (ADR-0004); downgrade indicator (explicitly `not recorded`); permission tier
+ three-tier legend + default-deny note + denied operations derived from the
audit; audit entries with `seq` / `prev_hash` / `hash` badges and a
chain-verified/chain-broken badge; model-version consistency (code, versions,
per-seat model versions, reason); an explicit `GOVERNANCE_DEGRADED` 503 banner.

**ArchivePanel** — archived-session list; six-view selector; replay timeline /
omniscient rounds / key moments; same-spec divergence point (`0` = "never
diverged", missing = `not recorded`, positive = diverged at round N) and
divergence seat; fail-closed consent ledger (`policy`, `participants`,
`granted`, `missing`, `public`, `reason`); `integrity.missing_fields` rendered
as `not recorded` chips; same-spec reprises; an explicit
`ARCHIVE_STORE_UNCONFIGURED` 503 banner.

`PanelsScreen` composes both behind a two-tab `TabBar` (Governance / Archive),
owns and disposes a `TabController`.

## The exact `PanelsScreen` constructor (for the integrator)

```dart
import 'package:dio/dio.dart';
import 'package:fluxspace_client/panels/panels_api.dart';
import 'package:fluxspace_client/panels/panels_screen.dart';

// The caller owns the Dio: set baseUrl + auth headers (ADR-0009 bearer token).
final PanelsApi api = PanelsApi(
  dio: Dio(BaseOptions(
    baseUrl: 'http://127.0.0.1:8095',
    headers: <String, dynamic>{'Authorization': 'Bearer <owner token>'},
  )),
);

PanelsScreen(
  api: api,                    // OR: dio: myConfiguredDio
  companionId: 'comp_alpha',
  archiveId: 'replay_ab12',   // optional; gates model-consistency + archive bundle
  compareWith: 'replay_cd34', // optional; 2nd archive for same-spec divergence
)
```

Signature (verbatim from `lib/panels/panels_screen.dart`):

```dart
const PanelsScreen({
  Key? key,
  PanelsApi? api,       // provide this OR dio
  Dio? dio,             // optional; a PanelsApi is built around it
  String companionId = '',
  String archiveId = '',
  String compareWith = '',
})
```

At least one of `api` / `dio` must be supplied (asserted in debug). `PanelsApi`
never sets a base URL or a token itself — the caller supplies them on the
`Dio`, matching `HubClient`'s convention.

### `PanelsApi` constructors (also for tests)

```dart
PanelsApi({Dio? dio, PanelsFetcher? fetcher});
PanelsApi.fromDio(Dio dio);

typedef PanelsFetcher = Future<Map<String, dynamic>> Function(
  String path, {Map<String, dynamic>? query});
```

`http_mock_adapter` is **not** a project dependency, so tests inject a
`PanelsFetcher` and need no network. A thrown `PanelsException` is propagated
unchanged; to simulate `GOVERNANCE_DEGRADED`, throw
`PanelsException(status: 503, code: 'GOVERNANCE_DEGRADED', message: ...)`.
When a `Dio` is supplied, `validateStatus` is set so a 503 is inspected and its
`error.code` is recovered instead of being thrown as an opaque Dio string.

## Test results

- `flutter analyze` (whole `fluxspace_client`): **No issues found**.
- `flutter test test/panels`: **49 tests, all passed** — JSON parsing incl.
  missing/unknown/wrong-typed fields and `downgrade`, `GOVERNANCE_DEGRADED`
  state (incl. the real 503 error envelope via an in-memory Dio adapter),
  `AUDIT_CHAIN_BROKEN`, empty and null lists, widget smoke renders for both
  panels and the composed screen, six-view switching, and no `RenderFlex`
  overflow at 800x600.

Both commands were prefixed with:
`env -u http_proxy -u https_proxy -u HTTP_PROXY -u HTTPS_PROXY NO_PROXY=localhost,127.0.0.1`

## What has NO read endpoint yet (backend capability that stays invisible)

These are **not faked** by this slice. The panel shows `not recorded` or a
documented note, and this list is the place to add an endpoint later.

1. **The budget `downgrade:"rule_engine"` value.** The only route that emits it
   is `POST /hub/companions/{id}/budget/usage` → `BudgetDecision`
   (`downgrade,omitempty`). That is a **write** that books token spend and
   appends an audit entry. `GET /hub/companions/{id}/budget` has no `downgrade`
   key. A read-only observability client therefore cannot display the downgrade
   decision; the indicator stays `not recorded`. (A "what would happen if I
   spent N tokens?" pre-flight `GET` does not exist; `Decide` is an internal
   pure function, not an endpoint.)

2. **One-shot `high_impact` approvals.** `POST /hub/companions/{id}/approvals`
   creates one and `POST /hub/companions/{id}/authorize` consumes one.
   `Registry.Approvals()` is diagnostics-only and is **not** exposed over HTTP;
   approvals are deliberately not persisted (ADR-0008 §4). There is no `GET`
   for outstanding/used/expired approvals, their IDs, or their `expires_at`.
   The panel can only show the tier and note the rule.

3. **A read-only authorization decision.** "May this operation run right now?"
   is answered by `POST /hub/companions/{id}/authorize`, which consumes an
   approval and commits an audit entry. There is no `GET` that answers it
   without mutating state, so a client cannot preview a permission decision.

4. **Individual consent `Grant` rows.** The evaluated `PolicyDecision`
   (`policy`, `participants`, `granted`, `missing`, `public`, `reason`) is
   visible via `GET /hub/archives/{id}`'s `consent`. But the per-owner grants
   (`Grant{archive_id, companion_id, owner_id, accepted, at}`) are returned
   only by the **write** `POST /hub/archives/{id}/consent`
   (`{"grants":[...], "decision":{...}}`). There is no `GET .../consent`, so
   who consented/when is not directly readable; only the aggregated decision
   is.

5. **Companion-scoped / global model-version consistency.** The check is
   archive-scoped only: `GET /hub/archives/{id}/model-consistency`. With no
   archive id the governance panel shows `not recorded — no archive
   selected`. There is no "consistency for this companion across matches" or
   fleet-wide endpoint.

6. **A divergence point without a second archive.** `same_spec_comparison`
   requires `compare_with`; a completed `Reprise.comparison` only exists after
   `POST /hub/reprises/{id}/complete` attaches a replayed archive. With fewer
   than two archives the divergence point is `not recorded`; there is no
   companion-scoped divergence endpoint.

7. **The raw immutable event stream / full `Archive` document.**
   `GET /hub/archives/{id}` returns the metadata `archiveRecord` and strips
   `events`, `settlement`, `kind`, `version` and `persona_snapshot` innards.
   The six views expose projections only. There is no `GET` that returns the
   frozen archive bytes for independent client-side verification.

8. **The `different_model` reprise declaration.** `CheckRepriseModelChange`
   (`hub/govern/modelcheck.go`) is a pure function enforcing that a same-spec
   reprise either keeps the model version or is declared as `different_model`.
   No endpoint accepts or reports that declaration flag, so a model swap is
   not observable from the client.

9. **A queryable "governance degraded" health flag.** There is no dedicated
   `GET` returning the degraded boolean; it is surfaced only as a `503`
   `GOVERNANCE_DEGRADED` error envelope on the governance routes (which is
   exactly how this panel detects it). `/health` reports `auth` state but not
   governance degradation.

### Write endpoints deliberately NOT called

`POST /hub/sessions/{id}/archive`, `PUT /hub/companions/{id}/permissions`,
`POST /hub/companions/{id}/permissions/revoke`,
`POST /hub/companions/{id}/approvals`,
`POST /hub/companions/{id}/authorize`,
`PUT /hub/companions/{id}/budget`,
`POST /hub/companions/{id}/budget/usage`,
`POST /hub/archives/{id}/consent`,
`POST /hub/archives/{id}/publish`,
`POST /hub/archives/{id}/reprise`,
`POST /hub/reprises/{id}/complete`.

Calling any of these to "observe" state would mutate it, so the panels stay
strictly read-only.
