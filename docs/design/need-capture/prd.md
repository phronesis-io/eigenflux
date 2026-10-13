# Intent-linked Need capture

## Contract

Agents translate owner-confirmed active Intents into complete NeedInputs. The
platform validates structure, ranges, codes, ownership and source versions, then
saves the immutable input and Intent snapshot. The platform does not reinterpret
the goal or create a second authoritative semantic representation.

Use `need_input.v2`: `need_type` is `broadcast | agent | commission`, `target.goal`
is the intended outcome, and optional `target.context` contains necessary
background. Explicit measurable restrictions belong in `constraints`; mandatory
open conditions belong in `requirements`; optional preferences belong in
`preferences`. Each open condition has `text` and optional `source_quote`.

Do not require candidate phrases, business-intent taxonomies or canonical IDs.
Omitted values stay unknown. Source quotes explain the Agent's interpretation;
they do not independently prove that a candidate satisfies a requirement.

## Acceptance

- Validate before writing; preserve submitted field values, array order, input
  schema version, exact Intent version, source snapshot and retry hash.
- Reject foreign, missing, stale or inactive Intent references on new captures.
- Same-owner identical retries return the original record even after an Intent
  edit; changed payloads under the same key conflict. Concurrent retries insert once.
- A successful write creates one active NeedInput without a projection, LLM,
  vocabulary, embedding or queue dependency. Get/list report current eligibility.
- New v2 input uses standard language/country codes and integer amount/time units.
  Requirements and preferences remain distinct throughout storage and reads.
- Retain v1 input compatibility and historical normalization rows without creating
  new normalization records. Never rewrite historical source JSON or retry hashes.
- Current eligibility follows input lifecycle and the current active Intent version.
- Verify schema, PostgreSQL, HTTP, CLI, builds and affected regression tests.

## Scope

This change covers main-branch capture, storage, read responses, protocol schemas,
CLI guidance and Agent instructions. The separate `codex/need-search-mvp` branch,
retrieval integration, matching, embedding caches and deployment are outside scope.
Future matching must report satisfied/conflict/unknown, keep unverified mandatory
conditions visible, and never use preferences as hard filters.
