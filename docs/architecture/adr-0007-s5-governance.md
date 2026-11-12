# ADR-0007 — S5 governance: budget, permissions, audit and model consistency

- **Status:** Accepted
- **Date:** 2026-11-07
- **Relates to:** ADR-0001 decision 3, ADR-0004 (§8 token cap), ADR-0005 (consent), ADR-0006

## Context

S5's exit criterion names five controls: *budget downgrade path, per-companion
permission isolation, audit log, consent gate, model-version consistency check*.
Four of them share a property that decides where they can live: they are all
**authority** questions. A client that grants its own permission, reports its own
spend and writes its own audit trail proves nothing — that is a diary, not
governance. The Hub is the only party in this system that neither the owner nor
the companion controls.

Two constraints shape the rest:

- ADR-0001 decision 3 keeps personality, memories and relationships client-side,
  so the Hub cannot be the one that decides *what* a companion wants to do. It
  can only decide whether it is allowed.
- Phase 1 spends zero tokens (the local rule engine), so the budget path cannot
  be validated by real model calls. It has to be correct as accounting and as a
  refusal, not as a billing system.

## Decision

### 1. Governance state lives on the Hub

`hub/govern` owns the three stateful controls — budget ledger, permission
registry, audit log — and `hub/api/governance.go` exposes eleven endpoints over
them. Unconfigured, the ten endpoints that depend on that state answer `503
GOVERNANCE_UNCONFIGURED`; there is no permissive default. The eleventh, the
model-consistency check, reads an archive and calls a pure function, so it holds
no governance state and is not gated on one.

This does not move persona data to the Hub. The Hub stores a *permission tier*
and a *token count* per companion, neither of which is personality.

### 2. Budget refuses with an explicit downgrade, and never books a refusal

`BudgetLedger` accounts per companion per UTC month. The month comes from the
server clock, never from the request, so a caller cannot report spend into a
month it prefers.

Over the cap, the decision is `allowed: false` with `downgrade: "rule_engine"`
and a reason containing the spend, the cap and the request. The GDD forbids a
silent quality reduction, so the alternative is named rather than performed.

A refused request books nothing. Charging for a call that was downgraded to the
local rule engine would make the cap drift upward every time it was hit.

The default cap is `DefaultMonthlyTokens = 200_000` from ADR-0004, and any
refusal against the default says so — the number is an owner decision, not a GDD
citation, and the response must not imply otherwise.

A limit of zero is rejected. "Zero" and "unlimited" must not share a
representation, because the product has no unlimited mode.

### 3. Permissions are three tiers, per companion, deny-by-default

`read_only < restricted_write < high_impact`, compared by explicit rank so an
unknown tier string cannot silently sort above a known one.

Isolation is structural: every lookup is keyed by companion id, so a grant for
one companion cannot satisfy another's request. A companion with no configuration
is denied (`no_permission_configured`), not defaulted.

`high_impact` requires a **single-use, expiring approval for that specific
operation**. An approval for `delete_files` does not authorise
`execute_command`; a used or expired approval is never reused. A standing
approval would defeat the GDD rule that high-impact actions are confirmed at the
moment of execution.

Revocation keeps the tier and sets `revoked`, so the audit trail can still show
what was granted before it was withdrawn.

### 4. The audit log is hash-chained

Every entry carries `seq`, `prev_hash` and its own `sha256` over all descriptive
fields. `Verify` recomputes the chain and reports the first broken sequence
number, so editing, deleting or reordering an entry is detected rather than
trusted.

Every authorisation and every budget answer is appended, including refusals. An
audit log that records only successes cannot answer "what did this companion
try", which is the question the control exists to answer.

This is tamper *evidence*, not authentication: without a key pair a determined
writer could rebuild the whole chain. ADR-0005 records the same limitation for
archives; signing needs key management the Hub deliberately does not have.

### 5. Model consistency fails on "unknown"

`CheckModelVersions` compares the declared version of every non-official seat.
Three outcomes matter:

| Code | Meaning |
|---|---|
| `consistent` | every real seat declares the same version |
| `mixed_versions` | more than one version across real seats |
| `version_unknown` | at least one seat has no recorded version |

`version_unknown` is a **failure**, not a pass. A match whose version was never
recorded cannot be certified consistent, and treating absence as fine would make
the check useless exactly where it matters. Official backfills are excluded:
they are not a user's model choice.

`CheckRepriseModelChange` then enforces the GDD reprise rule: switching models
between an archive and its reprise must be declared as a `different_model`
reprise, not discovered afterwards.

### 6. The consent criterion is already met, and is not re-implemented

S5's "consent gate" is satisfied by ADR-0005's replay consent (all/majority/none,
fail-closed, reprise requires everyone) plus the high-impact approval above.
Building a second consent mechanism would create two answers to one question.

## Consequences

- **The five S5 controls now have one owner and one place to inspect.** A client
  can request, but only the Hub decides and only the Hub records.
- **The budget path is accounting, not billing.** No provider invoice is
  reconciled; spend is what the client reported. A dishonest client can under-
  report. Closing that needs per-call attestation or a Hub-side model proxy,
  neither of which exists.
- **Governance state is in memory in this ADR**, like the rest of the Hub. A
  restart resets permissions, spend and the audit log. ADR-0008 addresses this
  with an append-only audit log plus an atomic state snapshot, and is the
  authority on the persistence semantics; the in-memory descriptions above
  remain correct for the components themselves.
- **Endpoint authentication is still phase-1 only.** Anyone who can reach the
  Hub can set a permission tier. That is the same trust level as every other
  phase-1 endpoint; it is why the audit log matters, and why S5's "trust" work
  is not finished by this ADR.

## Not claimed done

- ~~No persistence for permissions, budget or audit — all reset on restart.~~
  Closed by ADR-0008: `HUB_GOVERNANCE_DIR` makes the three controls durable
  (append-only hash-chained log + atomic snapshot), refuses to start on a
  snapshot ahead of the log, and drops outstanding approvals on restart by
  design.
- No authentication or authorisation on the Hub's own endpoints, so the
  governance API is only as trustworthy as the network it sits on.
- No guided runtime setup flow (model, budget, permission onboarding). That is a
  client journey and the client is blocked on S3 assets (G6).
- No Hub-side model proxy, so token spend is self-reported.
- No signing of the audit chain or of archives.
