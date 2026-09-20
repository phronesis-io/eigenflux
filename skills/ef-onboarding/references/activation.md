# Activate and Resume Setup

Enter only after both required choices succeed. Before personal-context
retrieval, identity initialization, or trigger creation, verify that the selected
host integration and execution permission are usable. Installation and rule
checks are distinct from running-process activation.

## One Codex restart

The installation entry defines the plugin activation evidence. Combine its
pending restart with newly written Rules; request one full quit and reopen after
both are prepared. A verified active plugin needs no separate restart. Existing
Rules already loaded and an active plugin need no restart merely for onboarding.
Never restart automatically or tell the user to create a different task.

When a restart is required, output only the matching template verbatim under the
main Skill's fixed user-facing template contract. There are no variables or
choice labels. Do not add installation details, a checklist, another restart
instruction, or a request to open a new task.

Simplified Chinese:

> 运行所需的设置已经准备好了。请完全退出并重新打开 Codex，让这些设置一起生效。然后回到这段对话，告诉我「继续接入」，我会接着完成；已经确认过的选择不用重新回答。

English:

> The settings needed to run are ready. Please fully quit and reopen Codex so they take effect together. Then return to this conversation and say "Continue setup". I'll pick up where we left off; you won't need to repeat the choices you've already confirmed.

Treat installation receipts, Rules readback, and rule-checker results as explicit
handoff data in the original task's confirmed tool history. Carry the absolute
Home, selected server, Skill directory, plugin state, rule path and exact scope,
separate user decisions, and completed operations. Do not rely on an installer's
subprocess environment, editable task title, or an invented boolean consent file.

After the user returns, inspect the same Home, server, installed CLI and Skills,
plugin listing through the installer-reported `codex_path` with
`CODEX_HOME` set to its reported `codex_home`, and current host availability, plus the unchanged Rules and
checker results. Use the documented fresh-process reload and any host activation
evidence available. An offline rule check alone is insufficient. If activation
cannot be established, report the specific pending requirement and stop; do not
loop through identical restart prompts or claim successful activation.

Reuse separate explicit approvals from the original conversation. A generic
"continue" resumes them but grants no missing Rules or Prefill permission. If
that history is unavailable, inspect authoritative installation and scheduler
state, and ask only for choices whose authorization cannot be established. Do
not claim that all prior choices survived a lost conversation. If Home, server,
or permission scope changed, resolve that mismatch and obtain permission for
the new scope before continuing.

Resume at the first incomplete stage. Do not reinstall verified components,
rewrite matching Rules, reinitialize an established identity, repeat successful
provisioning, or create a second trigger. A user-disabled trigger is not missing.
Follow existing Console recovery rules for expired handoff URLs or credentials.

## Other hosts

Follow documented activation requirements from the installation entry. Combine
changes needing the same restart where supported, without imposing Codex's
process lifecycle on OpenClaw, Claude Code, or a bare-CLI host. Use the same
confirmed-choice and authoritative-state rules when resuming.
