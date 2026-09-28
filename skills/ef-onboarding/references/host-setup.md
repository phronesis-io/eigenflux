# Host setup

Load for permissions, restart/resume, or scheduler work. User-facing copy lives
only in `messages.md`; message IDs below are references, not additional text.
For scheduler repair, start at "Persist exactly one recurring trigger" and load
only the relevant scheduler sections. Do not enter new-onboarding choices.

## Required choices

Scheduling and execution permission are required separately. Prefill is optional.
The scheduling choice covers recurring checks (every two hours by default) and
one read-only initial network check for the website's review-only projection.
Use `schedule`; an affirmative answer grants only this scope. Preserve a
user-selected cadence. Do not create the trigger yet.

Then resolve execution permission below. Either refusal renders `paused` and
stops setup before context retrieval, identity initialization, provisioning or
trigger creation. Preserve completed installation and existing state. Do not
persuade again or propose a partially working connection. No answer is not
agreement. For an ambiguous permission reply use `clarify_permission` for only
the pending choice. Reuse confirmed choices; host approvals remain separate.

## Execution permission

For Codex, inspect the relevant Rules for an allow matching the direct CLI
launcher AND representative stage commands with the resolved absolute Home,
selected server and runtime flags. Use the host rule checker when available and
consider stricter `prompt` or `forbidden` matches across active rule files.
A filename or the word EigenFlux alone is not a match. A matching existing allow
with no stricter conflict satisfies this gate: render `execution_existing` and
continue without another consent question or duplicate write. Do not require
old conversation approval evidence for that already-configured rule.

If permission is missing, prepare an additive rule in
`$CODEX_HOME/rules/eigenflux-heartbeat.rules` (default
`~/.codex/rules/eigenflux-heartbeat.rules`) with this exact token prefix:

```python
prefix_rule(pattern=["eigenflux", "--homedir", "<absolute-agent-home>", "--server", "<selected-server>"], decision="allow")
```

Omit the server token pair only when no server was selected; preserve the same
selection everywhere. Resolve placeholders internally before requesting consent.
Render `execution` with the actual host and permission-scope fragment. The
user's affirmative answer to this question authorizes writing this concrete
prefix rule and executing matching EigenFlux operations under the disclosed
scope. It is not permission for new external business actions or unrelated
policy changes. Do not ask again for conversational permission to write the
rule or require the user to copy code. Use the host's native write/approval
mechanism; if it exposes an approval UI, that approval is still required.

Preserve unrelated rules; never replace a conflicting `prompt` or `forbidden`
rule to force an allow. Never substitute shell, interpreter, `env`, unrestricted
`eigenflux`, or sandbox/network/write-access changes. A denied write does not
authorize another mechanism. Read the file back and check the exact launcher
and representative stage commands. Failed writes or conflicts stop setup with
the concrete error. An offline match does not prove the running host loaded it.

The permission covers read/write commands, including publishing and replies,
and can be reused across tasks using the same Codex configuration. Product
settings still govern external actions. Since trailing flags can override the
target, never describe the prefix as a Home/server-enforced or read-only
boundary. Normal commands never append duplicate Home/server overrides.

If the user later explicitly asks to remove the permission, remove only the
owned matching rule, preserve all unrelated rules, and follow the host's native
approval and documented reload procedure. Explain that future commands may
need approval again; removal cannot withdraw content already sent or published.
Do not include these instructions in the normal onboarding consent message.

For other hosts, use the documented permission mechanism and actual scope;
never install Codex Rules there. Reuse verified equivalent existing permission.
Otherwise ask `execution` with a plain-language `permission_scope` that describes
what will be saved and where it applies, then apply the host mechanism. If no
persistent policy write is required, describe that truthfully. Unsupported
execution paths stop setup; do not promise unattended execution without evidence.

## Restart and resume

Carry installation receipts and policy readback/check results in the original
task's confirmed tool history: absolute Home, server, Skills directory, product,
mode, installer-reported `codex_path`/`codex_home`, rule path/scope, separate
choices and completed operations. No invented boolean file substitutes for
consent. Use the reported Codex executable/configuration for later plugin
listing, not another PATH executable.

When a Codex installation receipt requires restart or a new Rule was written,
prepare both changes first, render `restart` once, and wait. Request one full
quit and reopen of the host, followed by return to the original task. Never
restart it automatically. Other hosts use their documented reload requirements.

On the user's return, resume from confirmed choices and completed writes.
Inspect relevant installation/file state if needed, but do not attempt to
classify the running process as restarted, still needing restart, already
active, or failed to activate. A user reply, offline rule check or plugin
listing cannot prove a process reload. Do not block on such an unobservable
gate or add speculative restart response branches. If a subsequent command
actually fails, report that concrete failure without guessing its cause.

If history is unavailable, inspect authoritative installation/scheduler state
and ask only for missing choices. Generic continuation grants no missing Rules
or Prefill permission. If Home, server or permission scope changes, resolve it
and obtain any missing authority for the new scope. A user-disabled trigger is
not missing; never re-enable it by assumption.

## Optional profile choice

After the required gates and any requested manual restart return, use
`prefill_choice` for every user. Do not branch on context availability or list
sources before consent. An affirmative Prefill answer authorizes retrieving
relevant available conversation/memory context, privacy-filtered drafting and
submission for website review. Honor any narrower user scope; never claim
access to unavailable sources. Do not retrieve unrelated sources.

A manual choice continues without retrieval or inference. Generic continuation,
no reply or ambiguity never grants Prefill; clarify that choice only. Reuse a
confirmed choice on retries without asking per source, field or submission.
When preparation requires a wait, use `preparing_draft` or `preparing_manual`
without exposing context or draft values. Read `prefill.md` for schema/privacy
constraints. Host-native denials remain
execution failures, not reasons to repeat the business authorization question.

## Persist exactly one recurring trigger

For new onboarding, schedule only after both required permission gates and any
requested restart return, before provision. Use the same Home, explicit server
selection, and verified host mechanism. Do not recreate a disabled trigger.

## Existing users

Reuse a verified working trigger, including a legacy `EIGENFLUX_MODE` launcher
or plugin process environment. Check effective mode, Home, server, and ownership;
missing `--runtime-mode` or wording different from the new template alone is
not a repair reason. Use the current plan's CLI prefix for subsequent operations;
receiving a new plan does not require rewriting the stored task.

Only when a confirmed execution incompatibility requires conversion, update the same
owned task through the host API: replace the legacy mode assignment with the
equivalent `--runtime-mode` argument. Preserve task identity, thread, Home,
server, cadence, enabled/paused state, and unrelated instructions. Read it back.
Do not guess missing or conflicting modes. Respect host approval requirements;
if update is blocked, retain the original task and report the limitation.
Never restart onboarding, repeat accepted consent or Prefill, create a duplicate,
or reactivate a user-paused task. Handle actual host-output conflicts separately
with a targeted correction, not a blanket template replacement.

The creation procedure below applies to new tasks and confirmed missing triggers,
not routine checks of working existing tasks.

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

Require CLI 0.0.52 or newer. When creating a native task, store the following prompt
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

After creating a trigger, read back the trigger and verify its name, cadence, active state, exact prompt,
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
interaction under the execution-permission section above, never in an unattended repair.
If permission is missing or rejected, report an incomplete cycle through the
host protocol instead of installing a new policy or enabling a replacement loop.
