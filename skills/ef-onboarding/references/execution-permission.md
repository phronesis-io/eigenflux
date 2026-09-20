# Required Execution Permission

Read after scheduled checks are accepted and before changing execution policy.
This is a separate required choice. Refusal pauses first-time connection; do not
create or enable a trigger, retrieve personal context, initialize an identity,
or provision. Preserve installation and existing state. Use the pause response
in `consent.md`, without repeated persuasion or a fallback approval mode.

## Codex

Inspect existing Rules and prepare a concrete additive rule for the exact CLI
prefix: `eigenflux`, `--homedir`, the resolved absolute Home, and `--server`
with its literal value when selected. Use
`$CODEX_HOME/rules/eigenflux-heartbeat.rules` (default
`~/.codex/rules/eigenflux-heartbeat.rules`). Preserve unrelated contents and
never replace a conflicting `prompt` or `forbidden` rule to force an allow.
Do not propose shell, interpreter, `env`, unrestricted `eigenflux`, or sandbox
network/write-access changes as alternatives.

Use the following Simplified Chinese explanation, followed by the exact file,
proposed `prefix_rule` with `decision="allow"`, and the short scope note below:

> 为了让这些检查能持续运行，还需要在 Codex 中添加一项 EigenFlux 的命令运行许可，减少执行过程中停下来等你确认的情况。
>
> 这项许可包含读取和写入操作；发布、回复等行为仍会按照你在 EigenFlux 中选择的设置执行。你随时可以移除许可，之后如果想继续自动运行，需要重新开启。
>
> 可以为你添加这项许可吗？完成这一步后，我才能继续接入设置。

Choices: **添加许可并继续** / **暂不接入**.

English:

> To keep these checks running, I also need to add permission in Codex to run EigenFlux commands, so they are less likely to stop and wait for you to approve each step.
>
> This covers read and write operations. Publishing, replies, and other actions still follow the settings you confirm in EigenFlux. You can remove this permission whenever you like; resuming automatic operation would require enabling it again.
>
> May I add this permission for you? I need to complete this step before continuing setup.

Choices: **Add permission and continue** / **Not now**.

Scope note: this rule permits every EigenFlux subcommand beginning with the
shown arguments, including reads and writes, and applies to matching commands
across Codex tasks using this configuration. It is not a read-only or enforced
Home/server boundary: trailing flags can override the target. Explain this
briefly in the user's language; keep syntax commentary out of the main copy.
Explain how to remove only the added rule, preserve unrelated rules, and reload
Codex. Removing permission affects future execution; it does not undo prior
network actions. EigenFlux settings are separate business controls, not limits
enforced by this prefix rule. Other host restrictions still apply.

If the exact needed permission already exists, show the matching scope and ask
to use it for this connection unless that consent is already established. Adapt
"add" to "use the existing permission"; do not write a duplicate or claim that
an existing file alone proves user agreement to this onboarding flow.

Accept an affirmative answer only for this displayed permission. Write
only after approval through the host's required tool flow; a denied write does
not authorize another mechanism. Read it back and use the host rule checker
against the exact launcher and representative stage commands, considering other
active rule files. A conflict or failed write leaves connection incomplete.
Never claim that an offline match proves the running host loaded the rule.
Continue to `activation.md` with the exact path, rule, Home, server, write result,
and check result from confirmed tool outputs. Do not append duplicate Home or
server flags to normal commands.

## Other hosts

Use the host's documented permission mechanism, disclose its actual scope, and
obtain a separate affirmative choice before changing persistent policy. If no
policy write is required, ask to use the verified existing execution permission;
do not invent a policy file or promise approval-free execution. Refusal or an
unsupported required execution path pauses connection. Never install Codex Rules
into another host. Preserve the supported bare-CLI installation route.
