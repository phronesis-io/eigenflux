# First-time connection contract

The installation entry owns host installation. `ef-onboarding` owns the
foreground connection flow. Host tools own execution approvals and scheduling;
the CLI and server retain identity, authorization, and network-state ownership.

| Invariant | Source / final owner | Boundary | Verification | Intentional change |
| --- | --- | --- | --- | --- |
| Install only the invoking host; preserve other hosts, existing configuration, and explicit opt-outs | `skills/install.md`, `static/install.sh` | Installer to host plugin | Isolated installer tests with a non-default Home, installed/missing/failed plugin, and opt-out | Explain official plugin scope; no additional conversational install consent |
| Preserve exact Home, selected server, identity, referral, and existing accounts | Installation entry, CLI auth/config, Console handoff | Install, restart, scheduler, provision | Existing Home/referral integration tests and CLI suite | None |
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
