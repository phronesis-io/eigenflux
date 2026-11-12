# Audit shard inventory and manual export

- **Status:** Implemented
- **Date:** 2026-11-07
- **Relates to:** ADR-0008 (governance persistence), ADR-0010 §2 (rotation),
  `hub/govern/rotate.go`, `hub/govern/shards.go`

## Why this exists

The governance audit log is one continuous `seq + prev_hash` chain. When the
active `audit.jsonl` reaches the rotation threshold (8 MiB by default) or
`Store.Rotate()` is called, it is renamed to a deterministic shard
`audit.<12-digit-first-seq>.jsonl` and a fresh active file is opened. The chain
is **not** restarted per shard: the loader (`LoadAudit`) reads every shard in
sequence order and then the active file, and verifies the whole chain from the
very first entry on every startup.

Because verification always starts at entry 1, **no shard may ever be deleted,
truncated, compressed or rewritten**. Doing so would remove entries from the
middle (or front) of the chain and break `prev_hash` links for every later
entry; introducing a "chain anchor" that skips the old entries was explicitly
rejected. That is why the Hub offers **no automatic retention, compaction,
deletion or migration of shards**. This document describes the read-only view
and the manual, human-driven path for archiving old shards offline instead.

## Endpoint

```
GET /hub/audit/shards
```

This is a privileged, owner-only read. It passes through the `WithAuth`
default-deny gate: a request with no bearer token gets `401 AUTH_REQUIRED`, and
a spectator token (or any non-owner token) cannot reach it. It also passes
through `Server.governance()`, so an unconfigured Hub answers
`GOVERNANCE_UNCONFIGURED` and a degraded Hub answers `GOVERNANCE_DEGRADED`,
exactly like the rest of the governance surface. It never mutates state.

### Response shape

```json
{
  "count": 4,
  "shards": [
    {
      "name": "audit.000000000001.jsonl",
      "path": "/var/lib/hub/govern/audit.000000000001.jsonl",
      "first_seq": 1,
      "last_seq": 3,
      "entries": 3,
      "bytes": 1185,
      "first_hash": "sha256:...",
      "last_hash": "sha256:...",
      "active": false
    },
    { "...": "one object per shard, in sequence order" },
    { "...": "" },
    {
      "name": "audit.jsonl",
      "path": "/var/lib/hub/govern/audit.jsonl",
      "first_seq": 10,
      "last_seq": 11,
      "entries": 2,
      "bytes": 790,
      "first_hash": "sha256:...",
      "last_hash": "sha256:...",
      "active": true
    }
  ]
}
```

`shards` lists every `audit.<12-digit-seq>.jsonl` shard in sequence order,
followed by the active `audit.jsonl` as the final element. Exactly one entry has
`active: true`. Empty files report zero `first_seq`/`last_seq`/`entries`/`bytes`
and empty hashes. A memory-only Hub (no data directory) returns an empty list.

Each field is measured by reading the real file:

| Field | Meaning |
|---|---|
| `name` | File name (the active file is `audit.jsonl`). |
| `path` | Absolute path of the file on the Hub host. |
| `first_seq` / `last_seq` | `seq` of the first / last entry in the file. |
| `entries` | Number of parsed `AuditEntry` lines in the file. |
| `bytes` | Real on-disk size of the file. |
| `first_hash` / `last_hash` | `hash` of the first / last entry in the file — real chain values, not a synthesized anchor. At a boundary, one file's `last_hash` is the next file's first entry's `prev_hash`. |
| `active` | `true` only for the append target `audit.jsonl`. |

`ShardInventory()` is strictly **read-only**: it parses every line but creates,
renames, truncates and deletes nothing. In particular it does not use the
loader's torn-tail repair (which truncates the active file); an incomplete final
line is left untouched on disk and simply not counted.

## Manual export

The Hub writes plain JSONL, so an operator can archive shards with their own
tooling. There is deliberately **no HTTP export endpoint** — the only new route
is the read-only inventory above — and **no code path that writes, rewrites or
removes a shard inside the Hub process**.

Two `govern.Store` helpers stream raw bytes out for a small offline tool or a
`go run` helper the operator owns:

```go
// Stream the entire chain: every shard in sequence order, then the active file.
// Output is byte-for-byte the concatenation of the source files.
err := store.ExportAudit(w)          // w is any io.Writer (file, pipe, upload)

// Stream exactly one rotated shard, addressed by its first sequence number.
err := store.ExportAuditShard(w, 4)  // -> audit.000000000004.jsonl
```

`ExportAudit` copies each file's raw bytes directly (`io.Copy`), so the export
is byte-for-byte identical to the original JSONL and the whole history is never
held in memory. Both helpers are read-only: they only open files for reading.

### Suggested offline procedure

1. `GET /hub/audit/shards` with an owner bearer token and record the inventory
   (`name`, `first_seq`, `last_seq`, `bytes`, `first_hash`, `last_hash`). Keep
   this manifest with the archive.
2. Choose the old shards to archive (see the decision checklist below). The
   active `audit.jsonl` must stay in place — it is the append target.
3. Copy the chosen `audit.<seq>.jsonl` files verbatim to durable offline storage
   (for example `cp --preserve` or an object-store upload of the raw bytes), or
   use `ExportAuditShard` / `ExportAudit` to stream them out. Verify the copy
   byte-for-byte against `bytes` / `first_hash` / `last_hash` in the manifest.
4. **Do not delete or modify the source shards from a running Hub's directory.**
   The Hub still needs them to verify the chain on every startup. Removing them
   turns the next startup into a refused start (a broken chain), not a smaller
   log.

## No automatic deletion — and why

**There is no automatic deletion, retention, compression, truncation,
compaction, or chain anchor in the Hub, and none will be added as part of this
work.** Deleting or rewriting a shard would break the `seq + prev_hash` chain:
verification reads from the very first entry forever, so any gap, rewrite or
"skip old entries" anchor degrades the tamper-evidence guarantee. The trade-off
is deliberate: **disk grows without bound; the chain never degrades.**

### Operational consequence

One line is appended per governance decision, fsynced per write, and rotated
into immutable shards that are never pruned. The governance directory therefore
grows monotonically for the lifetime of the Hub. There is no bound and no
automatic cleanup. Operators must budget disk for the full history and monitor
the directory; `GET /hub/audit/shards` is the tool for seeing how many shards
exist and how large they are.

### What a human must decide before archiving shards offline

Archiving is a **human decision**, never an automatic one. Before taking any
action, an operator must decide:

- **Which shards** to archive, by `first_seq` / `last_seq` / `name` from the
  inventory. Never the active file.
- **Where** the verbatim copy goes and **how it is kept byte-for-byte verifiable**
  (checksums, the recorded `first_hash` / `last_hash`, and the inventory
  manifest), so the exported chain can still be verified offline.
- **Whether and when to reclaim disk.** Reclaiming space in the live directory
  means removing shards the running Hub still reads at startup. That is only
  safe if the operator has a separate, deliberate plan for the live chain — for
  example retiring the entire Hub/data directory and standing up a new one with
  a fresh chain — and accepts that the new Hub no longer verifies the old
  history. A partial deletion in a still-running directory is never safe.
- **Retention and legal requirements** for the audit history, and who is
  authorised to make that call.

In short: the Hub reports the shards and can stream them out byte-for-byte; it
will never delete them. Any space reclamation is an offline, operator-owned
decision made with full knowledge that the live chain must remain intact and
verifiable from entry 1 on every startup.
