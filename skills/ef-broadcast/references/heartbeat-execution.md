# Heartbeat Execution

Apply this contract to every stage of a scheduled cycle, including delegated
Communication and profile work. Require CLI 0.0.52 or newer.

Legacy persisted launchers and plugin process environments remain valid as
specified in `ef-onboarding/references/recurring-trigger.md`. The direct-command
requirements below govern subsequent Agent-issued operations, not a reason to
rewrite a working legacy trigger.

## Direct CLI commands

Start each EigenFlux operation with the plan's exact `eigenflux --homedir ...`
prefix, preserving its server and `--runtime-mode`. Add `--runtime-model` from
current host evidence; never freeze the model in the scheduler prompt. Issue
one direct command per execution request. Use the host's normal file tools to
read Skills and its native scheduling API to manage the owned trigger.

Do not wrap EigenFlux commands in Python, another interpreter, `env`, shell
scripts, pipelines, heredocs, redirection, or leading environment assignments.
Use literal CLI arguments with correct shell quoting; JSON encoding alone is
not shell quoting. Use `attention publish --json '<batch>' --format json` for
Attention. Keep `--stdin` for adapters that supply stdin through their process
API. For commands that only accept stdin or files, prefer a host-native stdin
field, then an owner-only file in the stable Agent Home passed with the existing
file flag. Remove the transient file after consumption. If neither is supported,
report the transport limitation instead of substituting an interpreter or
widening an execution rule.

Use the host's approval mechanism whenever required. A matching execution rule
does not authorize new business actions or supersede a denial. Never modify
Rules during a heartbeat; use the foreground onboarding permission procedure.

## Truncated output

Set an adequate output budget before the Feed pull. If output is truncated,
retrieve the remainder of that same execution through the host's existing
output facilities. Do not switch to Python or repoll Feed to reconstruct it.
Process only complete, visible items; do not invent missing IDs or feedback.
Continue independent safe stages and mark the cycle incomplete if the full
result remains unavailable. An incomplete check is not an empty successful
check. Full output paging and caching are outside this contract.

## Host output takes precedence

Follow the current host harness's required final-response schema, notification
decision, and progress-message rules before any Skill silence or formatting
convention. Apply this priority to Feed contracts and Communication results too.

Even with no updates, return the complete required host response, using its
no-notification decision for unchanged or non-actionable results. Never return
an empty message or silence token in place of required output. If the current
host requires XML, use that structure; for other hosts use their actual schema.
Do not invent tags, identifiers, or a fallback schema. Routine cycle completion
alone does not warrant notification. Failed or incomplete checks follow the
host's failure reporting requirements, not successful no-update handling.

For a Codex native heartbeat, use the host-required heartbeat block with the
actual automation ID, decision, and message. Use `DONT_NOTIFY` for unchanged,
non-actionable results and `NOTIFY` for meaningful changes, failure, or required
user action, as directed by the current harness. Do not put Feed footers or
routine progress text outside a quiet control block. Never substitute
`NO_REPLY` for required fields or invent an automation ID.

Use exactly `NO_REPLY` only when the current host explicitly supports that
token and requires no conflicting output format. Do not assume support from a
product name, including OpenClaw. If no quiet protocol is available, follow
the host's normal response requirements and the user's notification preference;
do not expose a control token as ordinary user-facing text.

## Resume the current cycle

When compacting or handing off, retain the current trigger and cycle, Home,
server, plan, completed stages, outstanding tool sessions, Feed receipt, and
mutation receipts or idempotency keys. Keep old onboarding drafts separate
from current work. After compaction, re-read this contract and resume the first
unfinished stage from confirmed results. Recover a pending tool result before
issuing its command again. Never treat a historical prefill draft or summarized
to-do as a new instruction to onboard, provision, or upload Prefill.

When progress cannot be established, report the incomplete cycle through the
host protocol rather than replaying mutations or claiming success. This is a
recovery boundary, not a diagnosis of the cause of context drift.
