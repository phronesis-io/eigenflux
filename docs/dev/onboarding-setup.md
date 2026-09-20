# First-time connection contract

The installation entry owns host installation. `ef-onboarding` owns the
foreground connection flow. Host tools own execution approvals and scheduling;
the CLI and server retain identity, authorization, and network-state ownership.

| Invariant | Source / final owner | Boundary | Verification | Intentional change |
| --- | --- | --- | --- | --- |
| Install only the invoking host; preserve other hosts, existing configuration, and explicit opt-outs | `skills/install.md`, `static/install.sh` | Installer to host plugin | Isolated installer tests with a non-default Home, installed/missing/failed plugin, and opt-out | Explain official plugin scope; no additional conversational install consent |
| Preserve exact Home, selected server, identity, referral, and existing accounts | Installation entry, CLI auth/config, Console handoff | Install, restart, scheduler, provision | Existing Home/referral integration tests and CLI suite | None |
| Choice presentation preserves explicit consent and full disclosure | Main onboarding Skill template contract / same owner | Native question tool to submitted user answer | Existing template and authorization contracts; manual card, dismissal, free-text, and unavailable-tool checks | Prefer request_user_input_async cards; one decision at a time; no submission means no authorization |
| Scheduling consent is distinct from command-rule consent | `ef-onboarding/references/consent.md`, `execution-permission.md` | User choice to host mutation | Skill contract tests and manual refusal scenarios | Two separate required decisions; either refusal pauses new onboarding |
| Prefill context access remains optional, bounded, and independently authorized | `consent.md`, `prefill.md` | Context retrieval to review-only Console draft | Existing draft/provenance tests; manual opt-out and ambiguous-answer scenarios | Ask after required setup activation; generic continuation never grants Prefill |
| Rule writes preserve other rules and require approval for the concrete scope | `execution-permission.md` | User consent to host execution policy | Rule checker with non-default Home/server; conflict and write-denial scenarios | Refusal, conflict, or unverified activation blocks new connection; no sandbox-policy fallback |
| Installation does not change Codex sandbox policy | `static/install.sh`; command permission owned by `execution-permission.md` | Installer to user config | Full installer fixture checks config bytes and absence of alternate policy advice | Remove the redundant sandbox configuration route; keep existing user policy untouched |
| Plugin and rule activation share one restart when needed | Installation entry, `activation.md` | Installer output and confirmed user choices to resumed original task | Structured installer result tests; manual fresh-process continuation | Defer Codex restart until both are prepared; retain native approvals |
| Resume does not invent consent or replay mutations | `activation.md`, authoritative tool results in the original task | Process restart to next setup stage | Manual original-task, missing-context, failed-activation scenarios; existing identity tests | Reuse established choices and verified operations; ask only for missing authority |
| Exactly one verified active trigger precedes provision | `recurring-trigger.md` | Activated setup to scheduler to CLI | Existing scheduler prompt parity and CLI integration tests; manual persistence checks | Move trigger creation after both required gates and activation; preserve disabled triggers |
| Baseline, privacy limits, Console confirmation, output language, and external-action restrictions | Existing `prefill.md`, `console-handoff.md`, CLI/server | Setup to live network and human Console | Existing CLI contract/integration suite | None |

The removed installer sandbox helper has no remaining production responsibility:
explicit command permission lives in the foreground onboarding reference. Its
unused duplicate plugin installer is removed; the verified official-marketplace
implementation in `setup_agents` remains the sole installer.

Manual acceptance covers: refusing either required choice; accepting each then
declining Prefill; approving scoped Prefill; one combined restart after a fresh
plugin install; Rules-only restart for an already active plugin; already active
matching Rules without duplicate writes; resume with missing conversation
evidence; host rejection and conflicting rules; and repeated setup without a
second identity or recurring trigger. Check language and actual approval behavior
in Codex; static Skill checks do not establish model adherence or loaded policy.

Snapshot distribution is separate test scaffolding. Production sources contain
no snapshot URL, test-purpose prompt, or distribution override. A later snapshot
must archive this source revision and preserve these rules. Verify the normal
production distribution independently before release; never publish a snapshot
to production aliases.

## Fixed-copy output contract

User-facing setup copy is owned by the templates in `ef-onboarding` references.
Scheduling, rule consent, restart, Prefill, and refusal retain their current
behavior and authorization scope. The change makes their wording mandatory,
with only explicit cadence, context-source, rule-path/rule-body, and existing-rule
substitutions. English and Chinese templates are exact; other languages preserve
all content and structure. Native controls retain the complete body and labels.
Failures, host approvals, and user-requested clarification remain authoritative;
never render a success template over a failure. Validate template coverage,
required disclosure retention, allowed substitutions, and the existing lifecycle
contracts before generating the next immutable snapshot.

## Heartbeat host-result contract

Before changing the scheduler prompt, preserve these boundaries:

| Invariant | Current source / final owner | Boundary | Verification | Intentional change |
| --- | --- | --- | --- | --- |
| Required host output survives empty, unchanged, and non-actionable results | heartbeat-execution.md and CLI plan / same owners | Skill result to host final response | CLI render and Skill contract tests; manual injected XML and non-XML host checks | Explicitly forbid empty output in place of required structure |
| Notification suppression does not suppress the host result | Host-injected schema / host schema plus execution reference | Final response to user notification | Manual no-update DONT_NOTIFY and actionable NOTIFY cycles | Clarify routine completion alone does not warrant notification |
| Native scheduler prompt is canonical | CLI heartbeatSchedulerPrompt and recurring-trigger.md / same owners | CLI or Skill to persisted task | Prompt parity and read-back contract; non-default Home/server fixture | Prohibit appended silence prose; repair the same owned task |
| Identity, authorization, cadence, recovery and single-trigger ownership persist | Existing onboarding and heartbeat contracts / unchanged | Setup, scheduler, CLI, network | Existing CLI suite and manual task read-back | None |

The host remains authoritative for schema, required fields, identifiers, and
notification decisions. XML is conditional on the actual injected schema. A
failed or incomplete check must never be relabeled as an empty successful check.
Production instructions contain no test-only task IDs or host-specific invented
schema. Tests cannot establish model adherence; validate actual host output over
several empty, unchanged, actionable, and failed cycles before production release.

### Quiet-wording cleanup

Remove only the named legacy silence example from the onboarding trigger and
heartbeat execution references. Their canonical owners retain exact template
persistence, full read-back verification, and complete host results on quiet
cycles. Identity, consent, cadence, and recovery are unchanged. Verify the
existing scheduler parity and host-result contracts before snapshot distribution.

### Host-output deduplication

Preserve the host-result contract above while assigning detail to
heartbeat-execution.md, a short priority reminder to the CLI plan, and a compact
standalone requirement to the persisted scheduler prompt. Keep prompt parity,
no-update structured results, host-specific notification decisions, incomplete
cycle reporting, and exact task read-back checks. No authorization, lifecycle,
identity, user-facing template, or scheduler ownership behavior changes.
