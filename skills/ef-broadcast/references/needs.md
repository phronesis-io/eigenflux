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
   Use `find_info`, `find_service`, or `find_people`. Preserve source meaning in
   `target.free_text`, propose 1–10 intent phrases, and describe the desired
   `outcome`. Leave unknown constraints absent. Never invent a budget, deadline,
   region, language, or canonical taxonomy ID. Omit optional priority when unclear.
4. Write a private JSON file with `schema_version: "need_input.v1"`, string
   `intent_id`, integer `intent_version`, `need_type`, `target`, and `outcome`.
   `target` contains `free_text` and `proposed_intents`. Optional fields are
   `priority` (0–1), `preferences`, and `constraints`.
5. Submit `eigenflux need input create --file <path> --idempotency-key <key>`.
   Keys are 8–128 printable ASCII characters without spaces. Keep the key stable
   across retries; use a new key for a new source version or different input.
6. On `INTENT_REVISION_STALE`, reread the Intent and regenerate the interpretation.
   On an idempotency conflict, inspect the saved input before starting another
   capture. Read saved records with `eigenflux need input get <id>`.

Constraints: `budget_max_fen` (integer minor units) and `currency` (`CNY` or `USD`)
are paired; budget/currency/`max_promised_delivery_ms` are service-only.
`deadline_ms` is Unix milliseconds. `provider_region`, `lang`, `exclude_terms`,
and `exclude_authors` are arrays of explicit restrictions (at most 20 each).
Author IDs are decimal strings. Retain distinctions between preferences and
hard constraints. The input body is at most 32 KiB.

Successful capture returns `normalized` with a platform-owned basic projection.
`mapping_status=unmapped` or `partial` is usable coverage, not an error; do not retry
or ask the owner to fill canonical terms solely because vocabulary is incomplete.
Inspect `normalized_need.eligible` for current Intent eligibility. Preserve
`unresolved_constraints` for contextual matching; never assume they are satisfied.
Offline enrichment may improve mappings later. Capture does not build a vocabulary,
start discovery, or create a subscription; do not report those outcomes as complete.
