# Immutable installation snapshot contract

Source under evaluation: `6cfbdf627bded5b0be4da3d28d333a1bd45793fe`.
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

## Five-stage snapshot acceptance

The product source remains 6cfbdf627bded5b0be4da3d28d333a1bd45793fe.
All five-stage copy, authorization gates, explicit Home/server identity, optional
Prefill, website completion gate, first-check cycle and action menu remain owned
by that source. No product CLI files change on the distribution branch.

The previous preview/install.py isolated local kit is obsolete and removed. Its
private Home, disabled auto-sync and local-bundle setup are test scaffolding, not
production requirements. The remote snapshot preserves normal host installation,
Home selection and signed sync; the existing native HTTP fixture checks a Home
and Skills path containing spaces, explicit nondefault server, repeated sync,
incomplete/completed website states, exact scheduler prefix, and drift rejection.
No local kit references or onboarding instructions are migrated to the entry.

Before delivery compare the pinned product source, this contract and generated
artifacts. Check the installation entry byte-for-byte after URL normalization,
review generated audit patches, run original CLI/installer tests and snapshot
HTTP integration, then verify every published artifact checksum. Manual testing
will still be required for host approvals, restart continuation, website setup
and actual user-visible first-check results. Publishing does not install locally.

## Distribution-switch rollback contract

A signed sequence belongs to its signing authority, not every distribution
sharing a host Skills directory. The immutable snapshot uses its own signing
key and sequence 1. The current source syncDecision compares sequences across authorities;
a prior production manifest at sequence 33 with all four managed directories
removed therefore blocks first snapshot installation.

The snapshot build overlay scopes that sequence comparison to identical key IDs.
Keep trusted remote signature verification, equal-sequence/different-revision
rejection and lower-sequence rejection within the same authority unchanged.
Keep managed edit and unrelated Skill preservation, atomic swap, explicit Home,
server and native host routing unchanged. No installed local files are deleted
by the builder or publisher, and no production CLI source is edited.

Cross-authority installation must still end on the compiled snapshot revision,
verify its local signature and all managed hashes, and fail if preserved edits
prevent that postcondition. It cannot accept an old foreign installation as a
successful snapshot. Regress with a real independently signed local sequence-33
manifest both with missing managed folders and with intact old folders; preserve
an unrelated Skill. Then verify repeated sync, same-authority rollback/reuse,
signature tampering and non-default identity/server handoffs.

## Browser handoff round

Product baseline: 6299f688 (latest main at refresh).
Product under test: 6cfbdf627bded5b0be4da3d28d333a1bd45793fe.
Distribution branch: codex/onboarding-browser-snapshot (no PR).
CLI version: 0.6.9; minimum: 0.6.7. Product ef-onboarding: 0.3.18; staged
entry-only revision: 0.3.19. The installer retains installation replace/record
and its locking/ownership behavior. The HTTP fixture expects the current
heartbeat origin and need_capture stage, without patching either behavior.

The product's browser handoff contract remains in
`docs/dev/onboarding-five-stage-contract.md`. The browser is opened only after
all preparation succeeds; failure falls back to the same full link; human
verification and server completion are unchanged. Host-native browser behavior
is for user acceptance, not simulated by the distribution fixture.

Production CLI/Skills publishing workflows do not trigger from this branch.
Only the isolated snapshot workflow is added. No live account, Rules, local
installation or native automation is modified by building/publishing. Live
installation uses the production EigenFlux service and normal Home selection.
Before promoting the product, exclude this entire distribution-only commit,
workflow and build overlays; the product branch already contains the retained
browser behavior and regression tests. Restore an installed snapshot through
an explicitly authorized normal install, verifying executable bytes and Skills
signer/revision; do not delete identities or alter existing tasks by default.

The refresh preserves current sync diagnostics on failed postconditions, including
modified-file details. The CLI-upgrade diagnostic entry URL also selects this
snapshot. Keep the os import required by current diagnostic output. Latest
account recovery, session renewal, lock handling and attribution reporting are
archived unchanged from the product source.
