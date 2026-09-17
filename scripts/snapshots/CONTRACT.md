# Immutable installation snapshot contract

Source under evaluation: `643000d227e3f95c389d84d792f7d86d140fe1b2`.
This branch contains distribution scaffolding only. It must never be published
to the production `cli/latest`, `skills/latest`, or installer objects.

| Invariant | Canonical owner | Snapshot boundary / owner | Evidence | Change |
|---|---|---|---|---|
| Business command behavior, routes, auth, consent, limits, idempotency, runtime metadata | Source commit CLI and Skills | Archive exact commit before building | Source audit + original CLI suite | None |
| Normal user join prompt and native scheduling | `skills/install.md`, onboarding and heartbeat contracts | Installation doc changes download locations only; no evaluation instructions | Entry diff; exact scheduler/behavior files comparison | Distribution URLs only |
| One CLI/Skills revision across future invocations | Signed Skills distribution | Compiled fixed CDN base and expected revision, no environment handoff | Native subprocesses across multiple invocations and hostile CDN env | Immutable snapshot selection |
| Integrity checks remain active | `internal/skills` | Original signatures, hashes, extraction, swap and preservation logic | Signed real HTTP fixture and drift rejection | Snapshot-specific signing key |
| No stale fallback falsely counts as the requested revision | `skills.Sync` | Distribution-only postcondition rejects wrong revision, signer, or modified installed files | Higher-sequence and modified-file tests | Fail closed on distribution mismatch |
| Home, server and host selection persist normally | Original installer and config | Keep original host setup, flags and Home resolution | Installer diff; non-default Home/server tests | None |
| Reinstallation selects the actual artifact | Installer | Compare installed file digest with snapshot digest; verify downloaded binary | Same-version different-binary regression | Exact artifact identity |
| Resume remains on same installer | Onboarding installation reference | Rewrite only entry URL and increment Skill patch version | Allowlisted staged content diff | Source URL + version |
| Rules and approvals remain real | Codex host and original recurring-trigger reference | No Rules/permission changes in builder, publisher or new entry prose | Source audit | None |
| Publication cannot mutate production or an existing snapshot | Publisher | Validate `snapshots/<source-sha>/<harness-sha>`, refuse nonempty prefix | Unit tests; remote listing + SHA verification | New namespace only |

The public entry contains normal installation instructions, not this contract.
Operators keep observations, rule toggles and evaluation instructions outside the
onboarding task. The binary/source metadata remain truthful and inspectable.

Temporary build overlays: fixed Skills origin in `cli/cmd/skills.go`; fixed
revision/signer/hash postcondition in `cli/internal/skills/sync.go`; installer
repair hint in `cli/cmd/doctor.go`. Original business handlers and scheduler
prompt are not patched. These overlays are build outputs, never production rules.

Supported distribution target: macOS and Linux. The Windows entry explicitly
rejects this distribution instead of installing another revision. Windows host
installation behavior is not certified by this snapshot.

Cleanup: stop the owned automation, restore normal installation through its
official channel with an appropriate operator-controlled cleanup of snapshot
Skills, and verify production behavior again. Snapshot sequence numbers and
signer identity are private to this distribution; do not force them into the
production release history or weaken rollback/signature checks.
