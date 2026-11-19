# ADR-0010 — Hub hardening and client integration (S3/S4/S5 surface)

- **Status:** **Implemented** — engineering work under the 2026-11-04 standing
  commit authorization. This ADR records what shipped and its limits; it does
  not change any signed design decision (ADR-0002 .. ADR-0009).
- **Date:** 2026-11-07
- **Relates to:** ADR-0005 (archive boundaries), ADR-0007 (governance),
  ADR-0008 (persistence invariants), ADR-0009 (identity and ownership),
  `IMPLEMENTATION_PLAN.md` S3, `S3_ASSET_PROCUREMENT.md`

## Context

By 2026-11-07 the Hub's S1/S2/S4/S5 backends were code-complete and the Flutter
client could play a full match, but five things were still true:

1. Reconnection in the client was a **button**, not a behaviour. A dropped SSE
   stream left a frozen console with no indication of an outage.
2. The S4 archive and S5 governance subsystems were entirely invisible: every
   projection, budget decision and audit entry existed only behind `curl`.
3. S3 remained blocked on procured Rive / 2.5D assets, so no render layer existed
   at all — not even the structure that would receive those assets.
4. The `WithAuth` exemptions were path-prefix based with **no method check**, so
   `POST /health`, `POST .../events` and `POST .../spectate` bypassed the owner
   gate. They reached a 405, but a default-deny gate must not have accidental
   holes.
5. `HUB_GOVERNANCE_DIR` had no multi-process exclusion, `audit.jsonl` grew
   without bound, and the server had no graceful shutdown.

This batch closes 1, 2, 4 and 5, and delivers the *structure* for 3. It does not
close the S3 procurement blocker.

## Decision

### 1. Authentication exemptions are method-qualified and exact

`publicExempt(method, path)` in `hub/api/auth.go` replaces the suffix check. The
three documented exemptions are **GET only** and matched against exact paths or
a validated `/hub/sessions/{id}/{events|spectate}` shape:

| Route | Why it stays open |
|---|---|
| `GET /health` | Liveness and `auth:"enabled/disabled"` visibility (ADR-0009 §2.1) |
| `GET /hub/sessions/{id}/events` | Per-seat `X-Seat-Token`, verified by `VerifySeatToken` (ADR-0009 §2.3) |
| `GET /hub/sessions/{id}/spectate` | `X-Spectator-Token`, omniscient read-only stream |

`GET /hub/sessions/{id}` and `/snapshot` remain **read-only relaxations**, not
public routes: a valid `X-Spectator-Token` substitutes for the bearer on GET
only, compared with `subtle.ConstantTimeCompare`. All 23 mutating routes reject
a spectator token. ADR-0009 invariants (OwnerID in the audit preimage, one
companion cannot have two owners, one-shot `high_impact` approval,
`GOVERNANCE_DEGRADED`) are unchanged. `hub/api/auth_coverage_test.go` walks all
42 registered routes.

### 2. Governance directory: advisory lock and bounded audit shards

- **Lock.** `hub/govern/lock.go` uses a stdlib-only `O_CREATE|O_EXCL` lockfile
  with pid / host / timestamp, stale-owner detection, in-process reentrancy and
  explicit release on every error path. `golang.org/x/sys` is deliberately NOT
  added to this module, so this is **advisory, not kernel-enforced**: an
  uncooperative process can ignore the file; stale reclamation has a TOCTOU
  window; pid reuse is treated as "still alive" and refuses rather than steals.
  Upgrading to `Flock` / `LockFileEx` later does not change the Store contract.
  Both `windows/amd64` and `linux/amd64` builds pass.
- **Rotation.** When `audit.jsonl` exceeds `DefaultRotateThreshold` (8 MiB) or
  `Store.Rotate()` is called, it becomes `audit.<12-digit first-seq>.jsonl` and
  a fresh active file is opened. The `seq` + `prev_hash` chain **continues
  across shards**; the loader concatenates shards in seq order before chain
  verification and snapshot-seq validation. Torn-tail truncation applies only
  to the active shard; a torn shard is corruption and refuses startup.
  `WithRotateThreshold(<=0)` restores the old single-file behaviour.

ADR-0008's invariants are untouched and are the reason rotation is subtle: the
write order stays **audit first, then snapshot**, fsync per write, approvals stay
unpersisted, and any write failure still fails closed into `GOVERNANCE_DEGRADED`.
Rotation writes **no synthetic audit entry** — it is not an authorization change.
Known limitation, unchanged from `SaveState`: the parent directory is not
fsynced after rename (unreliable on Windows), so a crash at exactly that point
can lose a shard rename.

### 3. Graceful shutdown

`hub/main.go` now uses `signal.NotifyContext` (SIGINT/SIGTERM), an explicit
`&http.Server{}`, and delegates to `api.Run(ctx, srv, ln, components)`. `Run`
calls `Shutdown` with a bounded 5s timeout, waits for `Serve` to release the
listener, then closes the governance store through an optional
`interface{ Close() error }` assertion (so it compiles with or without a
`Close` method on the store). Errors are joined and returned; only
`http.ErrServerClosed` is ignored. No degradation logic was added to swallow
real failures.

### 4. Client: automatic recovery, visible state

`lib/session/reconnect_policy.dart` is a pure, injectable exponential backoff
(500ms initial, x2, 30s cap, full jitter in [0.5, 1.0], 8 attempts default).
`SessionController` retries automatically on SSE error or unexpected end while
the session is live, carries `Last-Event-ID` on every retry, deduplicates by
event id, resets the policy on the first received event, and exposes
`connectionState`, `attemptCount`, `maxReconnectAttempts` and
`lastConnectionError`. `dispose()` / `disconnect()` cancel pending retries.
Every pre-existing public member keeps its name and signature.

`pages/theatre_page.dart` renders that state as a strip (idle / connecting /
live / reconnecting with attempt counter / failed with error code and manual
retry / closed), so a dropped stream is no longer indistinguishable from a
frozen match.

### 5. Client: S3 render skeleton and S4/S5 read-only panels

- `lib/theatre/` defines a plain `TheatreFrame` vocabulary with **zero Hub
  coupling**, a `TheatreAssets` contract, and a pure-Flutter `PlaceholderAssets`.
  Stage, seat cards, vote reveal and replay scrubber consume frames and assets.
  `TheatreScreen` is the single drop-in widget; `assets` is the only argument
  that changes when the procured pack arrives. Missing fields render as
  "not recorded" and are never invented.
- `lib/panels/` exposes S5 (budget, tier, denied operations, audit seq/prev_hash,
  model-version consistency) and S4 (archive list, six views, consent,
  divergence point) as read-only widgets. It calls **only GET endpoints** — no
  approval is consumed and no token is spent by looking at the state.
  `GOVERNANCE_DEGRADED`, `AUDIT_CHAIN_BROKEN` and `missing_fields` are explicit
  visible states, not empty lists.

`OBSERVABILITY_PANELS.md` lists the nine backend capabilities with no GET
endpoint and therefore still invisible; they were not faked into the UI.

## Consequences

- S3's exit criteria are **still not met**. Placeholders are not the procured
  Rive rigs, the 2.5D stage kit, the portrait/Official badge/type assets, or the
  60fps Flame scene with captured evidence. See `S3_RENDER_SKELETON.md` §asset
  swap contract and `S3_ASSET_PROCUREMENT.md`.
- The governance lock does not make the Hub safe against a hostile local
  process; it prevents accidental double-start corruption.
- Log rotation has no retention/deletion policy, no compression and no
  migration tool. Shards accumulate until an operator acts.
- The rotation threshold is not wired to an environment variable; production
  uses the 8 MiB default unless a caller uses `OpenWithOptions`.
- Real EigenFlux full-stack e2e (G10) is still **never run**: WSL 2 / Docker
  daemon unavailable. Local HTTP gateway stand-in remains the only end-to-end
  evidence.
- `go test -race` remains NOT RUN (no gcc on this machine).
