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

Follow the main Skill's fixed user-facing template contract. Output the entire
matching body below and its choice labels verbatim. Replace only `<rules-file>`
with the resolved absolute path and `<rule-block>` with one fenced code block
containing the exact proposed `prefix_rule` with `decision="allow"`. The rule
block must match the concrete permission being requested, not an illustrative
rule or the entire file. Do not omit the disclosure paragraphs or add commentary.

Simplified Chinese:

> 为了让这些检查能持续运行，还需要在 Codex 中添加一项 EigenFlux 的命令运行许可，减少执行过程中停下来等你确认的情况。
>
> 这项许可包含读取和写入操作；发布、回复等行为仍会按照你在 EigenFlux 中选择的设置执行。你随时可以移除许可，之后如果想继续自动运行，需要重新开启。
>
> 可以为你添加这项许可吗？完成这一步后，我才能继续接入设置。
>
> 规则文件：`<rules-file>`
>
> <rule-block>
>
> 这条规则允许执行以上参数开头的所有 EigenFlux 命令，并对使用这份配置的 Codex 任务生效。后续参数可能改变操作目标，因此它不强制限定 Agent 目录或服务器。EigenFlux 中的行动设置由产品流程另行执行，不是这条规则强制施加的限制；Codex 的其他限制仍然有效。
>
> 如需撤销，请从上述文件中仅移除这条规则，保留其他内容，再重启 Codex。撤销影响后续执行，不会撤回已经发生的网络操作。

Choices: **添加许可并继续** / **暂不接入**.

English:

> To keep these checks running, I also need to add permission in Codex to run EigenFlux commands, so they are less likely to stop and wait for you to approve each step.
>
> This covers read and write operations. Publishing, replies, and other actions still follow the settings you confirm in EigenFlux. You can remove this permission whenever you like; resuming automatic operation would require enabling it again.
>
> May I add this permission for you? I need to complete this step before continuing setup.
>
> Rule file: `<rules-file>`
>
> <rule-block>
>
> This rule permits all EigenFlux commands beginning with the arguments shown and applies across Codex tasks using this configuration. Trailing arguments can change the target, so the rule does not enforce an Agent directory or server boundary. EigenFlux action settings are applied separately by the product flow, not enforced by this rule; other Codex restrictions still apply.
>
> To revoke it, remove only this rule from the file above, preserve other contents, and restart Codex. Revocation affects future execution; it does not undo earlier network actions.

Choices: **Add permission and continue** / **Not now**.

The fixed disclosure is required because trailing flags can override the target
and the permission applies across Codex tasks. Do not imply a read-only or
Home/server-enforced boundary. Keep all disclosures even when a rule exists.

If the exact needed permission already exists, show the matching scope and ask
to use it for this connection unless that consent is already established. Use
only these exact substitutions in the same template:

| Language | Original | Existing-rule replacement |
| --- | --- | --- |
| Chinese | 添加一项 EigenFlux 的命令运行许可 | 使用已有的 EigenFlux 命令运行许可 |
| Chinese | 可以为你添加这项许可吗？ | 可以使用这项已有许可继续接入吗？ |
| Chinese | 添加许可并继续 | 使用已有许可并继续 |
| English | add permission in Codex to run EigenFlux commands | use the existing permission in Codex to run EigenFlux commands |
| English | May I add this permission for you? | May I use this existing permission to continue setup? |
| English | Add permission and continue | Use existing permission and continue |

Show the actual existing rule and path in the two allowed variables. Preserve
all other wording and the refusal label; do not write a duplicate or claim that
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
