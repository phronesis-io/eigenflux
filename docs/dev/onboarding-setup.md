# First-time connection contract

The installation entry owns host installation. `ef-onboarding` owns the
foreground connection flow. Host tools own execution approvals and scheduling;
the CLI and server retain identity, authorization, and network-state ownership.

| Invariant | Source / final owner | Boundary | Verification | Intentional change |
| --- | --- | --- | --- | --- |
| Install only the invoking host; preserve other hosts, existing configuration, and explicit opt-outs | `skills/install.md`, `static/install.sh` | Installer to host plugin | Isolated installer tests with a non-default Home, installed/missing/failed plugin, and opt-out | Explain official plugin scope; no additional conversational install consent |
| Plugin installation uses a compatible Codex executable consistently | `static/install.sh`, `skills/install.md` | PATH / app bundle discovery to install receipt and verification | Isolated installer tests: old PATH plus compatible app, compatible PATH only, unsupported / unknown versions | Require Codex >= 0.142.0; fall back to existing app binaries without upgrading Codex or changing PATH permanently; receipt carries selected path/version |
| Preserve exact Home, selected server, identity, referral, and existing accounts | Installation entry, CLI auth/config, Console handoff | Install, restart, scheduler, provision | Existing Home/referral integration tests and CLI suite | None |
| Choice presentation preserves explicit consent and full disclosure | Main onboarding Skill template contract / same owner | Complete chat template to explicit user reply | Template and authorization contracts; manual affirmative, refusal, ambiguous-answer, no-answer, and resume checks | Present each choice directly in chat; preserve complete disclosures, one pending decision, and explicit user consent; host execution approvals remain separate |
| Scheduling consent is distinct from command-rule consent | `ef-onboarding/references/consent.md`, `execution-permission.md` | User choice to host mutation | Skill contract tests and manual refusal scenarios | Two separate required decisions; either refusal pauses new onboarding |
| Prefill context access remains optional, bounded, and independently authorized | `consent.md`, `prefill.md` | Context retrieval to review-only Console draft | Existing draft/provenance tests; manual opt-out and ambiguous-answer scenarios | Ask after required setup activation; generic continuation never grants Prefill |
| Rule writes preserve other rules and require approval for the concrete scope | `execution-permission.md` | User consent to host execution policy | Rule checker with non-default Home/server; conflict and write-denial scenarios | Refusal, conflict, or unverified activation blocks new connection; no sandbox-policy fallback |
| Installation does not change Codex sandbox policy | `static/install.sh`; command permission owned by `execution-permission.md` | Installer to user config | Full installer fixture checks config bytes and absence of alternate policy advice | Remove the redundant sandbox configuration route; keep existing user policy untouched |
| Plugin and rule activation share one restart when needed | Installation entry, `activation.md` | Installer output and confirmed user choices to resumed original task | Structured installer result tests; manual fresh-process continuation | Defer Codex restart until both are prepared; retain native approvals |
| Resume does not invent consent or replay mutations | `activation.md`, authoritative tool results in the original task | Process restart to next setup stage | Manual original-task, missing-context, failed-activation scenarios; existing identity tests | Reuse established choices and verified operations; ask only for missing authority |
| Exactly one verified active trigger precedes provision | `recurring-trigger.md` | Activated setup to scheduler to CLI | Existing scheduler prompt parity and CLI integration tests; manual persistence checks | Move trigger creation after both required gates and activation; preserve disabled triggers |
| Baseline, privacy limits, Console confirmation, output language, and external-action restrictions | Existing `prefill.md`, `console-handoff.md`, CLI/server | Setup to live network and human Console | Existing CLI contract/integration suite | None |

Manual acceptance covers: refusing either required choice; accepting each then
declining Prefill; approving scoped Prefill; one combined restart after a fresh
plugin install; Rules-only restart for an already active plugin; already active
matching Rules without duplicate writes; resume with missing conversation
evidence; host rejection and conflicting rules; and repeated setup without a
second identity or recurring trigger. Check language and actual approval behavior
in Codex; static Skill checks do not establish model adherence or loaded policy.

## Fixed-copy output contract

User-facing setup copy is owned by the templates in `ef-onboarding` references.
Scheduling, rule consent, restart, Prefill, and refusal retain their current
behavior and authorization scope. Their wording is mandatory,
with only explicit cadence, context-source, rule-path/rule-body, and existing-rule
substitutions. English and Chinese templates are exact; other languages preserve
all content and structure. Present the complete body and labels directly in chat
and wait for an explicit reply before dependent actions. No answer grants no
permission; preserve confirmed choices on resume. Only the presentation changes:
identity, storage, consent scope, stage order, and native execution approvals
retain the owners and checks in the table above.
Failures, host approvals, and user-requested clarification remain authoritative;
never render a success template over a failure. Validate template coverage,
required disclosure retention, allowed substitutions, and the existing lifecycle
contracts before release. The permission reference owns the reply lists and
existing-rule substitutions; render the choices once.

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

## Release and installation compatibility

CLI 0.0.52 introduces the direct runtime flags and Attention JSON arguments.
The Skills bundle requires CLI 0.0.52 so released 0.0.49–0.0.51 clients cannot
adopt instructions for unsupported commands. Environment metadata and stdin
remain supported for existing integrations. New native tasks use direct flags.

After review and merge, publish CLI 0.0.52 from main with the normal Release CLI
workflow, verify its public version and command flags, and verify the signed
Skills release and normal installation entry. The Release Skills workflow also
runs on merge; its minimum-version gate protects older clients while the binary
release is pending. Complete public installation verification only after both
artifacts are available. Deploy the merged main installer through the normal
backend deployment process; do not serve a feature-branch installer.

Release checks use the normal CLI build, signed Skills synchronization tests,
and installer integration tests. No
alternate CDN, revision pin, signing key, or test installation URL is part of
the product. Disposable fixture keys and loopback endpoints stay in tests.
