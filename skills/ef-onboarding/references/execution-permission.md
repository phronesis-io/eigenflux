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

The reply lists below are part of each template. Render them once with their
bold labels and explanations; do not append a second choice summary.

Simplified Chinese:

> 接下来，还需要你允许 Agent 自动运行 EigenFlux 命令。
>
> 你刚才同意了定时检查。为了让 Agent 执行检查时，能够读取新动态、保存处理进度，而不必每次都停下来等你确认，还需要添加一项命令运行许可。这样，你不在电脑前时，检查也能继续进行。
>
> **我会在 Codex 中添加一条运行许可。这是完成 EigenFlux 接入、让定时检查持续运行的必要一步。**
>
> 这项许可包含读取和写入操作，也涵盖发布、回复等命令；Agent 仍需按照你在 EigenFlux 中选择的设置行动。之后，在使用同一份 Codex 配置的任务中运行符合这条规则的 EigenFlux 命令，也可以沿用这项许可。
>
> 规则文件：`<rules-file>`
>
> <rule-block>
>
> 你随时可以让我协助移除这条许可，保留其他规则，再重启 Codex。移除后，后续操作可能重新需要确认；已经发布或发送的内容不会自动撤回。
>
> **请直接回复：**
>
> - **同意添加**：添加许可，继续接入。
> - **暂不接入**：暂停接入，不添加许可。

English:

> Next, I need your permission for the Agent to run EigenFlux commands automatically.
>
> You have agreed to scheduled checks. To let the Agent read new updates and save its progress during those checks without stopping for your approval each time, I also need to add command execution permission. This lets checks continue while you are away from your computer.
>
> **I will add an execution rule in Codex. This is a required step to complete EigenFlux setup and keep scheduled checks running.**
>
> This permission covers read and write operations, including publishing and reply commands; the Agent must still follow the settings you choose in EigenFlux. Tasks using the same Codex configuration can also use this permission when running EigenFlux commands that match the rule.
>
> Rule file: `<rules-file>`
>
> <rule-block>
>
> You can ask me to help remove this permission at any time, preserving other rules, then restart Codex. After removal, future operations may need approval again; content already published or sent will not be automatically withdrawn.
>
> **Please reply with one of these options:**
>
> - **Allow**: Add permission and continue setup.
> - **Not now**: Pause setup without adding permission.

Keep the read/write scope, reuse across Codex tasks using the same configuration,
and revocation explanation. Because trailing flags can override the target,
do not imply a read-only or Home/server-enforced boundary. Product settings
remain workflow requirements, not restrictions enforced by this rule.

If the exact needed permission already exists, show the matching scope and ask
to use it for this connection unless that consent is already established. Use
only these exact substitutions in the same template:

| Language | Original | Existing-rule replacement |
| --- | --- | --- |
| Chinese | 还需要添加一项命令运行许可 | 还需要你同意使用已有的命令运行许可 |
| Chinese | 我会在 Codex 中添加一条运行许可。 | 我会使用 Codex 中已有的运行许可。 |
| Chinese | **同意添加**：添加许可，继续接入。 | **同意使用**：使用已有许可，继续接入。 |
| Chinese | **暂不接入**：暂停接入，不添加许可。 | **暂不接入**：暂停接入，不使用这项许可继续设置。 |
| English | I also need to add command execution permission | I also need your agreement to use the existing command execution permission |
| English | I will add an execution rule in Codex. | I will use the existing execution rule in Codex. |
| English | **Allow**: Add permission and continue setup. | **Allow use**: Use existing permission and continue setup. |
| English | **Not now**: Pause setup without adding permission. | **Not now**: Pause setup without using this permission to continue. |

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
