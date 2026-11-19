# ADR-0005 — Replay archive, viewing modes and consent boundaries

- **Status:** Accepted
- **Date:** 2026-11-07
- **Relates to:** ADR-0001 (FluxSpace boundaries), ADR-0003 (relaxed targets), ADR-0004 (signed design decisions)

## Context

S4's exit criterion is *"SHA-256 archive verification; six viewing modes;
all-participant consent for public replay"*. The GDD specifies the replay
document, the six modes and the consent policy, but leaves four engineering
questions open, and each of them can be answered in a way that quietly
overstates what the product can prove:

1. **Where does the archive live?** The GDD says archives are *optionally*
   synced to the Hub (`design/game-brief.md`, EigenFlux integration table), and
   also that the Hub provides replay archives. Personality, memories and
   relationships are client-side and never synced (ADR-0001 decision 3).
2. **What happens to fields the Hub does not own?** The GDD archive config asks
   for `persona_id`, `model_version` and a full `persona_snapshot` per seat. The
   Hub knows the six personality dimensions from the seat claim and nothing
   else.
3. **How strict is consent?** `replay_consent` can be `all`, `majority` or
   `none`, and the invitation may not specify one at all. A permissive default
   would publish matches their owners never agreed to.
4. **What counts as a "key moment"?** An auto-marked highlight can be derived
   from data or invented by a model; only one of those is auditable.

## Decision

### 1. The Hub owns the archive; publication state lives outside it

The Hub already owns the only authoritative record of a match: its referee
produced it and its event log is what the spectators saw. `hub/archive` freezes
that log rather than accepting a client-supplied copy.

Immutability is enforced structurally, not by convention: the archive is a
value whose hashes cover `{version, session_id, config, events, settlement}`,
and publication plus consent are held in `archive.Store`. Publishing therefore
cannot change any hashed byte, and `Verify` recomputes rather than trusting the
recorded values.

### 2. Missing fields are reported, never filled in

`integrity.complete` is false and `integrity.missing_fields` lists each
`config.seats[n].persona_id` / `model_version` / `persona_snapshot` the Hub could
not capture. A caller may supply them at archive time; the six-dimension
`PersonaSnapshot` from the seat claim is always captured.

Rejected alternative: emitting a plausible `persona_id` from the companion id,
or a `model_version` of `"unknown"`. Both make the archive look complete while
being wrong, and the same-spec comparison would then compare fabricated
metadata.

### 3. Consent fails closed, and a reprise is stricter than publication

- An unspecified `replay_consent` is treated as `all`.
- An unrecognised policy is an error, not a permissive default.
- `majority` means strictly more than half, so a two-seat match still needs both
  owners. A "half" reading would let one owner publish a match the other refused.
- Official backfill seats are excluded from signing: they have no owner.
- A same-spec reprise requires **every** participant regardless of the
  publication policy, because the GDD ties reprises to the original owners'
  consent rather than to publication.

Refusals are `403` with `REPLAY_CONSENT_INCOMPLETE` /
`REPRISE_CONSENT_INCOMPLETE` and a body naming the missing participants, so a
denial is actionable.

### 4. Key moments are derived from measurable signals

A moment is a branch event (with its declared metric effects), one of the
highest-magnitude metric swings (sum of absolute deltas), or the terminal
outcome. The projection is deterministic: the same archive always yields the
same moments, and every moment carries the numbers that justified it. No model
call, no editorial judgement.

### 5. Same-spec comparison requires a genuinely identical configuration

`CompareSameSpec` rejects the pair unless the skeleton hashes match and the
social contract, seat count, per-seat companion and round count are identical.
Divergence is the first round in which any seat's `action_id` differs, reported
with the specific seat. Comparing two different games and calling the difference
"personality" would be the exact mistake this feature exists to avoid.

## Consequences

- **Six viewing modes are backend projections over one event stream.** They
  cannot disagree with each other, and adding a mode costs no storage.
- **S4's exit criterion is met at the Hub surface, not in the UI.** The Flutter
  client has no replay screens yet; S3 is still blocked on Rive/2.5D assets
  (G6, ADR-0004). S4 is therefore *backend complete*, and the product-level
  "watch a replay" claim is not made.
- **Archives are in memory, like the rest of the Hub.** The verification path is
  written so a disk or object-store backend can replace the store without
  changing callers, but persistence is not implemented and restart loses
  archives.
- **G11 remains open.** Post-match Agent Card projection (profile/fields) was
  deferred to S4 by ADR-0002 and is *still* not called; `PublishAgentCardFields`
  exists and is shape-verified, but no follow-up or archive path invokes it.
- **Consent is only as good as the identity of the caller.** Phase-1 Hub
  endpoints are unauthenticated except for the spectator token, so a client can
  claim any `owner_id`. This is the same trust level as every other phase-1
  endpoint and is recorded here rather than implied away; closing it belongs to
  S5's permission work.

## Not claimed done

- No persistence, no object storage, no retention policy.
- No replay UI, no client rendering, no animation.
- No `different_model` or `different_persona` reprise variants: both need a live
  model adapter, which ADR-0004 G4 already registers as a labelled deviation.
- No signature over archives. SHA-256 detects accidental or naive tampering; it
  does not authenticate the producer. A signed archive needs key management that
  the Hub deliberately does not have yet.
