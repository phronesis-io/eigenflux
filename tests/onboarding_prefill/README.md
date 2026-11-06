# Onboarding prefill behavioral regressions

Run both cases whenever changing onboarding draft-generation instructions.
Use the current checkout's Skill, not installed or CDN copies. These fixtures
are synthetic Chinese user contexts and are excluded from the Skills bundle.
The second case adds an explicit ongoing paid-work goal to the first case.

From the repository root:

```sh
python3 tests/onboarding_prefill/run.py prepare --output build/onboarding-prefill/run-1
```

Give each generated `*.prompt.txt` to a separate fresh Agent with no inherited
conversation history. Do not send `cases.json`, this README, expected outcomes,
prior outputs, or the suspected regression to the generating Agents. The
embedded instructions and context are sufficient; disable unrelated retrieval
and external actions. Save each raw JSON response as `<case-id>.draft.json` in
the same run directory. Run each case independently, without reusing a session.
Use new output directories for subsequent runs.

```sh
python3 tests/onboarding_prefill/run.py check --output build/onboarding-prefill/run-1
```

The checker rejects missing/malformed drafts, stale source fingerprints,
discarded capability fields, unexpected intents for capabilities-only context,
missing intents for the ongoing goal, and unauthorized action policies. It
does not generate model responses or establish semantic correctness.

Review the generated drafts against these behavioral criteria:

| Case | Required behavior |
| --- | --- |
| `capabilities_only` | Preserve supported capabilities in Agent description and offering; leave intents empty. Completed commissions alone do not establish a continuing goal. |
| `ongoing_commissions` | Monitor real customer needs or paid commission opportunities matching the supported services. Analyze and present opportunities for user confirmation before contact. Merge overlapping monitoring needs; no target item count. |

Record the generator host/model when observable, source hashes, run directory,
case results, semantic rationale, and overall verdict in the change's validation
evidence. Retain the raw drafts and `checks.json` for review. Report a pass only
after both structural checks and semantic review pass. Do not substitute
wording/keyword assertions, saved expected drafts, or checker unit tests for
fresh Agent generations. A single paired run checks the observed behavior,
not stability across every model or repeated sampling.

To verify the checker itself without any model or account access:

```sh
python3 -m unittest discover -s tests/onboarding_prefill -p 'test_*.py'
```
