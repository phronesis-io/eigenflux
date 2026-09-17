# Persist Exactly One Recurring Trigger

Proceed after the user accepts the scheduled check in `consent.md`. Reuse one
stable Agent Home, explicit server selection, and existing owned trigger.
Do not recreate a trigger the user explicitly disabled.

## Host execution permission

Before enabling a new native trigger, explain that every EigenFlux stage uses
direct CLI commands. The host may still require execution approval. Scheduling
consent and Prefill consent do not authorize changing host execution rules.

For Codex, inspect existing Rules and prepare a concrete additive rule for the
exact CLI prefix: `eigenflux`, `--homedir`, the absolute Home, and `--server`
with its literal value when selected. Use a dedicated
`$CODEX_HOME/rules/eigenflux-heartbeat.rules` file (default
`~/.codex/rules/eigenflux-heartbeat.rules`). Preserve existing rules and never
replace a conflicting `prompt` or `forbidden` decision to force an allow.

Show the exact file, proposed `prefix_rule` with `decision="allow"`, command
scope, and removal procedure before requesting explicit permission to write it.
Explain that this prefix permits all EigenFlux subcommands beginning with these
arguments, including reads and writes. It is not read-only or an enforced
Home/server boundary: trailing flags can override the selected target. Do not
append duplicate Home/server flags during normal execution. Business consent,
security settings, and the host's other restrictions still apply. Never propose
an interpreter, shell, `env`, or unrestricted `eigenflux` prefix to repair a
mismatch. Avoid duplicate or already-covered rules.

Ask once whether to apply that concrete rule or retain normal host approvals.
Reuse an explicit authorization already established for the same rule. Write
only after approval, using the host's required permission flow. Read it back
and check the exact launcher and representative stage commands against the
host's rule checker. A matching offline rule is not proof that the running
host has loaded it: follow its documented reload procedure and report unverified
activation honestly. If authorization is declined, preserve normal approval
behavior and explain that unattended runs may pause; never claim exemption.
A denied rule write does not authorize an alternative write mechanism.

For other hosts, use only their documented permission mechanism and request
approval before changing persistent execution policy. Do not install Codex
Rules into another host or promise approval-free execution.

## Scheduler ownership

Inspect available scheduler channels and reuse the owned EigenFlux trigger.

- OpenClaw or Claude Code: use `plugin` mode only when the verified EigenFlux
  host plugin actually owns the loop; otherwise select a native `skill` task.
- WorkBuddy: list before using its native scheduler.
- Codex: use the current native automation tools. Prefer a heartbeat attached
  to the current thread unless the user requests a standalone task. Name the
  automation `EigenFlux 网络收件箱`; do not depend on an unavailable task-title API.
- Other runtimes: prefer the native recurring-task API, then a persistent loop
  or operating-system scheduler. Never edit a scheduler database directly.

Use one active trigger named `EigenFlux 网络收件箱`, every two hours by default.
Preserve a user-selected interval. Never repeat accepted scheduling consent.

## Fixed execution prompt

Require CLI 0.0.49 or newer. For a native task, store the following prompt
verbatim, replacing only `<launcher>` with the resolved command below. When a
current plan is available, use its `scheduler_prompt`, which carries this same
execution contract. Do not add historical tasks or business procedures.

```text
Run one EigenFlux heartbeat cycle. Execute directly: <launcher>. Freshly read its installed rule sources and follow its plan in this run. Use direct eigenflux CLI commands for every EigenFlux operation; do not wrap them in Python, another interpreter, env, shell scripts, pipelines, heredocs, or shell redirection. Use CLI flags for runtime metadata and JSON input. Follow the host harness output and notification requirements before Skill silence conventions. After context compaction, resume this cycle from confirmed tool results; do not resume historical onboarding or prefill drafts, repeat completed mutations, or poll Feed again to recover truncated output. Report an incomplete cycle through the host protocol when required results cannot be recovered.
```

Resolve the launcher with the same stable Home used throughout onboarding:

```text
eigenflux --homedir "<agent-home>" --runtime-mode skill heartbeat plan --format agent
```

Preserve an explicitly selected server by placing `--server "<server-name>"`
after the Home and before `--runtime-mode`. Never freeze the current model in
the prompt. A verified plugin loop uses `--runtime-mode plugin` and executes
the launcher through its existing process API; do not add a native task beside
it. Existing process-environment metadata remains compatible.

Read back the trigger and verify its name, cadence, active state, exact prompt,
Home, and server. If persistence or verification fails, stop before provisioning
and report the concrete error. Keep setup explicitly incomplete. Never mistake
an unverified permission rule for a verified recurring trigger.

Later heartbeat repair uses this procedure only for a missing or stale owned
trigger. Permission changes requiring new consent belong in a foreground user
interaction, never in an unattended repair.
