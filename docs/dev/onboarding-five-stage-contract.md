# Onboarding migration contract

Recorded before implementation against `3a3612d1`. All final owners below are
production behavior. Isolated test homes, mock servers, and fixture manifests
are test scaffolding, never production state or authorization evidence.

## Invariants and ownership

Pre-change names refer to `skills/ef-onboarding/references/` unless qualified.

| Invariant / pre-change source | Final owner | Boundary and explicit data | Verification | Intentional change |
| --- | --- | --- | --- | --- |
| Installation: SKILL.md, install.md | install.md; SKILL.md entry | Receipt: Home, server, CLI, Skills target, product, mode, reload requirement | Installer tests; manual pending-plugin return | Show verified stage 1 once |
| Identity: console-handoff.md | connection.md | Same absolute Home and selected server in every command; returned Home and authoritative Agent ID | Existing provision/recovery tests; non-default first-check fixture | None |
| User language and copy: SKILL.md, consent.md, execution-permission.md, activation.md, console-handoff.md | messages.md; SKILL.md rendering contract | Message IDs, declared slots, current stage, confirmed results | Template/reference coverage; Chinese/English walkthrough | Five stages, compact progress, concise choices |
| Scheduled checks: consent.md | host-setup.md | Explicit current reply, cadence, original-task history | Boundary checks; manual refusal/ambiguity/resume | Still required separately; either refusal stops further setup |
| Execution: execution-permission.md | host-setup.md | Exact CLI prefix, Rules path/readback/check; separate native approval | Boundary checks; manual conflicting rules/denied writes/non-default target | Matching existing allow skips reauthorization; normal copy hides rule code and revocation instructions |
| Restart: activation.md | host-setup.md | Installer receipt, completed writes, choices, user return | Manual fresh install, Rules-only change, missing history | Ask once for required reload; never infer process restart or loaded policy |
| Prefill: consent.md, prefill.md | host-setup.md choice; prefill.md schema | Optional choice, approved available context, exact draft and provenance | Draft validation tests; manual privacy/opt-out | Same question regardless of context availability |
| Scheduler: recurring-trigger.md | host-setup.md; CLI canonical prompt | Owner, trigger ID, mode, Home, server, cadence, enabled/paused state, exact prompt | Prompt parity; legacy-mode tests; host readback | None; no duplicate or forced reactivation |
| Provision/baseline: console-handoff.md | connection.md | Exact draft/runtime, validated URL ticket+nonce, one Feed receipt, restricted Attention Prefill | Existing provisioning/baseline/recovery suites | Remove premature joined claim |
| Website state: console-handoff.md, runtime_access.go | connection.md; runtime_access.go | Fresh authenticated context for same account; error distinct from incomplete | First-check completed/incomplete/query-error tests | Stage 4 completion requires server evidence; no invented page-level progress |
| Foreground first check: heartbeat plan, broadcast execution | CLI first-check plan; connection.md | Current user request, live access, stage order, confirmed cycle receipts | CLI non-default target tests; incomplete emits no stages; source isolation | Add foreground entry; baseline never counts as first-check success |
| Action exploration: existing functional Skills | connection.md routes; messages.md menu | Completed manual cycle; selected action; action-specific consent | Manual success/empty/failure/retry/menu paths | Optional menu once; no duplicate Feed option or sixth stage |
| Recurring heartbeat: heartbeat.go, heartbeat-execution.md | Same owners | Actual current trigger, unchanged launcher/prompt, required host result | Scheduler parity; normal plan excludes onboarding sources | Never attach foreground copy/menu to recurring jobs |

Execution permission includes reads and writes, publication and replies, across
tasks sharing the host configuration. Product security settings remain separate
behavioral requirements. A prefix rule does not enforce a Home/server boundary
when trailing flags override the target. Preserve unrelated and stricter Rules;
never substitute a shell/interpreter prefix or change sandbox/network policy.

## Explicit handoffs and loading

`SKILL.md` routes the current request and declares shared gates. Load
`messages.md` for user responses; `host-setup.md` for consent, restart or scheduler
work; `prefill.md` after the profile choice; `connection.md` for provision,
handoff or foreground first check. Each visible template has one canonical
Chinese and English body. Ordinary heartbeat plans exclude onboarding sources.

The five stages are components, permissions, profile preparation, website setup,
and first check. The manual profile path completes stage 3 by choosing where to
fill the form; it never claims a draft exists. Continuation or a saved boolean
does not grant missing permission. Resume from confirmed results without
repeating successful writes, identity creation, provisioning, polling or trigger
creation. A user return does not prove the host reloaded policy.

CLI 0.0.56 adds `heartbeat plan --first-check`. It uses the existing live access
check and emits `purpose: first_check`. An incomplete website state has no
executable stages and `wake_on_empty: false`; completed state keeps the normal
cycle order and includes the foreground connection/message sources. Query
failures return errors. A plan is not a completed cycle. The recurring launcher
never includes the new flag, and historical chat text cannot trigger this mode.
Legacy credentials retain their existing authenticated-profile access semantics.

## Website delivery boundary

The consumer website is outside this repository. Keep a concrete return phrase
in the host handoff until the website completion component is shipped. Never
claim that a nonexistent popup will guide the user. A future website component
must wait for email verification and all four settings steps, provide a copyable
first-check request for the original conversation, and remain recoverable after
dismissal. Copying is not execution or check completion. Only then switch to the
website-guided host variant; never show both variants.

## Acceptance and cleanup

Run the full CLI suite, CLI build, Skill validation, migrated-reference checks,
and non-default Home/server integration tests. Preserve existing identity,
recovery, baseline, runtime reporting, and scheduler tests. Manually review both
locales and each branch: refusal, no/ambiguous reply, Prefill/manual, matching
rule, denied write, restart return, lost history, incomplete website, failed
query, empty/useful/failed cycle, retry, optional actions, scheduled run carrying
old onboarding context. Static tests do not establish live model adherence or
loaded host policy; report that limit explicitly.

Minimum compatible CLI becomes 0.0.56. Publish the reviewed CLI and signed Skills
through normal workflows before public installation verification. No temporary
URLs, overlays, or local live-Agent installations belong in this change.

Compare the pre-change base, intended contract, and final implementation before
delivery. For every deleted reference, verify retained rules have a canonical
owner, all consumers are updated, and tests cover the retained boundaries.
