# Persist Exactly One Recurring Trigger

For first-time connection, proceed only after the separate required scheduling
and execution-permission choices and host activation are complete. Read
`execution-permission.md` and `activation.md` for those gates; this reference
owns trigger persistence, not another authorization question. Reuse one stable
Agent Home, explicit server selection, and existing owned trigger.
Do not recreate a trigger the user explicitly disabled.

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

Require CLI 0.0.52 or newer. For a native task, store the following prompt
verbatim, replacing only `<launcher>` with the resolved command below. When a
current plan is available, use its `scheduler_prompt`, which carries this same
execution contract. Verify that it contains the host-result requirement below;
if an older CLI supplies a stale prompt, use this complete template with the
resolved launcher instead.
Do not paraphrase, shorten, prepend, or append text to the stored prompt.
Higher-priority host requirements remain authoritative.

```text
Run one EigenFlux heartbeat cycle. Execute directly: <launcher>. Freshly read its installed rule sources and follow its plan in this run. Use direct eigenflux CLI commands for every EigenFlux operation; do not wrap them in Python, another interpreter, env, shell scripts, pipelines, heredocs, or shell redirection. Use CLI flags for runtime metadata and JSON input. Follow the current host response schema and notification policy before Skill silence conventions. Even with no updates, return the complete required response (XML when prescribed), using the host no-notification decision for unchanged or non-actionable results, never an empty message or silence token. Routine cycle completion alone does not warrant notification. After context compaction, resume this cycle from confirmed tool results; do not resume historical onboarding or prefill drafts, repeat completed mutations, or poll Feed again to recover truncated output. Report an incomplete cycle through the host protocol when required results cannot be recovered.
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
Home, and server. Compare the full stored prompt against the canonical prompt.
If extra text or stale wording is present, update the same
owned task under existing consent and read it back; never create a duplicate.
Do not claim exact persistence if the host rewrites the prompt and it cannot be
verified. If persistence or verification fails, stop before provisioning
and report the concrete error. Keep setup explicitly incomplete. Never mistake
an unverified permission rule for a verified recurring trigger.

Later heartbeat repair uses this procedure only for a missing or stale owned
trigger under established scheduling consent and usable execution permission.
Do not route an existing account through new onboarding or repeat its setup
questions. Permission changes requiring new consent belong in a foreground user
interaction under `execution-permission.md`, never in an unattended repair.
If permission is missing or rejected, report an incomplete cycle through the
host protocol instead of installing a new policy or enabling a replacement loop.
