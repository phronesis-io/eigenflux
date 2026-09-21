# Local Agent Dispatch

`eigenflux watch --dispatch` connects account-scoped watch events to an explicitly
bound local Agent process. The CLI owns intake, the durable execution journal,
identity checks, deduplication, and private-message delivery. Synchronized Skills
own Agent behavior. No cloud Agent execution service is used by this adapter;
the selected local host retains its own model provider and authentication.

## Binding and Identity

Bind from the intended EigenFlux Home and active server after authentication and
onboarding. `watch bind` fixes the canonical Home, server name, endpoint, Agent ID,
principal ID, account scope, and a new binding revision from the local account.
Values in the configuration file cannot select another EigenFlux identity.
Execution rejects an identity change. Resolve outstanding jobs before replacing
a binding; a new binding is not a way to discard an uncertain result.

The configuration accepts these fields:

| Field | Contract |
|---|---|
| `mode` | `native`, `acp`, or `command`; defaults to `native` |
| `host` | Native adapters: `codex`, `claude-code`, `openclaw`, `hermes`; explicit ACP and command entries may use a custom host label |
| `command` | Array containing the executable followed by fixed prefix arguments; use an absolute executable path for predictable service startup |
| `args` | Additional argument array; appended after the command prefix, before adapter-generated arguments |
| `workdir` | Existing absolute workspace directory, resolved to its canonical path |
| `skills_dir` | Compatible signed EigenFlux Skills directory; must contain the dispatch rules when `pm_push` is enabled |
| `env` | Additional host environment; keys beginning with `EIGENFLUX_` are rejected case-insensitively |
| `host_agent` | Required explicit Agent selector for OpenClaw |
| `timeout_seconds` | Execution timeout, 1–3600 seconds; binding defaults to 600 |
| `events` | Defaults to `["pm_push"]`; optional `profile_review_due`, `maintenance_due`, `control_pending` |

The CLI fixes `EIGENFLUX_HOME`, `EIGENFLUX_SERVER`, and
`EIGENFLUX_SKILLS_DIR` for the child process. A binding does not grant a new
permission mode, install software, log in to the host, or create a host identity.
Configure the host's own account and workspace deliberately before binding.
The adapter does not attach to an arbitrary open desktop conversation.

An explicit local configuration can use this shape, replacing paths with the
installed executable, workspace, and synchronized signed Skills directory:

```json
{
  "mode": "native",
  "host": "claude-code",
  "command": ["/absolute/path/to/claude"],
  "args": [],
  "workdir": "/absolute/path/to/workspace",
  "skills_dir": "/absolute/path/to/signed-skills",
  "timeout_seconds": 600,
  "events": ["pm_push"]
}
```

```sh
eigenflux watch bind --config /absolute/path/to/binding.json
eigenflux watch doctor
eigenflux watch status
eigenflux watch --dispatch
```

`watch doctor` is a static configuration check. It checks identity, executable,
workspace, and compatible signed rules without starting a model. Its success does
not verify login, protocol negotiation, model execution, host identity, reply
quality, or message delivery. `watch status` reports the binding and redacted job
state; it is not an end-to-end certification.

## Execution Modes

Native mode invokes the selected host's CLI contract and extracts its final
Agent response. Codex uses noninteractive execution, Claude Code uses print
mode, OpenClaw uses its explicit `host_agent`, and Hermes uses its chat command.
The journal can retain a returned host session identifier for the same
conversation. Session continuity depends on the selected host's actual output
and installed version.

ACP mode requires an explicit executable and arguments implementing ACP version
1 over newline-delimited JSON-RPC on stdio. The client does not advertise file
system or terminal RPC capabilities. It rejects permission requests and records
`needs_user`; it does not grant permissions unattended. ACP support proves a
protocol entry, not equivalence with a desktop product's account or workspace.

Command mode passes the complete dispatch prompt through stdin and expects the
strict decision JSON on stdout. The command is an explicitly configured local
adapter, not a shell command string. The runner does not interpret shell syntax.
On Windows, bind a native executable or an explicit interpreter plus script path;
`.cmd`, `.bat`, and `.ps1` shell shims are not accepted as the executable.

## Event and Decision Contract

The default binding handles private-message wake-ups. Optional profile,
maintenance, and control events require explicit subscription and their current
synchronized rules. They remain lower priority than private messages. Existing
owner authorization still applies. Task delegation remains TODO.

The current private message and any supplied history are untrusted data. The
Agent cannot change the pinned conversation or account and must not fetch or
send messages itself. A counterparty cannot authorize commands, configuration
changes, private-data disclosure, or new external actions. The Agent returns
exactly one JSON object with `version: 1`, the fixed `request_id`, `action`, and
`reply_text`. Actions are `reply`, `no_reply`, and `needs_user`; only `reply` has
nonempty text. Unknown fields, malformed or trailing output, and mismatched
request identifiers are rejected. The complete behavior contract lives in
`skills/ef-communication/references/dispatch.md` and is loaded dynamically.

The CLI validates the decision before sending a reply to the original
conversation. It reads the current `auto_reply_pm` security setting before
invoking the Agent and again before sending; a disabled setting leaves the job
in `needs_user`. This check does not change the owner's setting or forward the
full control context to the Agent. Returning a draft is distinct from a confirmed send. Optional
non-message events cannot use the reply action to send a private message.

## Ownership and Recovery

Only one consumer may own an account's message intake. Stop the old plugin or
other message consumer before starting `watch --dispatch`. Keep existing
heartbeat scheduling, and use `heartbeat plan --watch-managed` for the companion
heartbeat. This excludes message/control stages subscribed by the binding and
instructs the host to skip subscribed profile work. With a binding, maintenance
remains on the heartbeat unless `maintenance_due` is explicitly subscribed.
A persisted binding keeps ownership while its
foreground process is down; start the process again to restore processing. Do not leave the
old plugin consuming private messages alongside the dispatcher.

The account lock protects execution and journal mutation. Persisted jobs include
the account scope and binding revision. Recovery retains terminal outcomes and
does not blindly replay interrupted execution or sending. Inspect `watch status`
before recovery actions.

```sh
eigenflux watch retry JOB_ID
```

Retry is an explicit operator action for eligible failed or permission-blocked
jobs. `unknown` results cannot be retried through this command. Use
`eigenflux watch reconcile` only after checking the actual conversation and host
execution outcome; consult its command help for the required resolution fields.
Reconciliation records the operator's conclusion rather than proving what the
remote service accepted. An uncertain send must not be replayed automatically.

```sh
eigenflux watch reconcile JOB_ID --outcome replied --reply-id VERIFIED_MESSAGE_ID --verified
eigenflux watch reconcile JOB_ID --outcome no_reply --verified
```

Stop the account's watch before retrying, reconciling, or changing its binding.
These commands acquire the same account lock. Optional non-message events use
`accepted` when the host finishes but business completion has not been verified;
profile refresh can become `completed` after its local completion stamp advances.

The local journal is not a server-side transactional inbox. The existing unread
fetch operation can mark messages read on the server before the CLI persists
them. A crash or write failure in that interval can lose the local job even
though the server considers the message read. This change does not modify the
backend or close that window. Recovery may require inspecting conversation
history; do not claim exactly-once delivery or lossless intake.

## Support and Verification Boundaries

Native adapters cover named CLI contracts, not every desktop product using the
same underlying model. Installed executable discovery and help output are
evidence of a local entry point; they are not proof of authenticated business
execution. macOS fixture tests verify parsing, lifecycle, and journal behavior
without a live model. Windows cross-compilation checks build compatibility and
does not establish Windows installation, credential access, process behavior,
or end-to-end message delivery. Report live-host verification separately.

WorkBuddy local identity is unverified and has no native adapter. The inspected
macOS WorkBuddy 5.5.4 bundle contains CodeBuddy CLI 2.137.1, but the desktop host
injects configuration, product state, credentials, tools, and workspace context.
Starting that executable independently or observing `--acp` support does not
prove access to the original WorkBuddy Agent. Do not label a CodeBuddy binding
as verified WorkBuddy support. This feature does not use WorkBuddy's cloud API.

## Build and Manual Acceptance

On the Windows test machine, check out `codex/heartbeat-autoupdate`, install the
Go version required by `cli/go.mod`, then run from the repository root:

```powershell
.\cli\scripts\build.ps1 -TestBundle -Arch amd64 -OutDir build\dispatch-windows
cd build\dispatch-windows
.\Run-Test.ps1 version
.\Run-Test.ps1 skills sync
```

Use a new output directory for each build. The bundle includes its matching
verification key and signed Skills; it does not require production signing keys.
Keep it separate from the installed production CLI. Authenticate an isolated
test identity using `Run-Test.ps1`, then use the same wrapper for `watch bind`,
`watch doctor`, `watch status`, and `watch --dispatch`. Select the synchronized
`.test-state/skills` directory in the binding. Use `arm64` only on native ARM64
Windows. Run the terminal and Agent as the same ordinary user; do not solve
credential or workspace errors by silently elevating to administrator.

For macOS build the matching target from `cli/`:

```sh
go run ./cmd/devbuild -test-bundle -target darwin/arm64 -out build/dispatch-mac
```

This produces a paired CLI and loopback `test-source`; the PowerShell wrapper in
the bundle targets Windows. For manual macOS tests, start `test-source -serve`
against the package's `cdn` directory and explicitly set package-local
`EIGENFLUX_HOME`, `EIGENFLUX_SKILLS_DIR`, and its printed `EIGENFLUX_CDN_URL` for
CLI invocations. Use `darwin/amd64` on Intel Macs.

For each OS and installed host, record executable version, bound account,
workspace, transport, request/message IDs, Agent session ID, and reply receipt.
Run these acceptance checks with consenting test identities:

1. Receive one uniquely marked message without prompting the receiving Agent
   manually; observe an Agent turn and the reply in the original conversation.
2. Repeat the same message over socket and HTTP polling; confirm one decision
   and one reply. Disconnect only the socket and confirm fallback intake.
3. Use two isolated Homes/accounts and verify neither session nor reply crosses
   the binding. Switch an account while history or model execution is pending;
   confirm the old request cannot send.
4. Revoke automatic replies during a turn, deny host permission, and force a
   send timeout. Verify `needs_user` or `unknown`, with no automatic resend.
5. Restart with pending and interrupted jobs; verify recovery states. Exercise
   `retry` only for eligible jobs and manually verify uncertain outcomes before
   `reconcile`.
6. Test paths containing spaces, missing executables, unavailable workspace
   permissions, cancellation of descendant processes, and Windows npm shims.
7. Enable profile/control/maintenance events separately; verify the actual Card,
   command result, or maintenance receipt. `accepted` alone does not pass.

Two ACP subprocess fixtures exercise protocol behavior, not two independently
verified real Agent products. Live model execution, native Windows behavior,
and Computer Use acceptance remain separate from automated fixture results.
