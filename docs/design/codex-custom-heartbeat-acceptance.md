# Codex custom heartbeat acceptance

## Problem and decision

An existing owner-customized heartbeat runs correctly but `guard-setup` rejects
its exact prompt because it differs from the onboarding template. Replacing that
prompt would remove the owner's monitoring requirements. Preserve those
requirements and add an explicit, verified scheduler-readback setup path.

## Scope and contract

- Keep the canonical setup path unchanged. A prompt file alone must not authorize
  a custom prompt.
- Accept a custom prompt only with fresh, complete host scheduler inventory,
  an explicit task ID, a unique enabled EigenFlux heartbeat for this Home/server,
  the same target thread, and the exact nonempty prompt read back from that task.
- Bind hooks to the SHA-256 of the whole prompt and retain account, thread,
  host trust, Stop-cycle completion and definition fences. A quoted fragment,
  different prompt, wrong thread or stale readback must not admit execution.
- Setup preserves task contents, cadence, status, other hooks and permissions.
  Repeating the same setup is idempotent; changed definitions require host review.
- Do not alter Feed delivery, Agent decisions, publishing, trades or PM authority.
  Do not interpret successful setup as unattended execution or visible delivery.

## Validation and release

Exercise accepted custom setup and rejected missing/stale/incomplete inventory,
wrong task/thread/account, duplicate/paused tasks and prompt drift. Verify exact
prompt admission and ordinary-message rejection, plus existing canonical tests.
Run the CLI command suite and build. Keep the fix in review until merged and
released; install the signed release before local acceptance. Roll back by
restoring the previous scoped hooks, preserving unrelated hooks and host trust.

## Operator interface

For an owner-approved custom prompt, provide all three flags to
`heartbeat guard-setup`: `--scheduler-prompt-file`,
`--scheduler-readback-file`, and `--scheduler-task-id`. Both file paths must be
absolute regular files. The prompt is limited to 64 KiB; the readback is less
than 2 MiB and uses the same `observed_at`, `complete`, `tasks` JSON schema as
`heartbeat scheduler-report`. Read the complete inventory from the host API
immediately before setup (within five minutes); do not synthesize a successful
inventory from a rendered card or an old local cache.

The task must be enabled, owned by EigenFlux for heartbeat execution, uniquely
match the selected Home/server, and match the explicit task ID and thread.
The supplied prompt must equal the task prompt after outer whitespace trimming,
which is also how the existing host observer compares prompts. All validation
runs before hook installation. Supplying only a prompt file retains the original
canonical-template requirement. Hook review and a real scheduled run remain
required after setup; no scheduler task is modified by this command.

## Separate operational findings

The macOS Commission watch is independently blocked before process startup by
system policy denying its Desktop log path. A supported permissions decision is
separate from this compatibility fix; do not disable OS protection or fabricate
watch readiness. A rendered scheduler card alone is not a complete machine-readable
inventory. Retain unavailable readback until an authoritative host adapter provides it.
