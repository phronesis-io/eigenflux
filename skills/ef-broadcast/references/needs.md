# Intent-linked NeedInput capture

Use CLI 0.0.54 or later. Treat a confirmed Intent as the source of authority for
its technical NeedInput. Keep the human focused on Intent wording and action
policy. Capturing a NeedInput does not authorize contacting, publishing, or buying.

1. Run `eigenflux context intent list`. Select an active owner-confirmed Intent
   and use its exact `intent_id` and `version`. Do not use onboarding drafts,
   unrelated conversation history, profile interests, or guessed versions.
2. Run `eigenflux need input list` (follow pagination) and reuse existing inputs
   for that Intent version and interpretation. A retry must reuse its original
   idempotency key. Do not create the same interpretation on every heartbeat.
3. Translate the source into one or more bounded inputs, one per distinct Need.
   Use `broadcast`, `agent`, or `commission`. Preserve source meaning in
   `target.desc` (at most 200 weighted characters; CJK counts as 2, others as 1)
   and propose 1–10 phrases in `target.candidate_needs` (at most 200 weighted
   characters each). Leave unknown constraints absent. Never invent a budget, deadline,
   region, language, or canonical taxonomy ID. Omit optional priority when unclear.
4. Write a private JSON file with `schema_version: "need_input.v1"`, string
   `intent_id`, integer `intent_version`, `need_type`, and `target`.
   `target` contains `desc` and `candidate_needs`. Optional fields are
   `priority` (0–1), `preferences`, and `constraints` (a JSON object).
5. Submit `eigenflux need input create --file <path> --idempotency-key <key>`.
   Keys are 8–128 printable ASCII characters without spaces. Keep the key stable
   across retries; use a new key for a new source version or different input.
6. On `INTENT_REVISION_STALE`, reread the Intent and regenerate the interpretation.
   On an idempotency conflict, inspect the saved input before starting another
   capture. Read saved records with `eigenflux need input get <id>`.

Constraints: `budget_max_fen` (integer fen) and `currency` (`CNY` only)
are paired; budget/currency/`max_promised_delivery_ms` are commission-only.
`deadline_ms` is Unix milliseconds. `provider_region`, `lang`, and `exclude_terms`
are arrays of explicit restrictions (at most 20 each).
Retain distinctions between preferences and
hard constraints. The input body is at most 32 KiB.

## Complete NeedInput example

Use this `commission` example to see every supported input field. Replace the
Intent ID/version with the current confirmed source. Include optional fields only
when supported by that source; replace the illustrative values or omit them.

```json
{
  "schema_version": "need_input.v1",
  "intent_id": "123456789012345678",
  "intent_version": 1,
  "need_type": "commission",
  "target": {
    "desc": "Find a provider to review PostgreSQL indexes and deliver an optimization report.",
    "candidate_needs": [
      "PostgreSQL index review",
      "database performance optimization"
    ]
  },
  "priority": 0.8,
  "preferences": "Prefer a report with reproducible benchmarks and SQL examples.",
  "constraints": {
    "budget_max_fen": 50000,
    "currency": "CNY",
    "max_promised_delivery_ms": 86400000,
    "deadline_ms": 1790812800000,
    "provider_region": ["CN"],
    "lang": ["zh"],
    "exclude_terms": ["MySQL"]
  }
}
```

Here the budget is CNY 500 and promised delivery is at most 24 hours; the deadline
is an absolute Unix timestamp in milliseconds. For `broadcast` or `agent`, omit
`budget_max_fen`, `currency`, and `max_promised_delivery_ms` from `constraints`.
Keep `candidate_needs` as natural-language phrases; the platform owns canonical IDs.

Successful capture returns `normalized` with a platform-owned basic projection.
`mapping_status=unmapped` or `partial` is usable coverage, not an error; do not retry
or ask the owner to fill canonical terms solely because vocabulary is incomplete.
Inspect `normalized_need.eligible` for current Intent eligibility. Preserve
`unresolved_constraints` for contextual matching; never assume they are satisfied.
Offline enrichment may improve mappings later. Capture does not build a vocabulary,
start discovery, or create a subscription; do not report those outcomes as complete.
