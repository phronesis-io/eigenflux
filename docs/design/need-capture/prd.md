# Intent-linked Need capture

## Purpose

Humans manage Intent actions in the existing Console. Agents translate confirmed
Intent actions into structured NeedInputs. The platform owns Normalized Needs
for future search and recommendations. Humans do not edit a separate Need form.

## Requirements

- Preserve the existing Intent input fields: `watch_for`, `trigger_when`,
  `action_instruction`, `action_policy`, and `priority`.
- Capture only interpretations of owner-confirmed active Intent actions. Do not
  turn profiles, interests, historical conversations, or unrelated tasks into Needs.
- Link each input to its owner, Intent ID, and exact Intent version. Retain the
  source snapshot and submitted field values for offline analysis and reprocessing.
- Allow several inputs per Intent, such as separate information and service Needs.
- Use `broadcast | agent | commission`, `target.desc` (200 weighted characters),
  and `target.candidate_needs` (1–10 phrases). Constraints are a typed JSON object.
- Store platform normalization separately, including schema, normalizer, and
  taxonomy versions and retained historical outputs for sample reconstruction. An Agent cannot submit a canonical normalization result.
- Keep authorization to capture distinct from permission to contact someone,
  publish, buy, or change the Intent's action policy.

## Acceptance

1. Authenticated completed Agents can create and read only their own inputs.
2. Submission rejects missing, stale, inactive, and foreign Intent references.
3. Idempotent retries return the same record, including after the Intent changes;
   changed input under the same owner/key conflicts. Concurrent retries insert once.
4. JSON string values and array order round-trip; derived fields are rejected.
5. Database constraints prevent normalized records from linking to a different
   owner or source Intent version. Only one normalization per input can be active.
6. Eligibility requires a normalized input, active projection, and current active
   Intent version. Intent changes invalidate old projections without rewriting them.
7. Local PostgreSQL, HTTP, CLI, unit tests, and service builds verify the contract.

8. Successful online capture atomically creates an immediately usable basic
   projection with no taxonomy, offline corpus, LLM, embedding, or queue dependency.
9. Unknown vocabulary produces `unmapped` or `partial` coverage, never failed
   capture. Keep retrieval text and original candidate Need phrases at every coverage level.
10. Failed or stalled offline enrichment leaves the previous projection readable.
    Concurrent publishers use compare-and-swap to avoid overwriting newer results.

## Scope

This increment includes local schema, capture API/CLI, deterministic online
normalization, and an internal versioned enrichment publication boundary.
Vocabulary building, enrichment scheduling, embeddings, retrieval integration,
subscriptions, and deployment remain separate work. Offline enrichment improves
semantic coverage; online readiness never depends on it.
