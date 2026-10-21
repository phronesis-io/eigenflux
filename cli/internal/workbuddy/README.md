# WorkBuddy local bridge

macOS-only standard ACP bridge. It asks the existing WorkBuddy Sidecar to create an isolated random session using native credential bootstrap. It never reads login credentials. Windows returns unsupported.

Supported methods: initialize, session/new, session/prompt, session/cancel. Input is forced to isMeta. Observed notifications/sessionUpdate messages become standard session/update notifications. Existing Desktop conversation binding and live GUI updates are unsupported; mainConversation and loadSession capabilities are not advertised. Failures do not retry prompts, replay work, or select another transport.

The control socket must belong to the current user and reject group/other writes. HTTP accepts only 127.0.0.1, without proxies or redirects. Optional `--tool` names restrict the worker’s available built-in tools using the official `--tools` argument; they do not grant permissions. Use only the tools required by the frozen contract. Scoped `--allow-tool` rules still control approval.

AllowedTools and EigenFlux environment variables come from caller configuration, never prompt metadata.

Cleanup uses the exact runtimeId of this instance. If the startup reply is lost, cleanup spends at most three seconds recovering runtimeId through native session.reconnect for this instance's random session, within a five-second cleanup deadline. ACP disconnection has a separate 500 ms limit so it cannot consume the runtime-kill budget. Native pending-create cancellation lacks a nonce/CAS guard and is not used. A create still pending beyond recovery remains unknown; no unguarded kill is attempted. No pre-existing runtime is taken over or killed.

Update this file whenever bridge logic changes.
