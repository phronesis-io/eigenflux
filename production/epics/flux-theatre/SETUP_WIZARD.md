# First-Run Setup Wizard — integration notes for the lead

**Status:** implemented, isolated slice. Owned files live only under
`fluxspace_client/lib/setup/**`, `fluxspace_client/test/setup/**`,
`fluxspace_client/lib/pages/home_page.dart`,
`fluxspace_client/lib/services/token_store.dart` and this document.

This wizard removes the need for a human to know the Hub base URL, the
`HUB_OWNER_TOKENS` bearer token, or the `POST /hub/companions` binding call.
It probes the real `/health` endpoint, reads the `auth` field, and shows one
screen per decision. It does **not** edit `theatre_page.dart`; the exact hook to
add there is in [Integration hook for `TheatrePage`](#integration-hook-for-theatrepage).

---

## 1. The two real Hub modes

Everything below is read from the Go backend, not guessed:

- `hub/api/health.go` — `GET /health` answers (always `200`, exempt from auth):

  ```json
  { "status": "ok", "service": "fluxspace-hub", "auth": "enabled" }
  ```

  `auth` is exactly `"enabled"` or `"disabled"`; it is set by
  `Server.authenticated()` (`hub/api/auth.go`), i.e. `len(s.OwnerTokens) > 0`.
  `/health` is on the `publicExempt` list, so it works **before** a token exists.

- `hub/main.go` — `ownerTokens()` reads `HUB_OWNER_TOKENS` (a JSON object of
  `owner_id -> bearer token`) and refuses to start with no tokens unless
  `HUB_ALLOW_UNAUTHENTICATED=1` is set. With tokens configured, `auth` is
  `"enabled"`; with the explicit open mode, `auth` is `"disabled"`.

- ADR-0009 §2.1 / §2.2 — when enabled, `owner_id` is derived **from the token**
  (a body `owner_id` mismatch is `OWNER_MISMATCH`); `POST /hub/companions` binds
  a companion to that owner and is idempotent. An open Hub has no owner, so
  `hub/api/companions.go` answers `401 AUTH_REQUIRED` for a bind — the wizard
  says so explicitly and lets the user skip the binding step.

The wizard surfaces this honestly: `HubHealth.checkSucceeded` is true **only**
on a `200` + `status: "ok"` + a recognised `auth` value. A missing/unrecognised
`auth` field, a non-200, a wrong `status`, or a transport error is a failed
check with an inline message — it can never be presented as success.

---

## 2. Public API

All symbols are under `package:fluxspace_client/setup/...`.

### `hub_probe.dart`

| Symbol | Purpose |
|---|---|
| `const String kDefaultHubUrl = 'http://127.0.0.1:8095'` | Prefill for the URL field (mirrors `HubClient.defaultBaseUrl`). |
| `enum HubAuthMode { enabled, disabled, unknown }` | Parsed `auth` field; `unknown` is never a success. |
| `HubHealth` | Parsed `/health` result. `HubHealth.fromResponse(int, Map)` reads the real JSON; `HubHealth.transport(Object)` is a connection failure. Getters: `reachable`, `statusCode`, `status`, `service`, `auth`, `error`, `failureMessage`, `checkSucceeded`, `authLabel`. |
| `BindingCheck` | `.success()` / `.failure({code, message})`, with `ok`, `code`, `message`. |
| `abstract class HubProbe` | Injected network seam: `Future<HubHealth> health(String baseUrl, {String? ownerToken})` and `Future<BindingCheck> bindCompanion({baseUrl, ownerToken, companionId})`. |
| `DioHubProbe` | Only production `HubProbe`; uses `dio`, parses `/health`, POSTs `/hub/companions`, maps `error.code` from refusals. Catches its own transport errors. |

### `setup_state.dart`

| Symbol | Purpose |
|---|---|
| `enum SetupStep { hubUrl, connection, ownerToken, identity, binding, save, done }` | The seven logical steps. |
| `SetupResult` | Handed back on completion. Fields: `hubUrl`, `ownerToken`, `ownerId`, `companionId`, `auth`, `bindingChecked`, `bindingOk`. Getters: `authLabel`, `bindingVerified` (true only when a check ran **and** succeeded). |
| `SetupState extends ChangeNotifier` | The state machine. |

`SetupState` constructor — both side effects injected, so tests never open a
socket or keychain:

```dart
SetupState({
  required HubProbe probe,
  required TokenStore store,
  String initialHubUrl = kDefaultHubUrl,
})
```

Key members:

- `List<SetupStep> steps` — dynamic sequence; the `ownerToken` step exists **only**
  when `requiresToken` (a successful check with `auth == enabled`). Enabled flow
  = 7 steps, disabled flow = 6.
- `Future<bool> next()` — validates the current step and advances (or stays with
  an inline `error`). On `save` it performs the save.
- `bool back()` — previous step, respecting the conditional token step.
- `Future<void> checkConnection()` / `Future<void> checkBinding()` — **the only
  network calls**, triggered by explicit buttons; navigation never calls them.
- `Future<SetupResult?> save()` — validates again, writes the four keys, moves to
  `done`, returns the result (or `null` + inline error on refusal/failure).
- Pure validators: `validateUrl`, `validateToken`, `validateOwnerId`,
  `validateCompanionId` (return `String?` inline message).
- Setters `setHubUrl` / `setOwnerToken` / `setOwnerId` / `setCompanionId`.
  Editing the URL invalidates a stale connection result; editing the companion
  id invalidates a stale binding result.
- Getters: `step`, `error`, `health`, `checking`, `binding`, `bindingChecked`,
  `bindingVerified`, `connectionSucceeded`, `requiresToken`, `busy`, `isFirst`,
  `isLast`, `stepIndex`, `stepCount`, `result`.

### `setup_wizard.dart`

| Symbol | Purpose |
|---|---|
| `SetupWizard` | `StatefulWidget`. `const SetupWizard({Key? key, SetupState? state, void Function(SetupResult)? onCompleted})`. When `state` is omitted it builds a production `SetupState(probe: DioHubProbe(), store: SecureTokenStore())`. **No keychain read happens at construction**, so a smoke render is safe under `flutter test`. All `TextEditingController`s are disposed. Layout uses `Wrap`/`Flexible`, a scrollable body and full-width error banners — no unbounded `Row`. |
| `static Future<SetupResult?> SetupWizard.open(BuildContext context, {SetupState? state})` | Pushes the wizard and resolves with the saved `SetupResult` (or `null` if dismissed). |
| `onCompleted` | Optional callback fired with the same `SetupResult` when Done is tapped. |

`SetupResult` is handed back **twice**: returned from `SetupWizard.open` (i.e.
`Navigator.pop`) and passed to `onCompleted`. HomePage currently uses the
`open` result and shows a confirmation `SnackBar`.

### Entry point — `pages/home_page.dart`

`HomePage` now has a clearly visible `Set up Hub` filled button and an AppBar
icon action, both calling `SetupWizard.open(context)`. It does **not** change the
tab count, `AppRoutes.order`, `AppShell`, or `FluxSpaceApp`'s constructor.

---

## 3. Saved keys (`services/token_store.dart`)

Two new keys were added. Every pre-existing key and method signature is
unchanged; `MemoryTokenStore` and `SecureTokenStore` are untouched and keep
working.

| Key | Constant | Written value |
|---|---|---|
| `fluxspace.hub.url` | `TokenKeys.hubUrl` (**new**) | trimmed Hub base URL |
| `fluxspace.hub.owner_token` | `TokenKeys.ownerToken` (existing) | bearer token (empty string on an open Hub) |
| `fluxspace.hub.owner_id` | `TokenKeys.ownerId` (existing) | owner id |
| `fluxspace.hub.companion_id` | `TokenKeys.companionId` (**new**) | companion id |
| `fluxspace.hub.seat.<sessionId>` | `TokenKeys.seatToken(sessionId)` (existing, unchanged) | per-seat stream token (session controller) |

`save()` writes exactly the first four. A `TokenStore.write` failure (e.g. an
unavailable platform keychain) is caught and surfaced as an inline error; the
wizard never reports success in that case.

---

## 4. Integration hook for `TheatrePage`

`lib/pages/theatre_page.dart` currently owns its own `TextEditingController`s
and builds its `HubClient` from them:

- `theatre_page.dart:37` `_baseUrl = TextEditingController(text: HubClient.defaultBaseUrl)`
- `theatre_page.dart:38` `_token = TextEditingController()`
- `theatre_page.dart:39` `_ownerId = TextEditingController(text: 'owner_alpha')`
- `theatre_page.dart:40` `_companionId = TextEditingController(text: 'comp_alpha')`
- `theatre_page.dart:53-54` `initState` builds `SessionController(api: HubClient(), tokens: SecureTokenStore())`
- `theatre_page.dart:78-91` `_connect()` rebuilds `HubClient(baseUrl: _baseUrl.text, ownerToken: _token.text, ownerId: _ownerId.text)` and calls `_controller.connect(companionId: _companionId.text)`.

The wizard already persists the four values to `TokenStore` and returns them as
`SetupResult`. **The lead must wire the following into `TheatrePage`** (this
slice does not touch that file):

1. **Get the values to `TheatrePage`.** Two supported routes, choose one:
   - **Shared notifier (recommended for live hand-back):** lift a
     `ValueNotifier<SetupResult?>` (or the `SetupState` itself) above `AppShell`
     and pass it to both `HomePage` and `TheatrePage`. In `HomePage._openSetup`,
     after `final result = await SetupWizard.open(context)`, set
     `notifier.value = result`. `TheatrePage` listens (e.g. `ValueListenableBuilder`
     or `addListener`) and applies the values below. `SetupWizard` also accepts
     an `onCompleted` callback if the lead prefers to push the result down
     without a shared object.
   - **Read persisted keys:** in `TheatrePage.initState`, asynchronously read
     `TokenKeys.hubUrl`, `TokenKeys.ownerToken`, `TokenKeys.ownerId`,
     `TokenKeys.companionId` from a `TokenStore` and prefill the controllers.
     This covers "restart the app" but not "setup finished while TheatrePage is
     already alive" by itself — pair it with the notifier for the live case.

2. **Apply a `SetupResult r` to the four controllers** (the exact hook):

   ```dart
   void applySetup(SetupResult r) {
     _baseUrl.text    = r.hubUrl;        // theatre_page.dart:37
     _token.text      = r.ownerToken;    // theatre_page.dart:38 ('' for an open Hub)
     _ownerId.text    = r.ownerId;       // theatre_page.dart:39
     _companionId.text = r.companionId;  // theatre_page.dart:40
     // Then rebuild the client/controller with these values, i.e. call the
     // same construction as theatre_page.dart:81-85, and re-run connect if a
     // session was already live:
     _connect();                        // theatre_page.dart:78
   }
   ```

   `_connect()` already reads exactly these four controllers, so assigning their
   `.text` and calling it is enough to rebind `HubClient` and
   `SessionController` to the configured Hub.

3. **Keep tests unaffected.** `theatre_page_test.dart` and
   `theatre_integration_test.dart` inject a `SessionController` and
   `MemoryTokenStore`; they must still pass unchanged. Therefore:
   - make the `TokenStore`/`SetupResult` source **injectable** on `TheatrePage`
     (e.g. `TheatrePage({super.key, this.controller, this.setupResult, this.tokens})`),
     defaulting to `SecureTokenStore()` in production only; and
   - do **not** call `SecureTokenStore.read()` in `initState` when a controller
     is injected, because `flutter test` cannot reach the platform keychain.
     Guard any keychain read behind `widget.controller == null` and catch its
     failure.

4. Do **not** add a fifth bottom-navigation tab or change `AppRoutes.order` /
   `AppShell` / `FluxSpaceApp`'s default constructor — `scaffold_test.dart`
   asserts four destinations.

---

## 5. Honesty guarantees (tested in `test/setup/`)

- A failed `/health` check, an unknown `auth` field, a non-200, a wrong
  `status`, or a transport error blocks `next()` on the connection step and
  blocks `save()` — no `done`, no writes, no `SetupResult`.
- `auth = enabled` adds the token step and requires a non-empty token;
  `auth = disabled` omits the token step and saves with an empty token.
- The optional companion binding check records `bindingOk` truthfully: a failed
  or skipped check yields `bindingVerified == false` in `SetupResult` and is
  shown as "not verified" on the done screen.
- `next()`/`back()` make **no** network calls; only `checkConnection()` and
  `checkBinding()` (explicit button presses) do.
- A failing `TokenStore.write` is surfaced as an inline error, never success.

## 6. Verification

```
flutter analyze                         -> No issues found!
flutter test test/setup/                 -> 25/25 passed
flutter test (full suite)               -> 193 passed, 2 skipped (pre-existing)
```

All `flutter test` commands were run with
`env -u http_proxy -u https_proxy -u HTTP_PROXY -u HTTPS_PROXY NO_PROXY=localhost,127.0.0.1`.
No dependency was added and `flutter pub get` was never run.
