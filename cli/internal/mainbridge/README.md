# Local main-conversation bridges

Update this file when bridge behavior changes. `ServeHermes` implements only the
EigenFlux ACP `ai.eigenflux.mainConversation` extension. Standard ACP does not
provide hidden input. No model, session, daemon, or scheduler is created here.

## Hermes: experimental, not tested with a real host

Configure `watch acp-bridge --host hermes` in `main_command` only. Ordinary
seller checks, publication trials, fulfillment and PM decisions use the binding's
execution `command`; the Hermes native adapter invokes `hermes chat --quiet`.
The main bridge implements no standard ACP `session/new` or `session/prompt`.
Binding validation rejects this built-in bridge as an execution command,
including when its argv is split between `command` and `args`.
Initialization requires client extension version 1; ordinary execution clients
receive `hermes_main_execution_adapter_required` before a host connection.

Supply each route explicitly:

| Flag | Required value |
|---|---|
| `--owner-url` | `ws://` or `wss://`, `localhost` or a loopback IP, an explicit port from 1 through 65535, and exactly `/api/ws`; no user information, query, fragment or encoded alternative path |
| `--main-session-id` | The exact existing live owner's opaque ID; no UUID conversion, leading/trailing whitespace or control characters |
| `--workdir` | The owner's absolute, clean working directory, identical to the binding's canonical `workdir` |
| `--token-file` | Optional absolute path to a regular file, at most 8192 bytes, containing the raw gateway token with optional surrounding whitespace; no JSON wrapper |

The bridge adds the token to its private connection URL. Keep the supplied
`--owner-url` free of `?token=`. No credential or conversation discovery is
performed. Tokens and supplied route values never appear in diagnostics. Remote
URLs, redirects, and proxies are excluded. Configuration errors retain a fixed
code and explain the failed field's expected format. The ACP runner preserves
only allowlisted bridge diagnostics from stderr or RPC error messages; unrelated
output remains private. See the [operator configuration](../../../docs/dev/agent-dispatch.md#hermes-execution-and-main-notifications).

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
`hermes_config_test.go` covers field guidance and execution-client refusal;
`hermes_runner_test.go` exercises startup stderr and RPC diagnostics through a
real bridge subprocess and the production dispatch runner.
