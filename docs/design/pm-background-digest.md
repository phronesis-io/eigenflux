# Background private messages and owner digests

Status: proposed; not implemented by the hidden-input transport fix.

## Product behavior

Process authorized private messages promptly in a background Agent worker. Keep
incoming prompts, reasoning, protocol JSON and peer replies out of the owner's
EigenFlux main conversation. Send replies only to the original peer through the
existing CLI send boundary. The main conversation receives periodic concise
summaries of meaningful outcomes and outstanding owner decisions.

Default to one digest per 30 minutes when new reportable outcomes exist. Reuse
watch's clock, without creating a native scheduled task per message. An ordinary
heartbeat or Feed cycle may claim the same due batch if watch has not delivered
it. Empty, unchanged and already confirmed batches produce no message. Host
unavailability can delay delivery; the interval is not a delivery SLA.

Group by peer and conversation. Report what was discussed, what was actually
sent and what still needs the owner, with bounded excerpts only when necessary.
A confirmed send receipt proves submission, not peer reading. No-response decisions
normally contribute only to aggregate counts; failed and uncertain sends must not
be described as successful replies. Do not disclose private message bodies in OS
notifications or general telemetry.

Ordinary private-message owner questions join the next digest; do not introduce
an immediate main-conversation or desktop fallback. Commission payment, delivery
confirmation and other transaction notices keep their existing independent
business routing and timing. Clarification messages do not authorize payments,
Order acceptance, fulfillment, account changes or new commitments.

## Execution boundary

Stop routing native Codex `pm_push` work through `MainSessionID`, `codex queue`,
`runCodexHiddenPM`, or `MainNoticeRunner`. Retain the verified main binding for
transaction notices and digest presentation only. Execute Codex decisions through
the existing background `exec --json` runner, keeping separate sessions per
account and peer conversation. Never resume the Desktop-owned main thread as a
background worker. Other ACP hosts keep their existing isolated execution.

Keep signed dispatch rules, exact request/identity binding, conversation history,
fresh automatic-reply permission checks and strict JSON decisions. The CLI
continues to own peer delivery and its receipt. Background process failure cannot
fall back to the visible main thread. An uncertain invocation or send stays
unknown and cannot be automatically replayed.

A host must provide an already authorized noninteractive execution channel;
Desktop owner connectivity alone does not establish this. If background Codex
execution is unavailable or asks for authorization, retain the job as blocked
and summarize the blocker at the next eligible owner cycle. Do not switch
accounts or weaken host permissions.

## Durable digest handoff

The existing journal removes completed message bodies. Add a bounded pending
digest record atomically with each terminal PM transition, preserving only the
minimum verified outcome needed for a summary. Do not reconstruct history from
already compacted jobs or mark a digest delivered when a peer reply is sent.
Use the existing account-scoped journal lock and capacity policy; never delete
lock files. Do not add another unbounded archive or copy host transcripts.

Each batch pins Home, server, Agent, binding revision, event range and a stable
batch ID. Watch and heartbeat/Feed share one bounded claim so they cannot display
the same batch concurrently. Claim one immutable batch before model invocation;
new outcomes wait for the next batch. Only an exact confirmed presentation
receipt advances the delivered cursor. A failed pre-submission attempt may
retry the same batch; an uncertain submitted presentation stays unknown for
receipt reconciliation and must not be blindly repeated. Restart retains claims,
pending outcomes and the last confirmed cursor.

Use the main hidden-input lane only for that aggregate presentation. Keep prompt
and metadata hidden and display the final digest only. If the host lacks a
verified result-only lane, let its next normal owner-facing cycle consume the
same claim instead. A busy main thread defers the batch. A confirmed handoff
prevents a later heartbeat/Feed cycle from presenting it again. An absent host
receipt is not proof of user visibility.

## Minimum implementation scope

1. Change native Codex PM session selection and runner routing together. Keep
   Commission main notices and existing main binding validation intact.
2. Add bounded digest outcomes, immutable batch claims and delivery cursors to
   the existing journal, with atomic terminal-state updates and restart recovery.
3. Add one due-batch hook to watch and the normal heartbeat/Feed completion path.
   Keep control-only, maintenance-only and pre-onboarding scopes unchanged.
4. Update signed communication/heartbeat rules for concise summaries, silence
   when there is no new outcome and explicit owner questions without automatic
   business execution. Bump changed Skill metadata and validate synchronization.

## Acceptance

Verify one real incoming PM produces exactly one peer reply and no new main
conversation prompt or result. Verify multiple peers aggregate into one due
digest, an empty window stays quiet, and watch plus heartbeat cannot duplicate
it. Cover revoked permissions, account changes, busy/unavailable hosts, crashes
before and after peer sends, and uncertain digest submissions. Verify Commission
payment and awaiting-confirmation notices retain their existing route. Protocol
fixtures and cross-compilation do not replace real host/UI acceptance.

## Current transport fix

The adapted PR #447 changes macOS PM input from visible queue messages to hidden
Desktop input. Its final per-message reply remains visible. It is not the above
background/digest implementation and must not be used as evidence that periodic
summaries or silent PM handling already work.
