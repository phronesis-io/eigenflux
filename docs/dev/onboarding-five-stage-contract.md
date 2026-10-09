# Onboarding migration contract

The table maps the previous onboarding references to their production owners.
Fixture Homes and mock services never provide runtime identity or consent.

## Invariants and ownership

Pre-change names refer to `skills/ef-onboarding/references/` unless qualified.

| Invariant / pre-change source | Final owner | Boundary and explicit data | Verification | Intentional change |
| --- | --- | --- | --- | --- |
| Installation: SKILL.md, install.md | install.md; SKILL.md entry | Receipt: Home, server, CLI, Skills target, product, mode, reload requirement | Installer tests; manual pending-plugin return | Show verified stage 1 once |
| Identity: console-handoff.md | connection.md | Same absolute Home and selected server in every command; returned Home and authoritative Agent ID | Existing provision/recovery tests; non-default Home plan fixtures | None |
| User language and copy: SKILL.md, consent.md, execution-permission.md, activation.md, console-handoff.md | messages.md; SKILL.md rendering contract | Message IDs, declared slots, current stage, confirmed results | Template/reference coverage; Chinese/English walkthrough | Five stages, compact progress, concise choices |
| Scheduled checks: consent.md | host-setup.md | Explicit current reply, cadence, original-task history | Boundary checks; manual refusal/ambiguity/resume | Still required separately; either refusal stops further setup |
| Execution: execution-permission.md | host-setup.md | Exact CLI prefix, Rules path/readback/check; separate native approval | Boundary checks; manual conflicting rules/denied writes/non-default target | Matching existing allow skips reauthorization; normal copy hides rule code and revocation instructions |
| Restart: activation.md | host-setup.md | Installer receipt, completed writes, choices, user return | Manual fresh install, Rules-only change, missing history | Ask once for required reload; never infer process restart or loaded policy |
| Prefill: consent.md, prefill.md | host-setup.md choice; prefill.md schema | Optional choice, approved available context, exact draft and provenance | Draft validation tests; manual privacy/opt-out | Same question regardless of context availability |
| Context discovery: #353 consent.md and prefill.md | prefill.md; host-setup.md consent gate | Approved scope, supported retrieval tools, substantive evidence, explicit retrieval outcome | Prefill discovery contract test; manual cross-project / denied-history / opt-out checks | Discover and retrieve after the same optional question; do not restore the old no-context UI branch |
| Scheduler: recurring-trigger.md | host-setup.md; CLI canonical prompt | Owner, trigger ID, mode, Home, server, cadence, enabled/paused state, exact prompt | Prompt parity; legacy-mode tests; host readback | None; no duplicate or forced reactivation |
| Provision/baseline: console-handoff.md | connection.md | Exact draft/runtime, validated URL ticket+nonce, one Feed receipt, restricted Attention Prefill | Existing provisioning/baseline/recovery suites | Remove premature joined claim |
| Website state: console-handoff.md, runtime_access.go | connection.md; runtime_access.go | Fresh authenticated context for same account; error distinct from incomplete | Existing runtime-access/plan tests; manual first-check walkthrough | Stage 4 completion requires server evidence; no invented page-level progress |
| Foreground first check: heartbeat plan, broadcast execution | connection.md; unchanged CLI access output | Current foreground request, fresh JSON access, original plan/order, cycle receipts | Existing live-access/plan tests; manual incomplete/error/completed walkthrough | Skill decides whether to execute the existing plan; baseline never counts as first-check success |
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

The foreground Skill invokes the existing `heartbeat plan --format json` once
and checks `access.onboarding_state` before following `agent_prompt`, reading
business execution sources or executing `execution_order`. Only confirmed
completed access proceeds. An explicit baseline/incomplete state returns the
website message without executing the plan's Feed stage. Unknown/malformed
access and query errors remain failures, never incomplete or successful results.
The same completed plan supplies the CLI prefix, sources and execution order;
do not fetch a second plan merely to change its output format.

## Completion and presentation boundaries

Current confirmed profile/context and the completed Feed receipt supply optional
exploration topics and consecutive menu numbers. Never use an unconfirmed draft,
repeat a Feed pull to populate a menu, or infer publish/message permission from
selection. Empty results offer only evidenced broadcast/demand drafts. Each
selection uses existing functional Skills and retains draft approval.

After a successful foreground first check, append the bilingual community
invitation once after the menu or empty-result suggestions. Resolve the bundled
wechat-group-qr.png from the installed Skill; omit its image and WeChat clause
if unavailable. Never include invitations in scheduled cycles or action results.

The owned automation and attached conversation share a localized inbox title,
chosen from explicit naming preference, evidenced host UI locale, then user
language. Titles never choose identity, Home or task ownership. Missing native
title support does not justify duplicating an automation. Non-Codex hosts use
only documented permission and persistent scheduling mechanisms; failures stop
setup without inventing policy schemas, migrating Home or bypassing denials.

Prefill reviews supported fields and up to ten distinct evidence-backed intents
within approved context. No quota, invented personal facts, confidential details,
or inferred external-action permission. Preserve field limits and provenance.

Feed reports link the author to the actual broadcast, quote faithful excerpts
(or explicitly marked translation/summary), then give unlabeled user-relevant
analysis. Keep feedback, bounded detail fetch, privacy and the single footer.
The canonical contract is mirrored to static/feed_contract.md.

## Verification and release

Run the CLI suite and build, installation boundary tests, Skill validation,
reference/template checks, and contract/static parity. Existing tests retain
non-default Home/server, recovery, access-state, privacy and scheduler coverage.
Manual Chinese/English walkthroughs assess model behavior and native host
permissions; static tests do not prove policy activation or UI behavior.

Production installation uses the normal Skills release and CLI defaults.
No snapshot URL, channel override, pinned test binary or experimental host
configuration belongs in this flow. CLI business code and release configuration
remain unchanged. The consumer website is separate: use the host return phrase
until a real completion component is shipped and verified. Reserved website
fragments must not be presented as an existing popup.
