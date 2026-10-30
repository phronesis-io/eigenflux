# Qwen 3.8 Flash rollout inventory (2026-10-30)

Status: prepared, **not activated**. The owner requested online Qwen Flash paths
to use `qwen3.8-flash`. Configuration on disk is an inventory, not evidence of
the model loaded by a running process.

| Runtime | Setting | Observed value | Target |
|---|---|---|---|
| Backend | `LLM_MODEL` | `qwen3.6-flash` | `qwen3.8-flash` |
| Backend | `LLM_TRANSLATE_MODEL` | `qwen-flash` | `qwen3.8-flash` |
| Backend | `SAFETY_LLM_MODEL` | `qwen3.6-flash` | `qwen3.8-flash` |
| Backend | `UGC_SLOP_MODEL` | absent | `qwen3.8-flash` |
| Backend legacy setting | `LLM_TASK_DECOMPOSE_MODEL` | `qwen3.6-flash` | `qwen3.8-flash` |
| PGC | `LLM_MODEL` | `qwen3.8-flash` | `qwen3.8-flash` |
| PGC | `LLM_SECONDARY_MODEL` | `qwen-flash` | `qwen3.8-flash` |
| PGC | `OPS_LLM_MODEL` | `qwen-flash` | `qwen3.8-flash` |
| PGC | `PGC_LLM_JUDGE_MODEL` | `qwen-flash` | `qwen3.8-flash` |

Current backend source has no consumer of `LLM_TASK_DECOMPOSE_MODEL`; changing
that legacy setting alone changes no active path. PGC deduplicates primary and
secondary model names, so the target removes the distinct old-Qwen secondary
attempt. Its configured cross-provider fallback remains in place. Embeddings
remain `text-embedding-v4`; discovery LR is a separate ranking model.

Minimal calls to the actual configured endpoints succeeded for backend Responses
with low reasoning, backend safety Responses without reasoning, and PGC primary,
Ops and judge Chat Completions with thinking disabled. Each probe returned
`qwen3.8-flash` and nonempty output. These are transport checks, not task-quality,
tail-latency or delivery-effect acceptance.

Before activation, retain paired task regression and recent-context delivery
replay covering changed extraction and context-generation paths. The Slop
gate-only replay freezes preexisting extraction, embeddings and recall pools;
it **does not validate this global model replacement**. Keep the release draft
until the required evidence and review are complete.

Apply the backend changes as an explicit root operation to its managed runtime
configuration, with a protected backup and expected-old-value checks, followed
by the approved main deployer. Follow the PGC deployment workflow for its runtime
configuration and restart all long-running consumers. Do not edit production
source. Verify process-loaded model settings and real call model labels after
restart; checking `.env` alone is insufficient. Never print keys or full process
environments. Roll back the exact changed keys to the protected backup values
and use the same deployment workflow if contract or processing checks fail.

Record model activation and Slop activation as separate events. Compare the same
Agent cohort's delivered count, PGC/UGC and Need/Profile mix, daily positive events,
processing failures and fallback share on the next complete calendar day. The
actual check date is assigned when activation occurs, not at PR creation.
