# Desktop notification boundary

`Notifier.Setup` installs the per-user integration and requests OS permission.
Call it during explicit setup or startup of a subscribed desktop watch. `Check` checks permission without prompting;
`Show` submits one message and never requests permission. Success means the OS
accepted the notification, not that the user saw or clicked it. Focus modes and
disabled banners remain controlled by the OS. Callers own account isolation,
deduplication, bounded retries, and non-blocking event delivery.

`Message` carries an opaque ID, fixed title/body, and an HTTPS URL. Only loopback
HTTP is allowed for local development. Reject credential-bearing URLs, command
protocols, control characters, and oversized fields. Never include credentials
or order input text. Account checking belongs to the destination Console page.

## macOS 11+

`macos/` is a Swift AppKit application using `UNUserNotificationCenter`. It
receives private JSON files through Launch Services, returns atomic receipts,
and remains available for notification-click callbacks. The click delegate
validates the URL again and opens it using `NSWorkspace`, without a shell.
The helper is a background accessory without a Dock icon.

Build on macOS with Xcode Command Line Tools:

```
bash cli/scripts/build-notifier-macos.sh build/notification-package arm64
```

Use `amd64` for Intel. Ship the resulting `EigenFlux Notifications.app` beside
the real CLI executable. Test ZIP bundles preserve POSIX modes so the CLI and
helper remain executable after extraction. `Setup` verifies its code signature and copies it to
`~/Library/Application Support/EigenFlux/Notifications/`. The OS can request
notification permission once; denial must be changed in System Settings.
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

WinRT `ToastGeneric` notifications use `activationType="protocol"`. Clicking
opens the validated URL in the default browser even after the sender exits.
No COM activation server or message-supplied command is executed. XML escaping
and a deterministic 16-character tag preserve data boundaries and OS dedup.
Reject serialized Toast XML above the Windows 5 KB limit.
PowerShell/notification restrictions from organizational policy produce an
error; the CLI does not bypass them. Run under an interactive desktop login.

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
