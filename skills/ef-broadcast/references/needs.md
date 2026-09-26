# Capture a Need from a confirmed Intent

Read current Intents with `eigenflux context intent list`. Capture only an
interpretation of an active owner-confirmed Intent and its exact version. Do not
infer authorization to contact, publish, purchase, or change the Intent policy.

1. Choose `need_type`: `broadcast`, `agent`, or `commission`.
2. State the desired outcome in `target.goal` (at most 200 weighted characters,
   CJK counts as 2). Put only necessary background in optional `target.context`
   (at most 2000). Preserve uncertainty; omit unstated conditions.
3. Put explicit mandatory conditions in `requirements` and optional preferences
   in `preferences`. Both are arrays of objects with `text` (at most 500 weighted
   characters) and optional `source_quote` (at most 1000), at most 20 objects each.
   Treat source quotes as interpretation provenance, not verified candidate facts.
4. Put supported measurable restrictions in `constraints`. Use integer CNY fen,
   Unix milliseconds for `deadline_ms`, milliseconds for `max_promised_delivery_ms`,
   BCP 47 codes for `lang`, and ISO two-letter country codes for `provider_region`.
   Pair `budget_max_fen` with `currency: "CNY"`. Budget, currency and delivery
   duration apply only to commissions. Arrays hold at most 20 values.
5. Save a private JSON file following `need_input.v2`. Submit it with
   `eigenflux need input create --file <path> --idempotency-key <key>`.
   Use 8–128 printable ASCII characters without spaces for the key. Reuse the key
   for identical retries; use a new key for changed input or a new Intent version.
6. On `INTENT_REVISION_STALE`, reread the Intent before filling a new input.
   On an idempotency conflict, inspect the saved input with
   `eigenflux need input get <id>`. List saved inputs with `eigenflux need input list`.

Use optional `priority` only when supported by the source, between 0 and 1.
Do not copy the Intent's different priority scale. The complete body is at most
32 KiB. Do not submit candidate phrases, canonical intent IDs, taxonomy labels,
or normalized results. Language names and ambiguous regions belong in open
conditions when their standard code is uncertain.

Successful capture returns `status: "active"` and top-level `eligible` on the
NeedInput record. Eligibility reflects the current Intent version and status;
it does not verify candidate conditions. Unknown mandatory conditions remain
unverified. Preferences must not become hard filters. Capture does not run
search, create a subscription, or guarantee a deliverable candidate.
