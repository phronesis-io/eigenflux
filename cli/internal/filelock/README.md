# Retained OS locks

`TryAcquire` opens a persistent lock file and attempts a nonblocking exclusive
OS lock: `flock` on macOS/Unix, `LockFileEx` on Windows. Contention returns false;
filesystem and OS failures retain their errors. `Release` is idempotent and closes
the handle without removing, truncating or rewriting the file. Process exit also
releases ownership. Never infer ownership from file contents, existence or age.

Consumers:

- `skills/lock.go`: the Skills parent `.ef-skills.lock`, shared by synchronization,
  bundle installation and uninstall. Busy writers keep the existing retry error.
- `feedevent/lock.go`: the event directory `.queue.lock`, shared by enqueue/flush.
- `skills/read_signed.go`: `Idle` probes an existing lock read-only, without
  creating it. Signed reads retain snapshot, digest and journal checks; a retained
  unlocked file does not block Agent dispatch.

Upgrade all CLI processes sharing a target before resuming synchronization or
queue writes. Old existence-based lock owners do not participate in OS locking;
do not run the two protocols concurrently. Once old processes have stopped,
abandoned empty or PID/timestamp files can be reused without deleting them or
waiting 300/600 seconds. Never delete or replace a lock file held by a process.

Skills staging, journal recovery, directory swaps and old-directory cleanup are
unchanged. These operations may still require host filesystem approval. A lock
change does not bypass WorkBuddy permissions or justify manual `rm -rf`, direct
Skill overwrites, manifest replacement or changes to signing trust.

Run `go test -race ./internal/filelock ./internal/skills ./internal/feedevent`
from `cli/`. Tests cover retained file identity/content, live-owner exclusion,
read-only probes and cross-process release after forced termination. Windows
cross-compilation verifies build compatibility; native Windows behavior still
requires machine acceptance.
