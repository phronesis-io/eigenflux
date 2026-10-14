# Desktop notification boundary

`Notifier.Setup` installs the per-user integration. macOS requests OS permission;
Windows registers silently and never sends a setup/test notification.
Call it during explicit setup or startup of a subscribed desktop watch. `Check` checks permission without prompting;
`Show` submits one message and never requests permission. Success means the OS
accepted the notification, not that the user saw or clicked it. Focus modes and
disabled banners remain controlled by the OS. Callers own account isolation,
deduplication, bounded retries, and non-blocking event delivery.

`Message` carries an opaque ID, rendered title/body, and an HTTPS URL. Only loopback
HTTP is allowed for local development. Reject credential-bearing URLs, command
protocols, control characters, and oversized fields. Never include credentials
or order input text. Account checking belongs to the destination Console page.

Desktop copy lives in `cmd/watch_desktop_copy.go`; terminal summaries remain in
`cmd/order_notifications.go`. Both prioritize recognized payload event kind,
then envelope type, then order state. Desktop templates distinguish buyer/seller
and follow the revised columns of the [copy document](https://pcnlty6lw65j.feishu.cn/wiki/Mw3bwlX2siZlXjkgxx3cF21Pnyc).
Event deadlines use local time with a UTC offset; missing deadlines refer to the
order page. `cmd/watch_desktop_service.go` replaces `{service_name}` only in the
separate desktop lane using the existing order detail API's frozen contract title.
Validate the pinned Console URL, order ID and recipient role/account before use;
remove title controls, collapse whitespace, cap at 80 runes, and validate again.
Never fetch under intake's credential lock. Lookup failure retains the queue
entry for retry with `service_name_unavailable`; never show unresolved copy.
Lookup and OS submission share the desktop worker's ten-second timeout.
Old queued text without the placeholder needs no lookup. Provider errors, input
materials and credentials are never displayed.
Wallet credit means platform balance, not withdrawal. These notices do not call
Agents or initiate money operations. Keep account/notification-ID queue dedup
and persistence before ACK separate from order-execution dedup.

`SystemLanguage` selects copy from the current user's first preferred OS UI
language: `AppleLanguages` on macOS (a bounded two-second `defaults` read),
`GetUserPreferredUILanguages` on Windows. Any `zh` variant uses Simplified
Chinese; other languages, missing settings and read failures use English.
Ignore later preferences, terminal locale variables and CLI `--lang`. Cache the
result once per process; restart watch after changing OS language. Commission,
local blocker and test titles/bodies use this rule when created. Already queued
messages keep their original text. Terminal output keeps its existing language
selection. OS-owned permission dialogs remain controlled by the operating system.

## macOS 11+

`macos/` is a Swift AppKit application using `UNUserNotificationCenter`. It
receives private JSON files through Launch Services, returns atomic receipts,
and remains available for notification-click callbacks. The click delegate
validates the URL again and opens it using `NSWorkspace`, without a shell.
The helper is a background accessory without a Dock icon.

macOS displays the name `EigenFlux` and `Resources/EigenFlux.icns`. The build
converts `macos/Icon.png`, copied from the website's `public/apple-touch-icon.png`,
into the icon before signing. Keep the bundle ID and installation path stable
when changing branding. System permission descriptions and buttons are owned by
macOS; notification titles and bodies are supplied by the caller.

Build on macOS with Xcode Command Line Tools:

```
bash cli/scripts/build-notifier-macos.sh build/notification-package arm64
```

Use `amd64` for Intel. Ship the resulting `EigenFlux Notifications.app` beside
the real CLI executable. Test ZIP bundles preserve POSIX modes so the CLI and
helper remain executable after extraction. `Setup` verifies its code signature and copies it to
`~/Library/Application Support/EigenFlux/Notifications/`. The OS can request
notification permission once; denial must be changed in System Settings.
`cli/scripts/install-local.sh` builds and installs the matching helper before
running `notify setup`; setup failure is reported as an incomplete install.
`devbuild` only packages files: run `./eigenflux notify setup` from the complete
package directory, then `./eigenflux notify status`. CLI/Skills updates alone
do not request permission or restart an existing watch. Bare-binary CDN updates
do not distribute the helper; use the complete macOS package for helper updates.
Keep `permission_required` distinct from `permission_denied`: the former needs
setup, the latter needs System Settings. Receipt timeouts wrap
`context.DeadlineExceeded` for callers to report a separate setup timeout.
The helper only needs notification permission, not Accessibility, Apple Events,
Full Disk Access, or administrator access. CLI and helper run as the signed-in
desktop user, not a system daemon. A user-scoped OS lock serializes installation
across accounts and excludes reads during replacement. Upgrades request the
owned helper to quit through its private JSON protocol, verify its PID has
exited, replace the bundle, then launch the new code automatically. A helper
that cannot acknowledge shutdown is not silently replaced.

The build script defaults to ad-hoc signing for locally built test packages.
Production downloads require `EIGENFLUX_NOTIFIER_SIGN_IDENTITY` with a Developer
ID identity and notarization of the distributed package. Cross-compiling Go on
Linux alone cannot create this helper. Never remove quarantine or weaken
Gatekeeper to make an unsupported package appear usable.

## Windows 10/11

The CLI invokes the OS Windows PowerShell with a static embedded script and
decodes redirected UTF-8 stdin without a console code page. No external module, execution-policy override, or
administrator access is required. Setup creates only the current user's
`EigenFlux Notifications.lnk` Start-menu shortcut and
`HKCU\Software\Classes\AppUserModelId\ai.eigenflux.notifications` registration.
The shortcut contains EigenFlux's own AUMID and stub activator CLSID.

`0x80070490` while reading the registered sender's setting means first-use
state is absent. Setup and Check return `ErrPendingFirstNotification`; the CLI
reports `registered`, `pending_first_notification`, and unknown visibility,
without claiming permission is enabled. Watch may submit the first real message
in this state. Known disabled settings remain failures. Only explicit
`notify test` sends a test. The Windows installer runs silent setup and status.

`Observation` records setup/status/send outcomes, duration, platform, a hashed
notification ID, and fixed failure codes/stages/HRESULTs. It never records text,
URLs, credentials, or a claim that the user saw a banner. CLI commands and watch
persist at most 256 local observations for seven days per Home/server under
`notifications/`; `notify log` reads them. There is no remote upload. Recording
failure is reported separately and cannot retry an already submitted message.

Setup displays `EigenFlux`, writes the embedded brand PNG into
`%LOCALAPPDATA%\EigenFlux\Notifications`, sets the notification registration's
`IconUri` to the PNG. Content-hashed filenames
and atomic creation keep concurrent accounts from reading partial assets or
reusing an old cached icon. Keep the AUMID, activator, and shortcut path stable.
Both platforms reuse `macos/Icon.png`. The Windows CLI embeds it, so it needs no
adjacent resource or download. Verify the source icon and name on a new toast.

WinRT `ToastGeneric` notifications use `activationType="protocol"`. Clicking
opens the validated URL in the default browser even after the sender exits.
No COM activation server or message-supplied command is executed. XML escaping
and a deterministic 16-character tag preserve data boundaries and OS dedup.
Reject serialized Toast XML above the Windows 5 KB limit.
PowerShell/notification restrictions from organizational policy produce an
error; the CLI does not bypass them. Run under an interactive desktop login.

Load `ToastNotifier` and `NotificationSetting` explicitly and read `Setting`
through the WinRT type's reflected getter, avoiding PowerShell's nullable dynamic
property projection. Only known disabled values mean permission denial. A null
or unknown value reports `setting_unavailable`; it never means enabled or denied.
Helper failures expose only a fixed stage and numeric HRESULT, never raw stderr
or exception messages. Stages distinguish registration, WinRT loading, notifier
creation, setting reads, XML construction and OS submission. Windows-only tests
exercise the real getter/status gate with fake objects without touching OS state.

## Verification and references

Run `go test ./internal/desktopnotify` from `cli/`; cross-compile Windows tests
with `GOOS=windows GOARCH=amd64 go test -c -o ../build/notify.test.exe
./internal/desktopnotify`. Native acceptance requires each OS: allow permission,
send a test notification, click its body, verify the browser destination, deny
permission, and verify an explicit failure without blocking order processing.

- [Apple authorization](https://developer.apple.com/documentation/usernotifications/unusernotificationcenter/requestauthorization(options:completionhandler:))
- [Apple notification response](https://developer.apple.com/documentation/usernotifications/unusernotificationcenterdelegate/usernotificationcenter(_:didreceive:withcompletionhandler:))
- [Apple file delivery](https://developer.apple.com/documentation/appkit/nsapplicationdelegate/application(_:openfile:))
- [Microsoft desktop toast activation](https://learn.microsoft.com/pl-pl/windows/apps/design/shell/tiles-and-notifications/toast-desktop-apps)
- [Microsoft desktop toast registration](https://learn.microsoft.com/en-us/windows/win32/shell/quickstart-sending-desktop-toast)

Keep this implementation map updated whenever notification logic changes.

## Owned cleanup

CLI uninstall removes this integration only with explicit shared-user cleanup
confirmation. macOS setup records the installed bundle digest in `.bundle-owner`;
cleanup requires a matching digest, uses the helper quit protocol and removes
only the owned application and marker. Older installations without this evidence
remain intact until ownership is established. Windows cleanup verifies the
shortcut target/arguments, dedicated AUMID, activator and content-addressed icon
before removing them. Changed or unknown assets are preserved. This never edits
the OS notification permission store or removes other accounts' configuration.
