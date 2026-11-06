# FluxSpace contracts (MVP phase 1)

Authoritative wire and storage contracts. All new docs are English per
AGENTS.md. Source of truth for behaviour is `design/game-brief.md`; source of
truth for the settlement maths is the frozen `fluxspace/_check.js`.

## 1. Dimension mismatch (must be resolved by design)

Two different six-dimension systems exist:

| Layer | Fields | Range | Source |
|---|---|---|---|
| GDD persona traits | `risk`, `information`, `social`, `commitment`, `expression`, `competition` | -2..+2 | `design/game-brief.md` "Persona storage format" |
| Engine vector | `risk`, `logic`, `empathy`, `coop`, `aggression`, `intuition` | 0..100 | `fluxspace/_check.js` `scoreAction` |

`hub/models/persona.go` implements an explicit, tested approximation:

```
Risk       = 50 + 25*risk
Logic      = 50 + 25*information
Coop       = 50 + 20*social + 5*commitment
Aggression = 50 + 25*competition
Intuition  = 50 + 20*expression - 5*information
Empathy    = 50 + 15*social + 10*expression
```

All outputs clamp to 0..100. This is **not** an equivalence claim. Story 002
AC9 ("every persona's behaviour and game preference matches the brief") cannot
be signed off until design confirms or replaces this mapping.

## 2. Persona assets

`fluxspace_client/assets/personas/*.yaml`. Only `lin_shen.yaml` has
`behavior_notes` and `family_bias` in the GDD. The other three files were
filled in during implementation and are marked `ADDED BY IMPLEMENTATION ...
Pending design sign-off` in-file. Do not treat them as GDD-authored content.

## 3. Hub REST + SSE (GDD "Hub API")

Base URL `http://127.0.0.1:8095`, overridable with `HUB_ADDR`.

| Method | Path | Purpose |
|---|---|---|
| GET | `/health` | Liveness; `200 {"status":"ok","service":"fluxspace-hub"}` |
| POST | `/hub/sessions` | Create session, returns `session_id` + SSE endpoints |
| POST | `/hub/sessions/{id}/join` | Join a seat |
| POST | `/hub/sessions/{id}/actions` | Submit a validated decision |
| GET | `/hub/sessions/{id}` | State query |
| GET | `/hub/sessions/{id}/events` | SSE state changes for a participant |
| GET | `/hub/sessions/{id}/spectate` | SSE omniscient stream, spectator role auth |
| GET | `/hub/sessions/{id}/snapshot` | Full state for reconnect recovery |
| POST | `/hub/sessions/{id}/advance` | Advance `observe -> discuss -> act`; resolve is implicit |
| POST | `/hub/sessions/{id}/discussion` | One bounded discussion line (max 2 per seat per round) |
| POST | `/hub/invitations` | Generate three distinct proposals and open a negotiation |
| GET | `/hub/invitations/{id}` | Negotiation state, persisted inbox and confirmations |
| POST | `/hub/invitations/{id}/send` | Send the structured invitation PM through `EigenFluxPort` |
| POST | `/hub/invitations/{id}/reply` | Apply a locally supplied accept/reject/modify reply |
| POST | `/hub/invitations/{id}/poll` | Fetch, persist, parse and apply replies arriving over PM |
| POST | `/hub/invitations/{id}/session` | Turn an all-confirmed negotiation into real seat claims and a Hub session |
| GET | `/hub/sessions/{id}/followup` | Preview the derived post-match follow-up document |
| POST | `/hub/sessions/{id}/followup` | Deliver one follow-up PM per real owner (idempotent) |

**Status (2026-11-04):** all endpoints above are implemented in `hub/api/sessions.go`
and covered by tests in `hub/api/sessions_test.go`. The two `POST .../advance`
and `POST .../discussion` rows are phase-1 additions to this contract: the GDD's
four-phase loop needs an explicit phase transition because the Hub is a
deterministic referee and does not sleep for the GDD's wall-clock phase
durations (observe 5s / discuss 60s / act 30s). The client owns those timers.
A round resolves automatically as soon as every joined seat has submitted a
validated decision.

Implementation notes that are part of the contract:

- `POST /hub/sessions` accepts `{invitation, claims, seed, companion_count,
  pace, mode}`. The Hub generates the skeleton from `seed` via
  `generator.GenerateSkeleton`; callers cannot inject a skeleton. Two invariants
  are enforced at creation and at join: one seat per owner, and one live match
  per owner. Violations return `409 OWNER_SEAT_CONFLICT` /
  `409 OWNER_HAS_LIVE_MATCH`. Owner locks are released when the match finishes.
- `POST .../actions` re-validates `action_id` against the current round's
  action pool (`400 ILLEGAL_ACTION_ID`), caps `reasoning_summary` at 200
  characters and `confidence` to `[0,100]`. Duplicate submissions return
  `409 DECISION_ALREADY_SUBMITTED`.
- `state_snapshot` in an SSE event is a compact view (round, phase, status,
  metrics, outcome). The full state is the `/snapshot` endpoint. SSE delivery
  polls the retained event log rather than holding a subscriber channel, so a
  slow observer cannot drop, reorder or block an event. `Last-Event-ID` (or
  `?last_event_id=`) recovery uses the same read path.
- `/events` requires `?seat_id=` and filters out private and omniscient-only
  events not intended for that seat. `/spectate` requires
  `X-Spectator-Token`; when `HUB_SPECTATOR_TOKEN` is unset the route returns
  `503 SPECTATOR_AUTH_UNCONFIGURED` rather than exposing game secrets.
- `EventPhaseChanged` is a phase-1 event type added alongside the eight replay
  events below. It records joins and phase transitions for replay archives.

**Negotiation endpoints (S1).** `POST /hub/invitations` generates three
proposals with distinct `generator.Fingerprint` values (`StructuralHash` is
topology-only and deliberately collides in the frozen prototype). `send` calls
`EnsureFriend` then `SendPM`; `poll` fetches the initiator's inbox, persists
every returned message before interpreting it (caution 3), ignores unrelated
PMs and applies each `msg_id` once. `accept` must name `selected_proposal`;
`modify` regenerates the proposal set and requires both sides to confirm again
(max 3 rounds). Timeout is 5 minutes. `POST .../session` only works from an
all-confirmed negotiation and feeds the same `session.Manager.Create`
validation as the direct endpoint, including seat uniqueness and
one-live-match-per-owner. Phase-1 deduction supports 2-4 seats:
`generator.MaxSelected` is 4, and claims of 5-8 now return an explicit
`400 UNSUPPORTED_SEAT_COUNT` instead of failing inside the generator.

**Follow-up endpoints (S1).** `GET .../followup` derives a
`fluxspace.followup.v1` document from the retained event log only: every memory
carries `source.kind`, `source.ref_id` (`<session>:<event id>`) and
`source.round`; private-channel events never enter a shared PM. Relationship
updates are labelled `draft: true` and are **not** persisted (S4 owns the
relationships table). `POST .../followup` sends one PM per non-official seat,
scoped to that companion's own memories, and is idempotent per Agent. Both
require a finished match and a configured `HUB_GATEWAY_URL` + `HUB_AGENT_ID`;
otherwise they return an explicit 409/503 rather than faking delivery.

**Not implemented here:** a PM round trip against the *live* EigenFlux stack.
The full loop runs over real HTTP against a local gateway stand-in
(`TestS1FullLoopOverHTTP`); WSL 2 + a running Docker daemon are still needed
for G10. `GatewayPort` is a real HTTP client, not a mock of the port.

### SSE event envelope

Every event is one `data:` frame. `id` is a monotonic per-session integer;
reconnect uses `Last-Event-ID` plus `/snapshot`.

```json
{
  "id": 42,
  "type": "match_start|round_begin|information_reveal|discussion|decision|resolution|branch_event|match_end",
  "session_id": "sess_...",
  "round": 2,
  "phase": "observe|discuss|act|resolve",
  "seat_id": "seat_0",
  "visibility": "public|private|omniscient",
  "payload": { },
  "state_snapshot": { },
  "ts": 1730000000000
}
```

`decision` events carry the three-density summaries from story 008 AC2:
`one_liner`, `structured`, `comparison`, plus `state_snapshot`. Private
channels are delivered only to intended seats; the omniscient stream sees all.

Replay archive endpoints are a separate surface; see §10.

## 4. Invitation JSON (PM `content` field)

```json
{
  "kind": "fluxspace.invitation.v1",
  "invitation_id": "inv_...",
  "from_agent_id": "agent_...",
  "from_companion_id": "comp_...",
  "to_agent_id": "agent_...",
  "proposals": [
    { "index": 1, "type": "deduction", "seats": 4, "minutes": 12,
      "structural_hash": "sh_...", "content_hash": "ch_...",
      "summary": "...", "contract_points": ["..."] }
  ],
  "social_contract_draft": {
    "sharing": "open", "private_channels": "allowed",
    "replay_consent": "all", "visibility": "private"
  },
  "estimated_minutes": 12,
  "expires_at": 1730000300000,
  "selected_proposal": 1
}
```

`selected_proposal` is a phase-1 addition: the Hub needs to know which of the
three proposals the participants confirmed before it creates a session. It is
validated against `proposals[]` at `POST /hub/sessions`.

**Create request (`POST /hub/invitations`).** The body is decoded into
`invitation.Request`. Alongside the party/seat/pace/mode fields it accepts an
optional `history_fingerprints` array carrying the partner group's most recent
completed-match fingerprints:

```json
{
  "invitation_id": "inv_...",
  "initiator": { "agent_id": "agent_...", "owner_id": "owner_..." },
  "responder": { "agent_id": "agent_...", "owner_id": "owner_..." },
  "seats": 2,
  "pace": "standard",
  "mode": "coop",
  "history_fingerprints": ["fp_...", "fp_...", "fp_...", "fp_...", "fp_..."]
}
```

`history_fingerprints` is optional: omitted or empty means no cross-match dedup
and proposal generation is unchanged. The Hub keeps only the last five entries
in a `generator.FingerprintWindow` (deduplicated, empties dropped) and excludes
those `generator.Fingerprint` values from all three generated proposals, so the
same companion group does not see a repeated game structure within five matches
(story-005 AC6 / the GDD Layer-2 rule). The comparison uses
`generator.Fingerprint`, **not** `StructuralHash`, because the latter is
topology-only and deliberately collides. The field is used solely for
in-negotiation dedup: the Hub does not persist match history to any durable
store (S1/S2 keep negotiations in memory), it is retained on the negotiation
only to carry it across `modify` rounds, and generated proposal fingerprints
are never written back as completed-match history. `decodeBody` uses
`DisallowUnknownFields`, so the field is accepted only because it is declared
on `invitation.Request`.

Each entry in the returned `proposals[]` carries a `fingerprint` alongside
`structural_hash` and `content_hash`. Callers must record the `fingerprint` of
the proposal that was actually played and send it back in the next
`history_fingerprints`: dedup runs on `generator.Fingerprint`, so without that
field no client could build the five-match history at all. `structural_hash`
is not a substitute — it is topology-only and deliberately collides.

Replies use `kind: "fluxspace.invitation.reply.v1"`:

```json
{
  "kind": "fluxspace.invitation.reply.v1",
  "invitation_id": "inv_...",
  "action": "accept | reject | modify",
  "agent_id": "agent_...",
  "selected_proposal": 1,
  "amendments": []
}
```

`selected_proposal` is required in spirit: an `accept` without it defaults to
the initiator's current choice. A counter-proposal clears the initiator's
confirmation until it explicitly accepts the same index. `modify` regenerates
all three proposals and is capped at 3 rounds. Timeout is 5 minutes (story
003 AC8); expired negotiations are terminal and cannot create a session.
Official backfill seats are labelled in `BuildClaims`, not silently inserted by
the negotiation. The social contract is a gentleman's agreement: recorded and
displayed, never system-enforced (GDD line 413).

Post-match PMs use `kind: "fluxspace.followup.v1"`. They carry source-tagged
`memories[]`, draft `relationship_updates[]`, `unresolved_threads[]` derived
from final metric values, and `recipient_companion_id`. No model call is made
and no reasoning is invented: every line traces to a retained Hub event.

## 5. Decision JSON Schema (GDD "Model adapter")

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["action_id", "reasoning_summary"],
  "properties": {
    "action_id": { "type": "string" },
    "reasoning_summary": { "type": "string", "maxLength": 200 },
    "confidence": { "type": "number", "minimum": 0, "maximum": 100 }
  },
  "additionalProperties": false
}
```

`action_id` must exist in the current round's action pool. A rejection triggers
one retry, then the highest personality-weight default action (GDD line 786).
Discussion text is capped at 200 characters. Reflection output is a
`memories[]` array, 1-3 entries, 500-token ceiling, 30-minute cooldown.

The Hub accepts one optional extra field, `source`
(`rule_engine | model | fallback | client`), so a replay can say who produced a
decision. It is never sent to a model provider. The retry/fallback path stays on
the client: the Hub only receives an already-validated decision and re-checks
its legality (ADR-0001 decision 3).

## 6. Local SQLite schema

Six tables, mirrored verbatim in
`fluxspace_client/lib/services/database.dart` (`LocalDatabaseSchema`).
`companions`, `memories`, `relationships`, `match_history`, `settings`,
`threads`. Memories carry `source_kind`, `source_ref_id`, `source_round`.
Relationships carry the five dimensions `closeness`, `trust`, `rapport`,
`respect`, `resentment` plus a derived `status`. Encrypted at rest; key in the
platform keychain. Memories and relationships are never synced.

## 7. EigenFlux adapter contract (real full-stack decision)

```go
type EigenFluxPort interface {
    EnsureFriend(ctx context.Context, fromAgentID, toAgentID string) error
    SendPM(ctx context.Context, fromAgentID, toAgentID string, body json.RawMessage) (messageID string, err error)
    ListPM(ctx context.Context, agentID string, sinceID string) ([]RawMessage, error)
    RefreshAccessToken(ctx context.Context, agentID string) (token string, err error)
    PublishAgentCardFields(ctx context.Context, agentID string, fields map[string]string) error
}
```

`hub/services/eigenflux.go` implements this with a real HTTP client, not a
mock. Verified against `idl/api.thrift`: `POST /api/v1/pm/send`,
`GET /api/v1/pm/fetch`, `POST /api/v1/relations/apply`. The PM `content` field
is a **string** in the IDL, so a FluxSpace invitation JSON document is
stringified into it; `RawMessage.UnmarshalJSON` unwraps it back into an object.
`request_id` is a stable hash of that body, which makes retries idempotent
against the gateway's 60-second dedup window. `ListPM` does not persist
messages: because fetch marks them read upstream, the negotiation manager
appends the whole returned slice to `Negotiation.Inbox` before interpreting
anything, deduplicated by `msg_id` (caution 3).

**G9 resolved by static verification on 2026-11-04.** Two endpoint shapes
were wrong in the first pass and are now fixed against the root module:

- There is **no v1 token-refresh RPC**. The earlier code called a fabricated
  `/api/v1/auth/refresh`. The real v2 flow (`POST /api/v2/agent-sessions/
  refresh-challenges` then `/refresh` in `api/consolev2/service.go`) needs the
  Agent's `refresh_token` and an ed25519 signature, which the Hub must not
  hold. A `401` now uses an injected `services.TokenRefresher`, or returns
  `TOKEN_REFRESH_NOT_CONFIGURED` with no network call. Clients can push a
  rotated token through `GatewayPort.SetAccessToken`.
- `PUT /api/v1/agents/me/profile/fields` (`api/agentcard/handlers.go`) expects
  `{expected_version, updates, source}` and reads the optimistic-lock version
  from `data.profile_version` on
  `GET /api/v1/agents/me/card/refresh-context`. The earlier `fields`/`version`
  names and the invented `game_preference` field would all have failed; field
  names are now whitelisted against `pkg/agentcard/registry.go` and
  `string_list` values are sent as JSON arrays.

Live-stack confirmation of both paths is still G10, but neither path is a
guess any more.

Integration cautions from GDD lines 772-779, all mandatory:

1. V2 features are flag-gated and default-disabled — verify deployment flags.
2. PM requires an existing friend relation — new contacts need a broadcast
   ice-break or friend request first.
3. PM fetch marks messages as read — persist immediately on receipt.
4. WebSocket is server-push only — clients cannot send via WS.
5. One WS connection per Agent — a new connection replaces the old (code 4002).
6. Access tokens expire in 15 minutes — implement refresh token rotation.

One EigenFlux Agent maps to up to four FluxSpace Companions. Companion ID is
FluxSpace-internal. Personality, memories and relationships are never written
into Agent Card projections.

## 8. Budgets

| Limit | Value | Source |
|---|---|---|
| Per-decision input | 800 tokens | GDD line 787 |
| Per-decision output | 200 tokens | GDD line 787 |
| Reflection cap | 500 tokens | GDD line 643 |
| Per-companion monthly cap | 200,000 tokens | Ratified 2026-11-06 by owner; see `adr-0004-signed-design-decisions.md` (G8/§8 option A). GDD is silent; this is an owner decision, not a GDD citation. |

Phase 1 spends zero tokens (local rule engine). Over-limit behaviour is a
downgrade to the local rule engine, never a silent quality reduction.

## 9. Known gaps (tracked, not hidden)

| # | Gap | Evidence |
|---|---|---|
| G1 | ~~Calibration targets unreachable~~ **RESOLVED (ADR-0003)** | `cd fluxspace && node _balance.js` → 480 matches: 7 wins / 41 losses / 432 timeouts = 10% non-rounds-out. GDD and story 005 AC2/AC10 want ~90% / 40-60% / ~10% win rates and ≥60% non-rounds-out. Pinned by `TestCalibrationGDDGapIsTracked`. Resolved by ADR-0003 (`adr-0003-relaxed-calibration-targets.md`): the owner relaxed the GDD calibration targets and kept `fluxspace/` frozen. |
| G2 | ~~Two incompatible persona dimension systems~~ **RESOLVED (owner decision, ADR-0004)** | Section 1 above. Resolved by ADR-0004: option A, "Confirm the existing `hub/models/persona.go` approximation", is signed as the basis for Story 002 AC9. |
| G3 | ~~Three persona definitions not in the GDD~~ **RESOLVED (owner decision, ADR-0004)** | Section 2 above. Resolved by ADR-0004: option A, "Retroactive sign-off of the three YAMLs", approves the three implementation-added persona YAMLs. |
| G4 | ~~Story 002 dependency inversion~~ **RESOLVED (owner decision, ADR-0004)** | Needs a real authorised replay clip (story 008) and a live model adapter (story 004/010), but lists only story 001. Resolved by ADR-0004: option A, "Re-hang story 002 onto S3 with explicit dependencies on S4 artefacts", makes the S4 replay clip and live model adapter explicit prerequisites. |
| G5 | ~~Numeric conflicts~~ **RESOLVED (owner decision, ADR-0004)** | Story 009 AC4 "1 per character per day" vs GDD "important-memory alerts max 2/day"; story 008 AC10 flat 50-500 FP vs GDD per-category ranges. Resolved by ADR-0004: option A, "The GDD wins", replaces the legacy story numbers with the GDD values. |
| G6 | ~~No Rive/2.5D assets~~ **RESOLVED (owner decision, ADR-0004)** | Story 006 needs four personas × idle/walk/expression plus backgrounds at 60fps; no asset files, art bible or evidence directory exist. Resolved by ADR-0004: option A, "Procure or commission the assets externally", is selected; S3 remains blocked until assets are delivered. |
| G7 | ~~Onboarding clip is a demo~~ **RESOLVED (owner decision, ADR-0004)** | No real authorised replay exists. Phase 1 uses a deterministic demo event stream, explicitly labelled. This discounts GDD's "real + authorized". Resolved by ADR-0004: option A, "Accept the labelled demo as an MVP interim substitute", accepts the labelled demo clip for MVP, to be replaced when S4 delivers a real authorised replay. |
| G8 | Legacy stories are Chinese and horizontally sliced | AGENTS.md requires English docs; GDD build order is five vertical slices. `IMPLEMENTATION_PLAN.md` supersedes them; `EPIC.md` now marks all ten `Superseded`. |
| G9 | ~~Two EigenFlux endpoints are unverified~~ **RESOLVED (static)** | See §7: the fabricated `/api/v1/auth/refresh` was removed in favour of an injected `TokenRefresher`, and the profile-fields request/response shape was corrected against `api/agentcard/handlers.go` and `pkg/agentcard/registry.go`. Both are covered by tests in `hub/services/eigenflux_test.go`. Runtime confirmation against the live gateway is folded into G10. |
| G10 | Live-stack e2e has never run | Docker CLI 29.9.2 is present but `docker info` reports the daemon pipe is missing; WSL is not enabled and `wsl.exe`/`reg.exe` are blocked by host command-security policy. `TestS1FullLoopOverHTTP` now drives invitation send -> PM reply -> fetch -> confirm -> 5-round match -> follow-up PM over **real HTTP against a local gateway stand-in**. The S1 exit criterion wording says "through EigenFlux", so a running EigenFlux stack is still required before sign-off. |
| G11 | Post-match Agent Card projection not invoked | ADR-0001 decision 2 says post-match social propagation goes back through PM **and** the profile/fields API. `GatewayPort.PublishAgentCardFields` is implemented and its shape is now verified, but the follow-up sender only sends PMs. Publishing a public recap is deliberately deferred to S4 (memory/relationship persistence), where the owning client holds the Agent token. Tracked, not claimed as done. |

G10/G11 are environment and scope gaps, not design gaps. No code in this
repository fakes a successful live round trip: the port issues real HTTP
requests, unconfigured paths fail loudly, and tests assert request shapes
against a local HTTP server. G1-G7 are now adjudicated by ADR-0003/ADR-0004;
G10/G11 remain environment/scope gaps unaffected by this sign-off.

**Owner decisions recorded 2026-11-06 in `adr-0002-s1-acceptance-and-g1-baseline.md`:**
the S1 exit criterion wording is kept verbatim (S1 stays *code complete,
live-stack sign-off pending* — G10 is not waived); the G1 authoritative
baseline is `fluxspace/_balance.js` full Runtime; the 2,081 untracked
root-module files must not be committed until a git remote is aligned; and
Flutter installation moves to the AI while WSL 2 stays owner-side. G4 was in
this table but missing from `EPIC.md`'s decision list; it is now tracked there.

**Subsequent owner decisions (2026-11-06, same day):** ADR-0003 resolved the
second half of G1 by **relaxing the targets** — the three-path win-rate and
>=60% non-`rounds_out` gates are retired, the measured 10% is a regression
floor, `fluxspace/` stays frozen and the golden chain is untouched. ADR-0004
then selected option A for all seven remaining design gaps (G2, G3, G5, §8,
G6, G7, G4). `DESIGN_DECISIONS_DRAFT.md` retains the three costed options per
gap as history and now carries a filled sign-off record; the effective
decisions live in ADR-0004. See the `~~RESOLVED~~` markers in the §9 table.

**Repository provenance finding (2026-11-06):** the first remote probe returned
a transient `502`; on retry `git ls-remote` succeeded with
`6aefaa0a61cf32ab2b24398333690c6863aa83e3` on `main`. A read-only comparison
against the public GitHub REST API (no fetch, no clone, no remote configured,
no `git config` writes) found 2,069 of 2,081 untracked local paths present in
`main`, 1,802 shared paths identical in size, and 8 of 10 sampled files with
identical blob SHAs — so this snapshot is derived from the official repository
but is **stale** (267 paths differ; four files from the newest upstream commit
are absent locally; `CLAUDE.md` and `api_service.go` differ in content). None
of `hub/`, `fluxspace_client/`, `production/epics/flux-theatre/`, `fluxspace/`
or `adr-0002-*` exist upstream; all Flux Theatre work is new. The exact
historical base commit is undetermined. No remote was configured, no fetch
attempted and no credentials stored. ADR-0002 decision 3 therefore now waits
on a merge-strategy decision, not on network access.

## 10. Replay archive, viewing modes and consent (S4)

An archive is a frozen, event-sourced record of one finished match. It is
immutable in the strict sense: everything that defines the match is captured
once at `Build` time, both hashes cover that capture, and the mutable state
(publication, consent) lives in the store, never in the archive. Publishing
therefore cannot invalidate an integrity check.

### Endpoints

| Method | Path | Purpose |
|---|---|---|
| POST | `/hub/sessions/{id}/archive` | Freeze a finished session; optional `seats[]` enrichment and `archive_id` |
| GET | `/hub/archives` | List archive summaries |
| GET | `/hub/archives/{id}` | Archive metadata (no event stream) |
| GET | `/hub/archives/{id}/verify` | Recompute both hashes and the event count |
| GET | `/hub/archives/{id}/views/{mode}` | One of the six modes; see query parameters below |
| POST | `/hub/archives/{id}/consent` | Record one participant owner's grant or refusal |
| POST | `/hub/archives/{id}/publish` | Evaluate the consent policy and publish |
| POST | `/hub/archives/{id}/reprise` | Authorise a same-spec re-run |
| GET | `/hub/archives/{id}/reprises` | List reprises for an archive |
| GET | `/hub/reprises/{id}` | One reprise record |
| POST | `/hub/reprises/{id}/complete` | Attach the replayed archive and compute divergence |

`POST .../archive` refuses anything that is not a finished match, and refuses
an event stream without exactly one `match_start` and one trailing `match_end`
- the six modes all assume a terminal event.

### Integrity

`integrity.content_hash` is `sha256` over `{version, session_id, config,
events, settlement}`; `integrity.skeleton_hash` is `sha256` over the skeleton
alone, which is what a same-spec reprise compares. `event_count` is stored
alongside and re-checked on every load: `Load` decodes, re-verifies and refuses
to return bytes that do not match, so a tampered file fails instead of playing.

`integrity.complete` is false, and `integrity.missing_fields` names exactly
which seat fields the Hub could not capture. The Hub does not know persona ids
or model versions; it records their absence rather than a plausible default.
A caller may supply them in `seats[]` at archive time, which flips
`persona_source` to `client_supplied`; the six-dimension `PersonaSnapshot` the
seat claim carried is always present.

### Viewing modes

`mode` is one of `full`, `accelerated`, `single_character`,
`omniscient_comparison`, `key_moments`, `same_spec_comparison`.

| Mode | Query | What it returns |
|---|---|---|
| `full` | - | Every event in sequence, with `offset_ms` from the first event |
| `accelerated` | `speed=2\|4` | Same events, compressed timeline; any other speed is a 422, not a silent rounding |
| `single_character` | `seat_id=` | Public events plus that seat's own private events; omniscient-only events never appear |
| `omniscient_comparison` | - | Round-by-round decisions for every seat, in seat order, with the round's agreement, synergy and metric delta |
| `key_moments` | `moment_limit=` | Branch events, the highest-magnitude metric swings and the terminal outcome, each with the numbers that justified it |
| `same_spec_comparison` | `compare_with=<archive_id>` | Divergence point, per-round per-seat action differences, outcome change |

`key_moments` is derived, never editorial: it uses branch effects and metric
deltas only, is deterministic for a given archive, and always ends with the
outcome event.

### Consent and publication

The policy comes from the archive's frozen `social_contract.replay_consent`:

| Policy | Publication requires |
|---|---|
| `all` (and any unspecified value) | every signing participant |
| `majority` | strictly more than half, so a two-seat match still needs both owners |
| `none` | never |

Fail-closed: an unspecified policy is treated as `all`, and an unrecognised
policy is an error rather than a permissive default. Signing participants are
the non-official seats' companions, in seat order; official backfills have no
owner, so asking them would be theatre rather than consent. A refusal is a
`403 REPLAY_CONSENT_INCOMPLETE` whose body names the missing participants.

A same-spec reprise always requires **every** participant, regardless of the
publication policy, because the GDD ties reprises to the original owners'
consent. `POST /hub/archives/{id}/reprise` therefore fails with
`403 REPRISE_CONSENT_INCOMPLETE` until all have granted.

`POST /hub/reprises/{id}/complete` compares two archives and rejects
`NOT_A_SAME_SPEC_REPRISE` unless the skeleton hashes match and the seat
configuration, contract and round count are identical. Divergence is the first
round in which any seat's `action_id` differs, with the specific seat recorded.
