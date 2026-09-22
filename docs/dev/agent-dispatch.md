# Local Agent Dispatch

`watch --dispatch` sends account events to a bound local Agent; the CLI delivers replies.
Read and update the [implementation map](../../cli/internal/dispatch/README.md) with every logic change.

## Configure and run

Authenticate/onboard the intended EigenFlux account and log in to the host first. Bind from that account's Home/server. The CLI pins identity; resolve outstanding jobs before rebinding. Host sessions are separate from existing desktop conversations.

Write a binding JSON file using these fields:

| Field | Value |
|---|---|
| `mode` | `native` (default), `acp`, or `command` |
| `host` | Native: `codex`, `claude-code`, `openclaw`, `hermes`; custom label for ACP/command |
| `command` | Executable + fixed prefix arguments as an array; prefer an absolute executable path |
| `args` | Additional argument array, before native adapter arguments |
| `workdir` | Existing absolute workspace path |
| `skills_dir` | Signed compatible Skills directory containing the dispatch rule |
| `env` | Host environment; `EIGENFLUX_*` keys forbidden; Windows keys ignore case and must be unique |
| `host_agent` | Required OpenClaw Agent selector |
| `timeout_seconds` | Default 600; range 1–3600 |
| `events` | Default `["pm_push"]`; optional `profile_review_due`, `maintenance_due`, `control_pending` |

```json
{
  "host": "claude-code",
  "command": ["/absolute/path/to/claude"],
  "workdir": "/absolute/path/to/workspace",
  "skills_dir": "/absolute/path/to/signed-skills"
}
```

```sh
eigenflux watch bind --config /absolute/path/to/binding.json
eigenflux watch doctor
eigenflux watch --dispatch
```

`doctor` checks configuration only; `status` shows redacted execution state. Neither proves live Agent execution. CLI checks fresh `auto_reply_pm` before invoking the Agent and before sending; disabled permission yields `needs_user`.

ACP requires v1 newline-delimited JSON-RPC over stdio; permission requests yield `needs_user`. Command mode reads the prompt from stdin and returns the [decision contract](../../skills/ef-communication/references/dispatch.md) on stdout. Use fixed argv, not a shell string. Windows `.cmd`, `.bat`, `.ps1` executables require an explicit interpreter/entry point. Oversized native command lines yield `needs_user`; use ACP/command stdin for large prompts. OpenClaw uses `--local` with isolated sessions: stop the Gateway sharing its state directory or configure a separate local state directory. If an earlier dispatch build used OpenClaw, resolve outstanding jobs and rebind before restarting to discard shared main-session references.

## Ownership and recovery

Stop the old account's plugin/message consumer before starting dispatch. Keep the companion `heartbeat plan --watch-managed`: it skips subscribed work and retains maintenance unless `maintenance_due` is enabled. Restart a stopped watch to resume its ownership; bindings do not automatically fall back to another consumer.

Stop watch before bind/retry/reconcile. Retry only `failed` or `needs_user`; inspect host/business results before reconciling `unknown/failed/needs_user` work:

```sh
eigenflux watch status
eigenflux watch retry JOB_ID
eigenflux watch reconcile JOB_ID --outcome replied --reply-id VERIFIED_MESSAGE_ID --verified
eigenflux watch reconcile JOB_ID --outcome no_reply --verified
# Optional events in unknown/failed/needs_user:
eigenflux watch reconcile JOB_ID --outcome completed --verified
eigenflux watch reconcile JOB_ID --outcome failed --verified
```

Reconciliation records the operator's conclusion, including PMs already handled manually. Explicit profile retry bypasses its prior claim cooldown; ordinary scheduled reviews keep that cooldown. `unknown` is never automatically resent. Optional events without completion evidence remain `needs_user`; inspect before retry. Reconcile as `failed` only after confirming work did not complete; retry stays explicit. Legacy `accepted` records are unverified.

## Build isolated test packages

Use `codex/heartbeat-autoupdate`, the Go version in `cli/go.mod`, and a fresh output directory. Packages contain paired verification keys and signed Skills; keep them separate from production installs. Run CLI and Agent as the same ordinary user.

Windows, from the repository root:

```powershell
.\cli\scripts\build.ps1 -TestBundle -Arch amd64 -OutDir build\dispatch-windows
cd build\dispatch-windows
.\Run-Test.ps1 version
.\Run-Test.ps1 skills sync
```

Use `arm64` on native ARM64 Windows. Authenticate a test identity and run the watch commands through `Run-Test.ps1`; bind the absolute `.test-state/skills` path.

macOS, from `cli/`:

```sh
go run ./cmd/devbuild -test-bundle -target darwin/arm64 -out build/dispatch-mac
```

Use `darwin/amd64` on Intel. Start the package's `test-source -serve` against its `cdn` directory. Set package-local `EIGENFLUX_HOME`, `EIGENFLUX_SKILLS_DIR`, and the printed `EIGENFLUX_CDN_URL` for CLI calls. `Run-Test.ps1` is Windows-only.

## Acceptance and limits

For each OS/host, record version, account, workspace, message/session IDs and reply receipt. Use consenting test identities to verify:

1. Incoming PM starts an Agent turn and produces a reply without manual prompting.
2. Duplicate WS/poll delivery produces one reply; socket outage allows fallback intake.
3. Two accounts stay isolated; switching during history/model work prevents the old reply.
4. Permission revocation, denied host permission and send timeout yield `needs_user`/`unknown`, without automatic resend.
5. Restart/retry/reconcile preserve recorded outcomes; test spaces in paths, missing executables, workspace permissions, descendant cancellation and Windows shims.
6. Explicit profile/control/maintenance subscriptions produce the actual Card/command/receipt; process exit alone fails acceptance. Check migration preserves `--watch-managed` and non-PM bindings leave messages unread.

Server fetch marks messages read before local persistence: crashes/write failures in that window can lose jobs. Exactly-once/lossless delivery is not guaranteed.

WorkBuddy local identity is unverified; no native adapter or cloud API integration. CodeBuddy's embedded CLI is not proof of WorkBuddy identity access. Task delegation is TODO. Fixture tests and cross-compilation do not verify live models, native Windows behavior or Computer Use; two ACP fixtures are not two verified real hosts.
