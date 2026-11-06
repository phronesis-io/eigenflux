# Epic: FluxSpace — Flux Theatre

> **Goal**: A desktop AI society and 2.5D spectator theatre where users own up to
> four persistent companions that live, form relationships, and invite other
> users' AI into generated 2-8-player games while humans watch omnisciently
> without interfering.
>
> **Scope**: MVP - desktop guest onboarding, one companion activation, one
> generated cooperative deduction theatre, omniscient spectator client,
> persistent memories and relationships, replay archives, retention essentials.

## Status: this epic is planned by vertical slices, not by these stories

`IMPLEMENTATION_PLAN.md` is the **build plan**. It supersedes the ten
`story-*.md` files in this folder; ADR-0001 §Consequences says the same. The
stories are retained as a historical requirements vocabulary only. **Do not
implement from them.** They are Chinese (AGENTS.md requires English), sliced
horizontally by module, self-declare `Status: Ready` with no gate evidence, and
several of their file paths contradict the accepted ADR (story 004 puts
`model_adapter.go` under `hub/services/`; ADR-0001 decision 3 puts the adapter
on the client side).

### Authoritative sources, in priority order

| Source | Owns |
|---|---|
| `design/game-brief.md` | Behaviour, rules, numeric targets, build order |
| `production/epics/flux-theatre/IMPLEMENTATION_PLAN.md` | Slice plan, exit criteria, verification record |
| `docs/architecture/adr-0001-fluxspace-boundaries.md` | Accepted architecture decisions (Status: Accepted) |
| `docs/architecture/fluxspace-contracts.md` | Wire protocol, storage schema, known gaps G1-G8 |
| `fluxspace/` | Frozen executable specification for the settlement maths. Never edited. |

### Build order (GDD) -> vertical slices

| Slice | Goal | Exit criterion | Status |
|---|---|---|---|
| S1 | Prove the riskiest loop: real invitation + isolated-seat match | PM invitation round-trips through EigenFlux; 5-round match runs on the Hub; seat uniqueness and one-live-match-per-owner enforced | **Code complete, live-stack sign-off pending** - negotiation (3 proposals -> PM -> accept/reject/modify -> all confirmed), seat-isolated 5-round match and post-match follow-up PMs all run end to end over real HTTP against a local gateway. The wording "through EigenFlux" still requires a running stack (WSL 2 + Docker daemon) before S1 is signed off. See `IMPLEMENTATION_PLAN.md` §S1 status |
| S2 | One reliably solvable theatre | Non-`rounds_out` rate must not regress below the recorded 10% baseline; 480-match run completes; structural fingerprints do not repeat within 5 matches | **Exit criteria met (2026-11-07)** - `hub/scripts/check_balance.py` is the executable gate (5x480 matches, floor = three-sigma lower bound of the 10% baseline, see ADR-0003 amendment); `TestCreateExcludesLastFiveMatchFingerprints` proves the window. Match-history persistence stays caller-side (S4) |
| S3 | Four personas and the 2.5D director | 60fps Flame scene; Rive idle/walk/expression for four personas; retained screenshot evidence | Blocked - external Rive/2.5D asset procurement is now the ADR-0004-selected path and has not happened. Flutter SDK installed; scaffold passes `flutter analyze` and `flutter test` (3 tests) |
| S4 | Memory, consequences, replays | SHA-256 archive verification; six viewing modes; all-participant consent | **Backend complete (2026-11-07)** - `hub/archive/` + 11 endpoints: immutable event-sourced archive, tamper detection, six projections, fail-closed consent, same-spec reprise with divergence point (ADR-0005). Replay UI and persistence are NOT done; G11 Agent Card projection closed by ADR-0006 (opt-in, blank-fill only) |
| S5 | Harden before expanding | Budget downgrade, per-companion permission isolation, audit log, consent gate, model-version check | **Hub-side complete (2026-11-07)** - `hub/govern/` + 11 endpoints: monthly ledger refusing with an explicit rule-engine downgrade, deny-by-default tiers with single-use high-impact approval, hash-chained audit log, fail-closed model-version check (ADR-0007), made durable by an append-only audit log plus an atomic state snapshot under `HUB_GOVERNANCE_DIR` (ADR-0008). Consent gate is ADR-0005. No multi-process locking, no log rotation, no Hub auth, no guided setup UI |

Each slice ends in something a human can run and judge. The ten legacy stories
only became playable after all ten were finished, which is why they were
replaced.

## Legacy story index (historical reference - NOT a work queue)

All ten are `Status: Superseded`. None has passed a readiness gate.

| # | Legacy story | Original type | Superseded by | Blocking design gap |
|---|---|---|---|---|
| 001 | Flutter scaffold and local storage | Integration | S1 | - (compiled 2026-11-06: `flutter analyze` clean, `flutter test` 3/3 pass) |
| 002 | Guest onboarding and companion activation | UI | S3 | G2, G3, G4, G7 |
| 003 | Async invitation and match creation | Integration | S1 | - (wire contract defined in contracts §4, §7) |
| 004 | Match runtime and deterministic referee | Logic | S1, S2 | File layout contradicts ADR-0001 decision 3 |
| 005 | Generator skeleton and reachability calibration | Logic | S2 | G1 resolved by ADR-0003 (targets relaxed); S2 exit criteria verified 2026-11-07 |
| 006 | Omniscient spectator client and 2.5D theatre | Visual/Feel | S3 | G6 - no Rive/2.5D assets, no art bible, no evidence directory |
| 007 | Persistent memory and relationships | Logic | S4 | - |
| 008 | Replay archives and same-spec reprise | Integration | S4 | G5 - Flux Points range conflicts with GDD |
| 009 | Retention and trust | Integration | S4 | G5 - notification cap conflicts with GDD |
| 010 | Security hardening and cost control | Integration | S5 | Contradicts story 004; §8 monthly token cap ratified by ADR-0004 |

### Decisions taken (all design gaps G1-G7 resolved 2026-11-06)

| ADR | What the owner decided |
|---|---|
| `adr-0002-s1-acceptance-and-g1-baseline.md` | Keep the S1 exit criterion verbatim; `fluxspace/_balance.js` full Runtime is the G1 baseline; align the git remote before any bulk commit; AI owns Flutter install and the English ADR draft, WSL 2 stays owner-side. |
| `adr-0003-relaxed-calibration-targets.md` | **Relax the G1 targets.** Retire the ~90% / 40-60% / ~10% three-path win-rate gates and the >=60% non-`rounds_out` gate. The authoritative baseline does not measure three-path win rates, so no replacement percentage is invented. The measured 10% is registered as an MVP regression floor. `fluxspace/` stays frozen; the Go calibrator is demoted to a structural self-check. |
| `adr-0004-signed-design-decisions.md` | **Select option A for all seven remaining gaps**: G2 (confirm the `persona.go` approximation), G3 (ratify the three YAMLs), G5 (GDD numbers win), §8 (ratify 200,000 tokens/companion/month), G6 (procure Rive/2.5D assets externally), G7 (accept the labelled demo clip as an MVP interim), G4 (re-hang story 002 onto S3 with explicit S4 dependencies). |

`production/epics/flux-theatre/DESIGN_DECISIONS_DRAFT.md` retains the full
option analysis and its signed record as history; the effective decisions live
in ADR-0004. Two labelled GDD deviations are now registered, not hidden: the
onboarding clip is a labelled demo (G7), and phase-1 decisions come from the
local rule engine while the S4 live model adapter is still absent (G4).

**Still open, but not design decisions:** G10 (live-stack e2e, needs WSL 2 +
Docker), the absence of a FluxSpace-owned Agent Card field (ADR-0006), the git merge strategy
(no clean base commit), and whether to push / merge to `main`.

### Environment prerequisites owned by the user

WSL 2 enabled (needs an elevated shell and a reboot; this account is non-admin)
and a running Docker daemon. A reachable git remote also needs owner input:
the official URL in `README.md` L222 resolves (`main` =
`6aefaa0a61cf32ab2b24398333690c6863aa83e3`), and read-only comparison shows
this 2,081-file snapshot is a stale derivative of it, so the owner must choose
a merge strategy (ADR-0002 "Still open" item 2). Flutter 3.47.9 and the
English design-decision ADR draft were delivered by the AI on 2026-11-06;
`flutter analyze` and `flutter test` now pass. Pinning the `any` dependencies
and normalising the mirror URLs in `pubspec.lock` remain open. The Go toolchain
is present and the Hub module builds and tests cleanly.
As of
2026-11-04 the owner delegated commit review to the AI: changes are reviewed
and committed autonomously, with no per-commit approval. Pushing and merging to
`main` are still separate owner decisions (ADR-0001 decision 5).
