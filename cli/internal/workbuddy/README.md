# WorkBuddy local bridge

Automatic Windows setup explicitly allows the native `PowerShell` tool alongside
Read, Write, Edit and Bash. On machines without Git Bash, WorkBuddy executes shell
work through PowerShell; the Bash allow rule does not cover it. The bridge still
uses `--permission-mode default` and forwards every remaining permission request.

Windows discovery distinguishes a closed Desktop (`workbuddy_host_not_running`)
from a running Desktop whose lazy Sidecar is absent (`workbuddy_sidecar_not_ready`).
For the latter, open a local conversation in WorkBuddy to initialize its runtime and retry setup. A discovery
file read failure and a failed process query have separate errors. These failures
preserve the existing task and binding; no WBIPC ticket, independent ACP login,
or synthetic PID file is used to bootstrap the worker. Automatic cold-start
bootstrap is unavailable until the host exposes a supported startup mechanism.

macOS and Windows standard ACP bridge. It asks the existing WorkBuddy Sidecar to create an isolated random session using native credential bootstrap. It never reads login credentials. Windows discovers the running host from `%TEMP%/wb/<sha1(configDir)[:12]>/sidecar.pid` and connects to its scoped named pipe. Its server must have the current Windows user SID and logon session. The host must already be running and logged in; no standalone ACP fallback or credential extraction is used. Windows runtime behavior awaits real-machine verification.

Supported methods: initialize, session/new, session/prompt, session/cancel. Input is forced to isMeta. Observed notifications/sessionUpdate messages become standard session/update notifications. Existing Desktop conversation binding and live GUI updates are unsupported; mainConversation and loadSession capabilities are not advertised. Failures do not retry prompts, replay work, or select another transport.

On macOS the control socket must belong to the current user and reject group/other writes. Windows discovers the packaged CLI from running WorkBuddy process paths, then standard installation roots; the path probe emits UTF-8 so Chinese user/install directories remain valid. It prefers WorkBuddy-managed Node versions in numeric order, skips incomplete extraction directories, then checks PATH. Host runtime discovery follows Node’s TEMP-before-TMP precedence. `Discover` resolves configuration; `Probe` creates and cleans up one isolated native runtime, initializes ACP and loads the new session without model input. Windows system environment needed for subprocesses is preserved alongside the explicit EigenFlux routing allowlist. HTTP accepts only 127.0.0.1, without proxies or redirects. Optional `--tool` names restrict the worker’s available built-in tools using the official `--tools` argument; they do not grant permissions. Use only the tools required by the frozen contract. Scoped `--allow-tool` rules still control approval.

AllowedTools and EigenFlux environment variables come from caller configuration, never prompt metadata.

Cleanup uses the exact runtimeId of this instance. If the startup reply is lost, cleanup spends at most three seconds recovering runtimeId through native session.reconnect for this instance's random session, within a five-second cleanup deadline. ACP disconnection has a separate 500 ms limit so it cannot consume the runtime-kill budget. Native pending-create cancellation lacks a nonce/CAS guard and is not used. A create still pending beyond recovery remains unknown; no unguarded kill is attempted. No pre-existing runtime is taken over or killed.

Update this file whenever bridge logic changes.

The dispatch runner removes progress text and one final Markdown JSON fence for successful WorkBuddy ACP results only. The prefix must contain no earlier fence or JSON object/array start. The enclosed value must be a valid JSON object; multiple blocks, trailing prose and extra values remain invalid. Exact object bytes continue through strict business field, duplicate-key and identity validation. No model call is repeated.
