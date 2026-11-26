# ADR-0006 — Post-match Agent Card projection (G11 closure, not a broadcast)

- **Status:** Accepted
- **Date:** 2026-11-07
- **Relates to:** ADR-0001 decisions 2 and 3, ADR-0005, contracts §7 and §9

## Context

ADR-0001 decision 2 requires post-match social propagation to go back through
**both** EigenFlux surfaces: PM and the `profile/fields` API. The PM half has
worked since S1. The profile half never ran, tracked as G11 and deferred twice
("belongs to S4, where the owning client holds the Agent token").

Closing it is not a matter of calling the endpoint. Reading
`pkg/agentcard/registry.go` shows the entire editable registry is
**owner-authored prose**: `agent_name`, `agent_description`,
`human_description`, `working_languages`, `seeking`, `offering`, and a set of
private fields. There is no match, companion or FluxSpace field at all. So a
naive "write the recap into the card" would do one of two bad things:

- overwrite a description the human wrote, silently destroying their content; or
- write a sentence that is accurate today and becomes a stale lie after the next
  match.

Neither is acceptable, and inventing a field name would simply be rejected — or
worse, accepted by a lenient server and never projected.

## Decision

### 1. Opt-in, not automatic

Projection runs only when `HUB_CARD_PROJECTION_FIELDS` names the fields. Unset
(the default) means the Hub never touches an owner's public card, and every
delivery record leaves `card_projection` **nil** rather than claiming a write
happened.

### 2. Blank-fill only, never replace

`GatewayPort.ProjectAgentCardFields` reads `GET .../card/refresh-context`,
inspects each candidate's `current_value`, and refuses the field if it holds
anything (`owner_content_present`). Only blank, absent or null values are
written. Order of refusals:

| Reason | Meaning |
|---|---|
| `not_projectable_by_hub` | not one of the two descriptor fields the Hub may set |
| `empty_value` | the caller supplied nothing |
| `owner_content_present` | the owner already wrote text; the Hub leaves it alone |

Only `agent_description` and `human_description` are projectable. `agent_name`
is excluded (identity, not a recap), and every `*_status` / `seeking` /
`offering` field is excluded because it states the owner's intent, not the
Hub's observed fact.

### 3. The projected sentence is dated and factual

```
FluxSpace: completed a 5-round deduction theatre on 2026-11-07 (outcome: partial).
```

It contains only what the Hub itself produced: the round count, the date and the
outcome. Personality, memories, relationships, metrics and motives never appear —
not because they are filtered out, but because they are never candidates. A test
asserts that no key containing `personality`, `memory`, `relationship`, `risk` or
`empathy` can reach the write body.

### 4. A failed projection never costs the message

The follow-up PM is the primary social artefact. If the card write fails (401,
409 optimistic-lock conflict, rate limit, unconfigured port), the delivery
succeeds and the failure is recorded in `card_projection.error`. Losing the
recap because a profile patch bounced would be the worse outcome.

### 5. The capability is a separate interface

`services.CardProjector` is not part of `EigenFluxPort`. Existing port
implementations and test doubles keep compiling; the sender type-asserts and
records `port does not implement Agent Card projection` when it is missing.

## Consequences

- **G11 is closed.** Both surfaces ADR-0001 decision 2 names are now exercised,
  and the delivery record makes the difference between "wrote", "skipped" and
  "failed" observable instead of implied.
- **Default behaviour is still "no write".** An operator must opt in, so an
  existing deployment cannot start mutating user cards because it upgraded.
- **A resumed match does not refresh the sentence.** Because occupied fields are
  never replaced, the first projected line stays. That is a deliberate
  trade-off: an accurate dated line is better than a self-updating one that
  overwrites whatever the human later wrote. Refreshing it needs a field the
  Hub owns, which does not exist.
- **The product gap remains, and is now precisely stated:** the Agent Card needs
  a FluxSpace-owned field (for example `fluxspace_summary`, `Public: true`,
  system-written, not owner-editable) before projection can be both accurate and
  non-destructive. Adding it is an upstream schema change in
  `pkg/agentcard/registry.go` plus its migration, and it is out of scope for the
  Hub module.

## Not claimed done

- No new Agent Card field was added; this ADR cannot make one exist.
- No projection of persona, memories, relationships, metrics or achievements.
  Those are refused by construction, not by policy alone.
- Projection is unverified against a live EigenFlux stack (G10): the request
  shape is asserted against a local HTTP server, and `refresh-context` /
  `profile/fields` are verified statically from `api/agentcard/handlers.go`.
