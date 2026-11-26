# ADR-0008 — Durable governance state: a log for decisions, a snapshot for facts

- **Status:** Accepted
- **Date:** 2026-11-07
- **Relates to:** ADR-0007 (S5 governance), ADR-0005 (archive integrity), ADR-0001 decision 3

## Context

ADR-0007 gave S5's controls one owner and recorded the consequence plainly:
*governance state is in memory, and a restart resets permissions, spend and the
audit log.* That was the largest remaining gap in S5. It is worse than it looks,
because the three losses are not equally bad:

| Lost on restart | Consequence |
|---|---|
| Permissions | Every companion silently becomes unconfigured. Deny-by-default makes this safe but breaks all legitimate work until an operator re-grants. |
| Spend | The monthly token cap can be reset by restarting the Hub. A cap that a restart clears is not a cap. |
| Audit log | "What did this companion try" loses its answer. An audit trail that evaporates is not evidence — it is a debug print. |

Phase 1 has no real token spend (the local rule engine), so none of this was
observable in a demo. It becomes observable the first day a real companion runs
against a real model.

## Decision

### 1. Two files, because the two kinds of state fail differently

```
<HUB_GOVERNANCE_DIR>/audit.jsonl   append-only, one AuditEntry per line, fsynced per append
<HUB_GOVERNANCE_DIR>/state.json    whole snapshot, replaced atomically (temp file + rename)
```

The audit log is never rewritten. Rewriting it is exactly the tampering the hash
chain exists to detect, so the file format makes the correct operation the cheap
one: appending a line. The permission and budget state is small and changes
wholesale, so an atomic replace cannot leave a half-updated permissions table
the way an in-place edit could.

Both facts are already in the data: an audit entry is immutable and ordered, a
permission can be overwritten by the next `PUT`.

### 2. Audit first, state second

`Components.Commit` appends the audit entry, flushes it, and only then writes the
snapshot. The order is the whole safety argument:

- audit → state, crashed in between: a decision was recorded and never applied.
  The log is ahead of the state. Detectable at load, and the direction that
  matters (an unrecorded *grant*) did not happen.
- state → audit, crashed in between: a permission change is live with no record
  of who made it. That is the failure the audit log exists to prevent.

So the invariant is **no authority change without a record**, not "no record
without a change". The weaker one is achievable without a transaction; the
stronger one needs one, and this Hub has no database.

### 3. Loading refuses two things and reports a third

| Load-time finding | Response |
|---|---|
| Chain does not verify | **Refuse to start.** Entries were edited, removed or reordered. Continuing would launder the damage into a log that verifies from the tampered point onward. |
| Snapshot references an audit entry the log does not have (`audit_seq` > log length) | **Refuse to start.** An authority change was applied without being recorded. |
| Log has entries past the snapshot | **Start, and say so.** This is the crash between the two writes from §2. The decisions are kept — they were recorded — and the operator is told they were never applied. |
| Torn final line (interrupted append) | **Repair, and say so.** The incomplete tail is truncated to the last newline and reported as `TornTailError`; at most one never-completed record is lost. A torn tail is the only repair this ADR allows, and it is reported rather than silent. |

A malformed line anywhere *but* the end is corruption, not a torn write, and
fails loudly.

### 4. Approvals are deliberately not persisted

An approval authorises one high-impact action at the moment of execution. If it
survived a restart, a grant issued before the process died would authorise an
action after it came back — which is precisely the standing approval ADR-0007
forbids. Restart drops every outstanding approval, and the next high-impact call
gets `approval_required`. The fail-closed direction here is also the simpler
implementation, which is a good sign that it is the right one.

### 5. A write that fails stops the process from answering

If `Commit` cannot record a decision, the endpoint returns `500
GOVERNANCE_PERSIST_FAILED` and the `Governance` bundle marks itself degraded;
every later governance call answers `503 GOVERNANCE_DEGRADED` until restart.

The alternative — keep granting permissions that are not recorded — would make
the audit log a best-effort diary. Degrading is louder and self-limiting: a
restart reloads from disk, so the unrecorded change is *discarded* rather than
quietly kept.

Degradation is coarse on purpose. It stops the whole governance surface, not just
the failing operation, because a store that just failed a write has given no
reason to trust the next one.

### 6. Restored state is untrusted input

`Registry.Restore` rejects an unknown tier, and `BudgetLedger.Restore` rejects a
non-positive limit or negative spend. A state file is a file: it can be edited,
truncated, or written by a different version. Reading it back as an authoritative
grant without checking it would undo ADR-0007's deny-by-default rule at the one
point where nobody is watching. `State.Version` is checked for the same reason.

### 7. Memory-only stays available, and stays loud

`HUB_GOVERNANCE_DIR` unset means the components are memory-only — the pre-ADR-0008
behaviour. That keeps a local demo from needing a data directory. It is not a
silent fallback: the Hub logs a line saying permissions, budget and audit will
reset on restart. A configured directory that fails to load is **fatal**, never a
downgrade to memory, because forgetting governance state is exactly the failure
this ADR is fixing.

## Consequences

- **The audit chain now outlives the process.** `GET /hub/audit/verify` after a
  restart verifies the whole history, not just the current run.
- **The token cap survives a restart.** Spend is booked against
  `companion|month` keys exactly as the ledger uses them, so a restore cannot
  shift spend into another month.
- **A crashed commit leaves a detectable trace** instead of an invisible gap.
- **The audit log grows without bound**: one line per decision, no rotation and
  no compaction. Rotation is impossible without weakening the chain (a rotated
  file is a chain with a new root), so it needs a scheme this ADR does not have.
- **One Hub per directory.** Two processes pointed at the same directory would
  both append and interleave records, breaking the chain. Nothing detects that
  today: there is no lock file, because a correct one is platform-specific
  (`LockFileEx` on Windows, `flock` elsewhere) and phase 1 runs a single Hub.
- **The spend table keeps one entry per companion per month forever.** Small, and
  not pruned; pruning needs a retention rule the GDD does not state.
- **Still tamper evidence, not authentication.** A determined writer could
  rewrite the whole log and the snapshot consistently. Closing that needs signing
  and key management, which ADR-0005 already recorded as out of scope.
- **The write path is fsync per decision.** Correct for governance and irrelevant
  at phase-1 volume; if model calls ever drive `POST /budget/usage` at hundreds
  per second, batching becomes a real design question.

## Not claimed done

- No multi-process or multi-host safety, no lock file, no leader election.
- No signing of the audit chain or the snapshot.
- No rotation, compaction or retention for `audit.jsonl`.
- No encryption at rest: the directory is `0700` and the files `0600`, which is
  access control, not confidentiality against an administrator.
- No migration tooling. `State.Version` is rejected when it does not match rather
  than upgraded, so a schema change needs its own decision.
- Archive, session and invitation state are still in memory. This ADR covers
  governance only.
