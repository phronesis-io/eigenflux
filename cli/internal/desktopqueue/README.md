# Desktop notification queue

Read before changing this package or `cmd/watch_desktop.go`. Update this file in
the same change. The CLI owns durable delivery; OS adapters only submit messages.

## Flow

1. `watch --dispatch` enables desktop notifications by default on macOS/Windows
   for a binding subscribed to `commission_order`. Both buyer and seller events
   become notifications; displaying one never invokes an Agent.
2. `deliverCommissionNotifications` validates and saves the execution job, then
   saves its desktop message, then ACKs the remote notification. Either save
   failing prevents ACK. Socket and HTTP use this same path.
3. A separate worker submits one eligible message per three-second cycle. Check
   the pinned identity before and after OS submission, including after setup.
4. `Journal.CommissionBlockers` supplies validated routing metadata for local
   `needs_user`, `failed`, and `unknown` work. Recover these notifications after
   crashes without exposing task bodies or changing execution state.

## Persistence and retry

`watch/desktop-<scope>.json` uses the existing Home/server/Agent/principal scope,
independent of binding revision. The caller holds the account watch lock; a mutex
serializes local workers. Atomic writes precede in-memory state changes.

Server message IDs include scope and `notification_id`, preserving reminders that
share an order version with a state change. Local blocker IDs hash scope, job ID,
status, and code. Successful submission receipts prevent replay across restart or rebind.
Keep at most 8192 entries; evict only submitted receipts, never pending work.
Deduplication ends when a receipt is evicted.

OS failures retain pending work with 1/2/4/8/16-minute retry delays; setup retries
every five minutes. These failures do not stop Agent workers. Queue persistence
failure or exhausted capacity stops watch; ingestion never ACKs an unsaved
desktop message. Submission followed by a crash before receipt persistence can
repeat the same stable message ID. OS acceptance does not prove banner visibility
or a user click.

## Links and verification

Build links from the configured server origin and validated decimal account,
order, and role fields. Never accept a URL from notification payloads or include
credentials. Use the native adapter's URL validator for one shared origin policy.
Unsupported origins disable only the desktop sink with a diagnostic.
The Console must verify the expected account before reading order details.

Tests: `queue_test.go` and `../../cmd/watch_desktop_test.go`. Run from `cli/`:
`go test -race ./internal/desktopqueue ./internal/dispatch ./cmd -run 'TestDesktop|TestQueue'`.
