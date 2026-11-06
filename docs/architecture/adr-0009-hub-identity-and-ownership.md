# ADR-0009 — Hub identity and companion ownership

- **Status:** **Proposed** — needs owner sign-off on §2.1, §2.2 and §2.3 before implementation
- **Date:** 2026-11-07
- **Relates to:** ADR-0007 (governance controls), ADR-0008 (governance persistence), ADR-0001 decision 3, self-audit findings 5 and 6

## Context

The Hub has exactly one real credential: `HUB_SPECTATOR_TOKEN`, which gates the
omniscient stream. Everything else about "who is calling" is a string the client
puts in the request:

| Claim | Where it comes from today | Enforceable? |
|---|---|---|
| `owner_id` | request body (`SeatClaim.OwnerID`, `join.owner_id`) | **No.** Any client can send any owner id. |
| `companion_id` | request body, and the governance path | **No.** The Hub never records who owns a companion. |
| `seat_id` | query string on the participant stream | **No.** Knowing a session id and a seat id is enough to read that seat's private decisions. |

Consequences that are currently real, not hypothetical:

1. **"One live match per owner" is a client-side courtesy.** `session.Manager`
   keys `owners[ownerID]` on the id from the request, so inventing a second owner
   id produces a second live match for the same human.
2. **Governance isolation is between companions, not against impersonation.**
   `PUT /hub/companions/{id}/permissions` authorises *the id in the path*. With no
   ownership table, nothing distinguishes an owner configuring their own companion
   from anyone configuring someone else's.
3. **Subjective streams are readable by seat id.** The private-decision filter is
   correct; the lock on the door is not there.

ADR-0007 recorded "no authentication on the Hub's endpoints" as a known gap. This
ADR is the decision about how to close it, and it exists separately because the
obvious answer — "add a bearer token" — closes only the first of the three.

## Decision

### 1. Three separable problems, not one

| Problem | Question | Closed by |
|---|---|---|
| **Authentication** | Who is calling? | A bearer token per owner (§2.1) |
| **Ownership** | May this caller act on this `companion_id`? | A Hub-side companion registry (§2.2) |
| **Stream access** | May this caller read this seat's stream? | A per-seat stream token issued at join (§2.3) |

They are decided separately because they fail separately. Adding authentication
alone produces an endpoint that *checks a token and still lets you rename
`companion_id` in the path* — a control that looks stronger than it is, which is
worse than an obvious absence.

### 2.1 Authentication — per-owner bearer tokens from the environment

`HUB_OWNER_TOKENS` is a JSON object mapping `owner_id` to a token. The Hub
compares with `subtle.ConstantTimeCompare` and derives the caller's `owner_id`
**from the token**, never from the request body. Request bodies that also carry an
`owner_id` are validated against the derived one and rejected on mismatch
(`OWNER_MISMATCH`), so a stale client cannot act as the wrong owner by accident.

Fail-closed startup: with no tokens configured, `main` refuses to start unless
`HUB_ALLOW_UNAUTHENTICATED=1` is set explicitly. An unauthenticated Hub stays
available for local work, but it must be *chosen*, not fallen into. `/health`
reports `auth: "disabled" | "enabled"` so a deployment can be checked with one
curl.

*Rejected:* delegated EigenFlux agent tokens verified against the gateway. It is
the better long-term answer — no local secret store, identity follows the Agent —
but it makes every Hub request depend on the gateway, and the full stack has
never run here (G10). Deciding it now would mean writing an integration that
cannot be verified. §4 records it as the upgrade path.

*Rejected:* HMAC-signed requests (timestamp + nonce). Stateless and replay
resistant, but it puts canonical-serialisation and clock work into the Flutter
client, and phase-1 has one client. The cost lands before the benefit.

### 2.2 Ownership — a companion registry on the Hub

New state: `companion_id → owner_id`, created on first bind, persisted through the
ADR-0008 store (`state.json`, snapshot + atomic replace), audited like every other
authority change.

```
POST /hub/companions            {companion_id}          → binds to the token's owner
GET  /hub/companions/{id}                                → {companion_id, owner_id, bound_at}
```

Rules:

- **Binding is idempotent and immutable for an existing pairing.** Re-binding a
  companion to a different owner is refused (`COMPANION_ALREADY_BOUND`), not
  transferred. Transfer needs its own decision, because it is a hijack primitive
  otherwise.
- Every governance endpoint that names a companion resolves the registry first and
  answers `404 COMPANION_NOT_REGISTERED` or `403 COMPANION_NOT_YOURS` before it
  looks at a tier, a budget or an approval.
- Unregistered companions are **denied**, which is a breaking change: a client
  that used to `PUT /permissions` on any id must bind first. That is the point —
  the old behaviour is the vulnerability.
- The registry lives on the Hub and not the client for the same reason the
  governance state does: a client that asserts its own ownership proves nothing.

*Rejected:* derive ownership from live match seats (`SeatClaim.OwnerID` and
`CompanionID` already sit in the same struct). Cheap, no new state, but it only
covers companions that happen to be seated right now, and a companion configured
*before* a match is exactly the case governance has to cover.

*Rejected:* audit-only ownership (accept the client's claim, record it, enforce
nothing). That is today's behaviour with extra bytes.

### 2.3 Stream access — a per-seat token issued at join

`POST /hub/sessions/{id}/join` returns a `stream_token` for that seat. The token is
stored **hashed** (SHA-256) in the session state, compared in constant time, and
required by `GET /hub/sessions/{id}/events` instead of `?seat_id=`, which becomes
a hint that must agree with the token's seat.

This is self-contained: it needs no identity provider, no registry and no
persistence (sessions are in memory anyway), so it can ship independently of
§2.1 and §2.2. It closes finding 5 without waiting for the rest.

*Rejected:* require the owner bearer token plus a registry lookup of the seat's
owner. Correct, but it makes the stream depend on two things not yet built, and a
seat token is strictly less authority than an owner token — the smaller credential
on the wider surface is the right shape.

### 3. What the audit log gains

Every entry records `owner_id` alongside `companion_id`, derived from the token.
"Which companion tried" becomes "which owner's companion tried", and the chain can
answer a question it currently cannot: was this one owner doing many things, or
many owners doing one thing each.

### 4. Upgrade path, deliberately not taken now

Delegated EigenFlux agent tokens: the Hub accepts an Agent access token, verifies
it against the gateway on first use, caches the result for the token's lifetime,
and derives `owner_id` from the verified Agent record. This removes the local
token map and makes identity follow the platform. It is blocked on the same thing
S1 is: no verified full-stack environment (G10).

## Consequences

- **Three breaking changes**: unregistered companions are denied, `owner_id` in a
  body must match the token, and `/events` needs a token instead of a query
  parameter. All three are intentional; none has a compatibility shim, because a
  shim here is the vulnerability with a switch on it.
- **Token rotation needs a restart** (or an operator edit + restart). No hot
  reload, no expiry, no revocation list. Forging an owner token is therefore
  equivalent to full control of that owner's companions.
- **Tests do not need tokens.** They construct `api.Server` directly; only `main`
  enforces startup policy, so the change does not ripple through 130 tests.
- **The governance write path grows one lookup.** Registry resolution happens
  before any decision, so a `503 GOVERNANCE_DEGRADED` or a `404` still precedes
  any state change.
- **`HUB_ALLOW_UNAUTHENTICATED=1` will be used by someone in production.** It is
  visible in `/health` for exactly that reason; nothing else will catch it.

## Not claimed done

- No per-companion scoped token: an owner token acts for all of that owner's
  companions. Compartments inside one owner are not modelled.
- No replay protection (no nonce, no signature), no rate limiting, no lockout.
- No OAuth/OIDC, no mTLS, no key rotation protocol.
- No ownership transfer flow; a mis-bound companion needs operator intervention.
- The spectator token remains a separate, manually managed secret. This ADR does
  not unify it.
- Nothing here authorises *companions* acting on their own behalf; that is the
  high-impact approval path in ADR-0007, and it is unaffected.

## The three signatures required

1. **§2.1** — per-owner bearer tokens + fail-closed startup, versus delegated
   platform tokens later.
2. **§2.2** — a Hub-side companion registry, accepting the breaking change that
   unregistered companions are denied.
3. **§2.3** — per-seat stream tokens issued at join, shipping independently.
