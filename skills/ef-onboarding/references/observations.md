# Installation observations

Use these diagnostics only during new onboarding or its explicit resume. They
record observed outcomes; they never authorize a business action, establish
consent, or replace verification. Keep user-facing onboarding messages unchanged.

After the CLI is available, use the established absolute CLI/Home/server prefix
to run `agent onboarding-start`. It resumes the installer's persisted attempt.
Retain the returned `install_session_id`; never expose or inspect its local proof.
Do not use `--new-attempt` on retries, restart, compaction, or a foreground return.
Use that flag only for an explicitly distinct new installation attempt.

At each actual outcome below, run:

```sh
<cli-prefix> agent onboarding-observe --event <event> --result <result>
```

Add only the listed component, choice, reason, count or attempt. Do not pass
chat, memory, profile text, credentials, URLs, device names, paths, error bodies,
or invented evidence. Failed recording/upload does not repeat the business
action or stop onboarding. `upload=pending` is not a server acknowledgement.
Retain the same Home/server; do not repair telemetry by changing identity.

| Actual observation | Event and fields |
| --- | --- |
| Required CLI/Skills/plugin operation starts, succeeds or explicitly fails | `component_install` or `component_verify`, `--component cli/skills/plugin`, `started/succeeded/failed`. File presence is not plugin loading or Skills reading. Record verified reading/loading under `component_verify` only after actual evidence. |
| Scheduling/execution question was actually presented | `permission_scheduling` / `permission_execution`, `started` |
| Explicit affirmative or negative answer | Corresponding permission, `succeeded` / `declined --reason user_declined`; no answer stays unreported, not declined. |
| Existing execution authority was verified | `permission_execution`, `succeeded --reason already_configured`; do not invent a new consent response. |
| A required host restart was requested | `host_restart`, `pending --reason restart_required`. A user return or offline rule check does not prove restart. Only report success with actual host evidence; otherwise leave pending and follow normal resume. |
| Explicit Prefill choice | `profile_mode`, `succeeded --choice prefill` or `succeeded --choice manual` |
| Prefill retrieval permission was explicitly granted | `profile_read_permission`, `succeeded`; a manual choice is `skipped --reason manual_selected` and performs no retrieval. |
| Authorized privacy-filtered draft is ready / preparation explicitly failed | `profile_draft`, `succeeded` / `failed`; manual entry is `skipped --reason manual_selected`. Do not upload draft fields to telemetry. |
| Exactly one correctly bound persistent trigger has been verified or reused | `scheduler_setup`, `succeeded`; otherwise `failed` only on a confirmed error, or `pending` for missing evidence. Never enable paused tasks to improve this metric. |
| Validated Console link has actually been presented to the owner | `console_link_delivered`, `succeeded`; generating a link alone is insufficient. |
| One permitted baseline poll started / explicitly failed | `baseline_feed`, `started/failed`. The CLI automatically records a fresh validated pre-onboarding baseline response, including a successful empty response; do not duplicate its success event. |
| Attention Prefill was accepted by the server | `attention_prefill`, `succeeded --count N`; with no qualifying candidates use `skipped --reason no_candidates --count 0`, not failed. |
| Fresh authenticated access confirms completed onboarding | `onboarding_unlocked`, `succeeded`; pending website setup is `pending`, not failed. |

The CLI automatically records stable identity initialization, credential-save
outcome, fresh pre-onboarding baseline success, and installation acknowledgement transport when an installation session
exists. Do not manually duplicate these receipts or treat them as full setup.
Native scheduler readback and runtime cycle receipts remain required separately.

## Optional foreground first check

Only when the user explicitly requests the first check, record
`first_check_requested --result started`. Retain its returned `attempt_id`
across compaction and all following outcomes. It is not a new install session.
Do not count an unrequested check, generated plan or baseline as a first check.

1. Recheck fresh access exactly as `connection.md` requires. If incomplete,
   record `first_check --result pending --attempt <id>` and follow that route.
2. Immediately before executing the admitted foreground cycle, record
   `first_check --result started --attempt <id>`.
3. After the required visible business checks actually complete, record
   `first_check --result succeeded --attempt <id>`. No new relevant content is
   still success. A confirmed failure is `failed` with a fixed reason such as
   `network_error`, `permission_denied`, `timeout` or `unknown_failure`.
   Keep optional Need maintenance and other legitimate skips out of the failure
   denominator, following the existing first-check completion rule.
4. After the actual owner-visible result has been delivered, record
   `first_check_result --result succeeded --attempt <id>`. Preparing the text,
   printing CLI output or finishing the cycle does not prove owner delivery.
   Interrupted or unobservable delivery stays unreported. Do not send a duplicate
   result merely to obtain a receipt.

The first check is optional. Scheduled checks may run without it and use their
existing runtime/cycle observations; do not emit foreground-first-check events
for those runs. These observations do not prove that a human read the result.
