# ADR-0011 — Durable archives and immutable audit retention

- **Status:** **Accepted** — the owner selected all three recommended options on
  2026-11-07; all decisions are implemented.
- **Date:** 2026-11-07
- **Relates to:** ADR-0005 (archive boundaries), ADR-0008 (governance persistence),
  ADR-0009 (identity), ADR-0010 (Hub hardening), `IMPLEMENTATION_PLAN.md` S4/S5

## Context

PR #529 opened with five engineering gaps still on the record: S4 archives were
in memory (a restart lost every replay, publication flag, consent grant and
reprise), audit shards accumulated with no way to inspect them, the client could
not play a recorded archive, there was no guided first-run setup, and the
spectator token had no expiry or revocation.

Three of those needed an owner decision because the obvious answer has a real
cost:

1. **Archive storage medium.** `hub/go.mod` has **zero external dependencies**.
   SQLite would add one (with a known-flaky proxy on this host) and would make
   the archive layer structurally unlike the governance layer, which already
   solves the same problem with JSON files.
2. **Audit shard retention appears to conflict with the hash chain.** `LoadAudit`
   reads every shard in `seq` order and verifies `prev_hash` from the first
   entry. Deleting the oldest shard does not just lose history — it makes the
   chain unverifiable and the Hub refuses to start. A "chain anchor" that skips
   old entries would silently narrow what "verified" means.
3. **Spectator token expiry / revocation** would change the wire contract and
   the identity decisions already signed in ADR-0009.

## Decision

### 1. Durable archives via JSON files, zero new dependencies

`HUB_ARCHIVE_DIR` selects a durable directory; unset means the existing
memory-only behaviour, logged explicitly. `archive.NewStore()` is unchanged and
still memory-only; `archive.OpenStore(dir)` loads persisted state.

- Layout: append-only `archive.jsonl` (one small event per state change:
  `seq`, `kind`, `archive_id`, `content_hash`, `hash`) plus an atomically
  replaced `state.json` snapshot carrying `version`, `last_seq`, every
  `Record` (Archive + Published + PublishedAt + Decision), `order`, the consent
  ledger, `reprises` and `repriseOrder`.
- Write order is **event first, fsync, then snapshot via temp + rename +
  fsync** — the ADR-0008 invariant "no record, no state change". Every mutating
  path (`Put`, `Publish`, `GrantConsent`, `CreateReprise`, `CompleteReprise`)
  goes through one `commit` helper.
- Integrity reuses the archive package's **existing `Verify()` /
  `ContentHash()`**. No second hash scheme. Publication state deliberately stays
  outside the immutable Archive (ADR-0005), so publishing still does not
  invalidate a hash.
- Refuse to start on tampered archive bytes, tampered log, unknown snapshot
  version, or a snapshot ahead of the log. A log ahead of the snapshot starts
  with a reported note. A torn tail is truncated and reported, never swallowed.
- Any write failure sets `Degraded()` and fails closed: that call answers `500
  ARCHIVE_PERSIST_FAILED`, and every later archive call answers `503
  ARCHIVE_STORE_DEGRADED`. Load failure is fatal, never downgraded to memory.
- The HTTP consent handler now calls `Store.GrantConsent` instead of
  `Ledger().Record`, so grants are durably recorded. `api.Run` closes the
  governance store; `main` closes the archive store after the listener drains.

### 2. Audit shards are never deleted, only inventoried and exported

The `seq + prev_hash` chain stays verifiable from the first entry, forever, on
every startup. **No automatic deletion, truncation, compression, retention
policy or chain anchor exists.**

- `Store.ShardInventory()` reports, read-only, every shard plus the active file:
  name, path, first/last seq, entry count, byte size, real first/last chain
  hashes, and an `active` flag. It uses its own read-only parser rather than
  `LoadAudit`, because the loader legitimately truncates a torn active tail.
- `Store.ExportAudit(w)` / `ExportAuditShard(w, firstSeq)` stream the original
  JSONL bytes to an `io.Writer` for a human to archive offline. They are
  byte-for-byte copies, not re-serialised data.
- `GET /hub/audit/shards` exposes the inventory under the existing default-deny
  owner gate. A spectator token cannot reach it. The route audit grew from 42 to
  43 routes and remains table-tested.
- The trade-off is explicit and accepted: **disk grows without bound** until a
  human decides what to archive. `AUDIT_SHARD_INVENTORY.md` documents what a
  human must decide before moving shards offline.

### 3. Spectator token expiry / revocation is deliberately out of scope

It changes the wire contract and ADR-0009's signed identity decisions, and would
touch the same auth files as the rest of this batch. It stays a registered open
item, not a silently weakened contract.

### 4. Archive replay and guided setup are wired, honestly

- `lib/panels/archive_frames.dart` maps the Hub's six real view projections to
  `TheatreFrame` timelines using **only GET endpoints**. Fields the Hub does not
  record (speaker, seat state, votes, winner, total rounds, display names) stay
  `null` and render as "not recorded" — no plausible value is invented.
- `SETUP_WIZARD.md` describes a guided first-run flow (Hub URL -> connectivity
  and auth check -> owner token when required -> owner/companion ids -> save).
  A shared `SetupResultBus` hands the result to `TheatrePage`, which fills the
  four connection fields and shows "press Connect". It **never connects
  automatically** and never reads the platform keychain on the test path.

## Consequences

- Archives are durable on a single node. There is **no multi-process lock** for
  `HUB_ARCHIVE_DIR` (the governance lock covers only `HUB_GOVERNANCE_DIR`), no
  retention policy, no encryption at rest and no archive signature.
- Audit shards grow without bound; this is intentional, not an oversight.
- The replay player still lacks real-time event pacing (fixed 900ms), audio,
  wall-clock scrubbing, side-by-side comparison, and client-side integrity
  verification. It still renders with `PlaceholderAssets`: **S3 exit criteria
  remain unmet** until procured Rive / 2.5D assets arrive.
- Setup results are applied to the console fields only. The user must press
  Connect; no hidden network call is made on their behalf.
- `go test -race` remains NOT RUN (no gcc). EigenFlux live-stack e2e remains
  NOT RUN (WSL 2 / Docker daemon unavailable, G10).
