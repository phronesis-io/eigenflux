# Capture a Need from a confirmed Intent

Read current Intents with `eigenflux context intent list`. Capture only an
interpretation of an active owner-confirmed Intent and its exact version. Do not
infer authorization to contact, publish, purchase, or change the Intent policy.

NeedInput is internal: prepare and submit it within the Agent workflow. Do not ask
the owner to fill this form or choose Need IDs. Search accepts a query;
recommendations select eligible Needs on the platform.

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
Submit only the fields in this contract. Do not add a requirement the user did not express.

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

## Automatic maintenance

Run this pass after an Intent add/update and once per completed-onboarding
heartbeat, including the first cycle after upgrade. Use the heartbeat plan's
exact CLI prefix. Keep Need IDs, review state, and routine errors internal.

1. Run `eigenflux need capture pending --limit 2 --format json`. Review only
   the returned versions; do not drain `has_more` in this cycle. Stop starting
   another review after 60 seconds. No pending Intents means this pass is done.
2. Read each returned confirmed Intent, including `watch_for`, `trigger_when`,
   `action_instruction`, and `action_policy`. Inspect `existing_inputs` with
   `eigenflux need input get <need_input_id>` when necessary. Cover every type
   supported by the Intent; reuse existing types and prepare at most one new
   input per missing type. Do not force all three types or infer a buying or
   contact intent from a general topic. Preserve conditions without treating
   `when` or an action instruction as unrelated query keywords.
3. Use the host's file-writing tool to save a private JSON object containing
   `intent_id`, `intent_version`, `outcome`, and `inputs`. For `captured`, put the
   missing `need_input.v2` objects in `inputs`; use an empty array if existing
   inputs cover the Intent. For an Intent with no meaningful discovery need
   and no existing inputs, use `no_need`, an empty array, and a short `reason`.
   Do not classify uncertainty or a technical failure as `no_need`.
4. Run `eigenflux need capture complete --file <path> --format json`. The server
   saves the inputs and completion together. Retain the exact file for an
   uncertain retry; delete it after confirmed success. Never submit these same
   inputs separately through `need input create` during this pass.
5. On a timeout, retry the identical file once. On `INTENT_REVISION_STALE`,
   `IDEMPOTENCY_CONFLICT`, or `kind_already_captured_refresh_pending`, discard
   the stale draft and read pending work again within the same cycle budget.
   Another Home may already have completed it. For other failures, leave work
   pending for the next heartbeat and continue later safe stages. Handle
   authentication errors through the existing auth flow.

Completion belongs to the exact Intent version and is shared across Homes.
Intent edits automatically make the new version pending. Existing captures are
reviewed once rather than duplicated. `no_need` suppresses repeat work only for
that version. Do not ask the owner to approve this internal representation or
change the source Intent, authorization, or policy as part of maintenance.
