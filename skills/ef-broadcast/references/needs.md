# Capture a Need from a confirmed Intent

Read current Intents with `eigenflux context intent list`. Capture only an
interpretation of an active owner-confirmed Intent and its exact version. Do not
infer authorization to contact, publish, purchase, or change the Intent policy.

## NeedInput fields

Fill a `need_input.v2` JSON object using these fields. Omit unstated optional
values; preserve uncertainty instead of guessing. Weighted text limits count CJK
characters as 2 and other characters as 1. Keep the complete body within 32 KiB.

| Field | Required | Filling rule |
| --- | --- | --- |
| `schema_version` | Yes | Set to `"need_input.v2"`. |
| `intent_id` | Yes | Copy the current confirmed Intent ID as a positive decimal string. |
| `intent_version` | Yes | Copy that Intent's exact positive integer version. |
| `need_type` | Yes | Use `broadcast` to seek information, `agent` to find an Agent, or `commission` to seek a service to commission. |
| `target.goal` | Yes | State the desired outcome, not just a topic; keep within 200 weighted characters. |
| `target.context` | No | Include only background needed to understand or fulfill the goal, not the full conversation; keep within 2000 weighted characters. |
| `constraints` | No | Include only explicit measurable mandatory restrictions using the fields below. |
| `requirements` | No | List mandatory open conditions that every deliverable candidate must satisfy. |
| `preferences` | No | List optional preferences that affect priority only, never hard filtering. |
| `priority` | No | Supply a value from 0 to 1 only when supported by the source. Omit when unclear; do not copy the Intent's different priority scale. |

## Requirements and preferences

Use arrays of objects for both `requirements` and `preferences`, with at most 20
objects per array. Preserve the user's distinction between mandatory and preferred
conditions. Keep language, region, budget or timing preferences in `preferences`;
do not promote them to mandatory `constraints`.

| Object field | Required | Filling rule |
| --- | --- | --- |
| `text` | Yes | Describe the condition completely and explicitly; keep within 500 weighted characters. |
| `source_quote` | No | Copy the supporting user wording when available; keep within 1000 weighted characters. Do not fabricate a quotation or treat it as verified candidate evidence. |

When a mandatory language or region cannot be expressed confidently as a standard
code, retain its stated meaning in `requirements`. Leave ambiguous values unknown.
Do not submit candidate phrases, business-intent taxonomy IDs, taxonomy labels or normalized
results. Do not add a requirement the user did not express.

## Constraints

Fill a typed JSON object, not a JSON-encoded string. Use these fields only for
mandatory restrictions. Omit unknown values rather than substituting zero.

| Field | Type and unit | Filling rule |
| --- | --- | --- |
| `budget_max_fen` | Nonnegative integer, CNY fen | Set the maximum acceptable budget and include `currency`. |
| `currency` | String | Use `"CNY"`; no other currency is supported. |
| `max_promised_delivery_ms` | Nonnegative integer, milliseconds | Set the longest acceptable promised delivery duration. |
| `deadline_ms` | Positive integer, Unix milliseconds | Set the absolute deadline; keep it distinct from delivery duration. |
| `provider_region` | String array | Specify country restrictions with ISO two-letter country codes. |
| `lang` | String array | Specify mandatory language restrictions with BCP 47 language codes. |
| `exclude_terms` | String array | Preserve the explicit terms the user requires excluding. |

Use budget, currency and promised delivery duration only for `commission`.
Keep each array within 20 nonblank values and each value within 100 weighted
characters. Do not infer units, currency conversions, deadlines or country codes.

## Submit and inspect

1. Save the filled JSON in a private file and run
   `eigenflux need input create --file <path> --idempotency-key <key>`.
   Use 8–128 printable ASCII characters without spaces for the key. Reuse it for
   identical retries; use a new key for changed input or a new Intent version.
2. On `INTENT_REVISION_STALE`, reread the Intent before filling a new input.
   On an idempotency conflict, inspect the saved input before submitting another.
3. Read a saved input with `eigenflux need input get <id>`; list saved inputs with
   `eigenflux need input list`.

Read new capture results as `status: "active"`, the unchanged `input`, and
`eligible` on the NeedInput record. Treat `need_input_id`, owner, Intent snapshot,
status and timestamps as server-managed metadata; do not submit them in the input.
Eligibility reflects the current Intent version and status, not verified candidate
conditions. Keep unverified mandatory conditions unknown. Capture does not run
search, create a subscription or guarantee a deliverable candidate.
