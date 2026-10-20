# Local main-conversation bridges

Update this file when bridge behavior changes. `ServeHermes` implements only the
EigenFlux ACP `ai.eigenflux.mainConversation` extension. Standard ACP does not
provide hidden input. No model, session, daemon, or scheduler is created here.

## Hermes: experimental, not tested with a real host

The user supplies a loopback `/api/ws` URL, an exact live session ID, an absolute
working directory, and optionally an explicit raw-token file. No credential or
conversation discovery is performed. Tokens never appear in ACP output. Remote
URLs, URL credentials, redirects, and proxies are excluded.

`hermes.go` follows official Hermes commit
[`065ea31ad3550342da3a0c36f0310f5577c483ce`](https://github.com/NousResearch/hermes-agent/tree/065ea31ad3550342da3a0c36f0310f5577c483ce):

- `tui_gateway/ws.py`: `/api/ws`, `event` frames, `gateway.ready`.
- `methods_session.py` and `session_registry.py`: `session.active_list` identifies
  an existing owner; `session.activate` with `omit_messages:true` checks the exact
  session and working directory without requesting conversation history.
- `session_transports.py`: activation adds a fanout subscriber; disconnect removes
  only this bridge's subscriber.
- `methods_prompt.py`: `prompt.submit` accepts `display_kind:"hidden"` and
  `queued:true`; an admitted inline turn returns `status:"streaming"` and
  `user_row_id`. A racing busy turn queues instead of steering the current turn.
- `prompt_turn.py` and `agent/persisted_turn_receipt.py`: success requires a
  `message.complete` for the bound session, `status:"complete"`, a complete
  persisted receipt matching that exact user row, and a committed final row.

Initialization checks `desktop_contract == 8`, retains any reported version for
subsequent consistency checks, and rejects the unaudited session-authority path.
The contract number is a protocol gate, not an exact build fingerprint or proof
of hidden UI behavior. A different build retaining contract 8 may be incompatible.
Real-host verification of the installed build, hidden input, final-only UI, and
unchanged existing viewer remains required before reporting host verification.

`inspect` never submits a prompt. `notify` checks structured idle state again,
then submits once. Busy is refused. Queued, missing/partial receipts, host approval,
disconnect, or timeout are unconfirmed; the bridge never retries, cancels,
interrupts, resumes, or creates another session. An already admitted or queued
host turn may finish after this bridge closes. One `notify` ends the connection.

`hermes_test.go` uses isolated loopback WebSocket fixtures, including early
completion, exact receipts, incompatible targets, busy/queued races, approval,
timeouts, token redaction, and forbidden fallbacks. It calls no real host or model.
