# Flux Theatre — Implementation Plan (supersedes the ten legacy stories)

- **Date**: 2026-11-04
- **Branch**: `feature/flux-theatre-mvp`
- **Authoritative design**: `design/game-brief.md`
- **Authoritative contracts**: `docs/architecture/fluxspace-contracts.md`
- **Architecture decisions**: `docs/architecture/adr-0001-fluxspace-boundaries.md`
- **Executable specification**: `fluxspace/` (frozen, read-only)

The ten `story-*.md` files in this folder are retained as historical
reference. They are written in Chinese (AGENTS.md requires English), are
sliced horizontally by module, and self-declare `Status: Ready` without any
gate evidence. This document replaces them as the build plan.

## Decisions taken by the owner

| # | Decision |
|---|---|
| 1 | Port the prototype algorithm to Flutter + Go; keep `fluxspace/` frozen as the executable specification |
| 2 | Real EigenFlux full-stack integration (PM / WS / auth), not a mock-only build |
| 3 | Phase-1 decisions from the local rule engine; no API key, no token spend |
| 4 | Independent modules inside this workspace (`fluxspace_client/`, `hub/`) |
| 5 | Windows desktop only for the first runnable build |

## Vertical slices (GDD "Build order")

| Slice | GDD step | Legacy stories | Goal | Exit criterion |
|---|---|---|---|---|
| S1 | 1 — prove the riskiest loop | 001, 003 | Two real users + two labelled official companions complete an authentic invitation, an isolated-seat match and a follow-up interaction | A real PM invitation round-trips through EigenFlux; one 5-round match runs end to end on the Hub; seat uniqueness and one-live-match-per-owner enforced |
| S2 | 2 — one reliably solvable theatre | 004, 005 | Make one generated theatre solvable, non-repetitive, participative and watchable for its full 10+ minutes | Non-`rounds_out` rate does not regress below the recorded 10% baseline (ADR-0003); 480-match run completes; structural fingerprints do not repeat within 5 matches |
| S3 | 3 — four characters and the 2.5D director | 002, 006 | Four recognisable personas, omniscient/subjective views, automatic event director, validated with target viewers | 60fps Flame scene; Rive idle/walk/expression for four personas; retained screenshot evidence under `production/qa/evidence/`. **Blocked on G6 procurement** — see `S3_ASSET_PROCUREMENT.md` for the quote package and acceptance tests |
| S4 | 4 — memory, consequences, replays | 007, 008 | Source-tagged memories, directional relationships, immutable archives, same-spec reprises, recaps, threads | SHA-256 archive verification; six viewing modes; all-participant consent for public replay |
| S5 | 5 — harden before expanding | 009, 010 | Guided runtime setup, predictable cost, privacy, three-tier tool permissions, public consent | Budget downgrade path, per-companion permission isolation, audit log, consent gate, model-version consistency check |

Slices are vertical: each one ends in something a human can run and judge.
The original ten stories only became playable after all ten were finished.

## S1 status (2026-11-04, updated) — code complete, live-stack sign-off pending

All three gaps from the previous pass are closed in code:

1. **Invitation negotiation** — `hub/invitation/` generates three proposals
   with distinct fingerprints, sends a structured PM through the real
   `EigenFluxPort`, fetches and persists replies, applies
   accept/reject/modify (counter-proposals, 3-round cap, 5-minute TTL), and
   turns an all-confirmed negotiation into labelled seat claims and a Hub
   session.
2. **Post-match follow-up** — `hub/followup/` derives a
   `fluxspace.followup.v1` document from retained events only (source-tagged
   memories, draft directional relationship updates, metric-derived threads)
   and idempotently delivers one scoped PM per real owner. Private channels
   never leak; no model call and no invented reasoning.
3. **G9 endpoint shapes** — the fabricated `/api/v1/auth/refresh` is gone
   (injected `TokenRefresher` / explicit `TOKEN_REFRESH_NOT_CONFIGURED`), and
   `profile/fields` now uses the verified `updates` + `profile_version` shape
   with a registry-derived field whitelist.

`TestS1FullLoopOverHTTP` drives the whole slice over real HTTP: negotiation ->
send PM -> gateway parses it and answers -> fetch/parse/apply -> confirmed ->
session -> join four seats -> five rounds -> settlement -> follow-up preview
and delivery, then re-delivery idempotency. 68 Go tests pass across 9 packages.

**S1 is still not signed off.** The exit criterion says the PM must round-trip
"through EigenFlux"; the loop above runs against a local HTTP gateway stand-in
because the Docker daemon is down and WSL is not enabled (G10). Nothing here
claims a live-stack pass. S2's exit criteria were implemented and verified on
2026-11-07; G11 was closed on the same day (ADR-0006).

**Owner ruling 2026-11-06:** the criterion is kept verbatim rather than
relaxed; S1 is labelled *code complete, live-stack sign-off pending*. See
`docs/architecture/adr-0002-s1-acceptance-and-g1-baseline.md`. A local HTTP
stand-in is not EigenFlux, and a weakened criterion would hide the project's
single highest integration risk.

## What is implemented now

**Go module `hub/` — built, vetted and tested.**

- `generator/`: FNV-1a-compatible `HashStr` (UTF-16 code-unit exact),
  `Fingerprint`, `StructuralHash`, `ContentHash`, `EstimateMinutes`,
  deduction skeleton generator with 60-attempt dedup, and the three-path
  calibrator.
- `referee/`: `FatigueMul`, `ComputeRoundDelta` (weighted-average settlement,
  synergy, fatigue, pace scaling, versus inversion), `ScoreAction`,
  `DecideAction`, `EvalCondition`, `CheckEnd` (rounds-out resolves to partial
  or regret, never neutral).
- `models/`: skeleton/decision/memory/persona types and the GDD-traits →
  engine-vector bridge.
- `services/`: `ModelAdapter` contract and `RuleEngineAdapter`.
- `session/`: the S1 state machine. `Create` enforces one seat per owner and
  one live match per owner; `Join` binds owners to seats; `Advance` walks
  `observe -> discuss -> act`; `SubmitAction` re-validates the decision against
  the live action pool and settles the round when all seats have submitted.
  Settlement calls `referee.ComputeRoundDelta` / `referee.CheckEnd`, applies
  `round_tick` and one weighted resolve-phase branch, and emits the full event
  log. A `seed` plus the invitation ID make a whole match reproducible.
- `invitation/`: the pre-match negotiation state machine. Three
  fingerprint-distinct proposals, PM send/poll, accept/reject/modify,
  counter-proposal re-confirmation, TTL/expiry, deduplicated inbox, and
  `BuildClaims` with labelled official backfill.
- `followup/`: source-tagged post-match document builder and an idempotent
  PM sender scoped to each real owner. Relationship updates are drafts only.
- `apperr/`: shared domain error type so business packages do not import
  `session` just to build an HTTP-status error.
- `archive/`: immutable S4 replay archives over the retained event stream.
  SHA-256 content and skeleton hashes, tamper-detecting `Load`, six viewing-mode
  projections, a fail-closed consent ledger and same-spec reprise comparison
  with a divergence point (ADR-0005). Archives are in memory and hold no
  persistence yet.
- `govern/`: S5 controls. Per-companion monthly token ledger whose refusal names
  the rule-engine downgrade instead of silently reducing quality; three-tier
  permissions with deny-by-default and single-use high-impact approvals; a
  hash-chained audit log that records refusals as well as successes; and a
  model-version check that treats an unrecorded version as a failure (ADR-0007).
  `persist.go` makes the three stateful controls durable under
  `HUB_GOVERNANCE_DIR`: an append-only `audit.jsonl` and an atomically replaced
  `state.json`, with load refusing a broken chain or a snapshot ahead of the log
  (ADR-0008). Unset directory = memory-only, logged explicitly.
- `api/`: `GET /health`, nine session endpoints, six negotiation endpoints,
  two follow-up endpoints, eleven archive/reprise endpoints and eleven
  governance endpoints. `main.go` listens on `:8095`;
  `HUB_SPECTATOR_TOKEN` gates the omniscient stream;
  `HUB_GATEWAY_URL`, `HUB_AGENT_TOKENS` and `HUB_AGENT_ID` configure the real
  gateway client; `HUB_GOVERNANCE_DIR` makes governance state durable and a
  failed load is fatal. Unconfigured gateway paths return explicit 503s. SSE
  polls the retained event log so no observer can drop an event.
- `services/eigenflux.go`: the §7 `EigenFluxPort` contract and a real HTTP
  `GatewayPort` (PM send/fetch, friend request, injectable token refresh with
  one 401 retry, Agent Card field patch with verified optimistic-lock shape).
- `testdata/export_golden.js` → `golden.json`: 5 hash samples, 7 fatigue,
  20 duration, 1 fingerprint, 4 settlement, 4 scoring, 6 condition cases —
  all exported from the frozen prototype and asserted value-for-value in Go.

**Flutter scaffold `fluxspace_client/` — compiled and analysed (2026-11-06).**

`pubspec.yaml`, `main.dart`, four-tab navigation, four page widgets,
`HubClient`, the six-table `LocalDatabaseSchema`, a scaffold test and four
persona YAML files. Flutter 3.47.9 / Dart 3.13.7 is installed at
`C:\Users\louvr\.workbuddy\toolchains\flutter` (not on system PATH; invoke by
absolute path). `flutter pub get` resolved 122 packages and produced
`pubspec.lock`; `flutter analyze` reports **No issues found!**. Dependency
constraints in `pubspec.yaml` are still `any` — pin them before distributing.

`flutter test` **PASSES — 3 tests** (four navigation destinations, six-table DDL,
Hub port collision). Two environment obstacles had to be cleared first, both
without touching source code:

1. `drift`'s transitive `sqlite3 3.9.0` native-asset hook downloads
   `sqlite3.x64.windows.dll` from `github.com` releases, which the host proxy
   intermittently rejects with `502 Bad Gateway`. The DLL was fetched with
   `curl` (retry succeeded) and its sha256 `35d811b3…` verified against the
   package's own `asset_hashes.dart`, then placed in the hook's shared cache
   directory. The hook's own hash check reuses it. No `pubspec.yaml` change.
2. The proxy environment variables also hijacked the local `flutter_tester`
   WebSocket (`Invalid WebSocket upgrade request`). Running the test with proxy
   variables unset and `NO_PROXY=localhost,127.0.0.1` fixes it.

Both are host-network quirks, not client-code defects. On a normal network
neither workaround is needed. Pinning the twelve `any` constraints using the
now-generated `pubspec.lock` remains a separate owner decision.

**Toolchain.** Go 1.25.0 installed at
`C:\Users\louvr\.workbuddy\toolchains\go` (user scope, no admin rights).
`winget install GoLang.Go` failed with "No applicable installer found";
`https://go.dev/dl/...` failed with an SSL error; the domestic mirror
succeeded.

## Verification results (2026-11-04)

| Command | Result |
|---|---|
| `go build ./...` (hub) | PASS |
| `go vet ./...` (hub) | PASS |
| `go test -count=1 -v ./...` (hub) | PASS — **140 passing tests** across api, archive, followup, generator, govern, invitation, models, referee, services, session (apperr has no tests of its own) |
| `python hub/scripts/check_balance.py` | PASS — the S2 gate. 5 x 480 = 2,400 matches aggregated; measured 9.38%–11.25% across 20 consecutive runs, 0 failures. Floor is 0.08, the three-sigma lower bound of the 10% baseline (see ADR-0003 amendment) |
| S4 replay archive | PASS — `hub/archive/` 15 tests (build, tamper detection, six modes, consent policies, reprise divergence) plus 3 HTTP tests covering the full S4 flow |
| Archive integrity | PASS — SHA-256 over `{version, session_id, config, events, settlement}` plus a skeleton hash; `Load` refuses tampered bytes; six tamper cases assert the exact mismatch |
| Six viewing modes | PASS — full, accelerated (2x/4x, other speeds rejected), single_character (private events of other seats are filtered), omniscient_comparison (seat-ordered), key_moments (derived and deterministic), same_spec_comparison (divergence point) |
| All-participant consent | PASS — fail-closed policy, majority = strictly more than half, officials excluded, reprise always requires every participant |
| Agent Card projection (G11) | PASS — 2 `services` tests plus 3 `followup` tests: only a blank public descriptor is written, an occupied field is skipped as `owner_content_present` with no PUT issued, an unconfigured sender touches the gateway zero times, and no personality/memory/relationship key can reach the write body |
| S5 governance | PASS — `hub/govern` 21 tests plus 7 HTTP tests: budget refuses over-cap with `downgrade: rule_engine` and books nothing; permissions deny by default and never leak between companions; a high-impact approval is single-use, operation-specific and expiring; the audit chain detects edited, removed and reordered entries; model consistency treats a missing version as a failure |
| S5 governance persistence (ADR-0008) | PASS — 9 `govern` tests plus 2 HTTP tests: permissions, spend and the chain survive a full reload; the chain continues at seq N+1 instead of restarting; outstanding approvals are dropped across a restart; an edited entry, a snapshot ahead of the log and an unknown schema version all refuse to load; an interrupted append is truncated and reported; a write failure returns `500 GOVERNANCE_PERSIST_FAILED` and every later call returns `503 GOVERNANCE_DEGRADED` |
| Five-match fingerprint window | PASS — `TestCreateExcludesLastFiveMatchFingerprints` builds a five-match history from the exact skeletons this seed would emit, then asserts all three proposals and a modify-round regeneration avoid every one |
| `bash scripts/build.sh` (hub) | PASS — `hub/build/fluxspace-hub` |
| Golden comparison vs `fluxspace/_check.js` | PASS — every case matched |
| Hub identity and ownership (ADR-0009) | PASS — 9 new tests plus a live smoke: no token is `401 AUTH_REQUIRED`, a wrong one `401 AUTH_INVALID`, an unbound companion `404 COMPANION_NOT_REGISTERED`, another owner's `403 COMPANION_NOT_YOURS`; the binding, the permission and the `owner_id`-bearing audit chain all survive a restart with `verify: true`; an unconfigured Hub refuses to start and `HUB_ALLOW_UNAUTHENTICATED=1` makes the insecure mode say so in the log and in `/health` |
| Pre-review self-audit | PASS — `SELF_AUDIT_2026-11-07.md`: 40 routes vs 39 documented (0 phantom, 0 undocumented), every documented test count matches, 4 findings fixed (false 503 claim, unbounded request body, non-constant-time token compare, snapshot bypassing the injected clock), 6 recorded open with reasons |
| `cd fluxspace && node _balance.js` | 480 matches: 7 wins / 41 losses / 432 timeouts (unchanged; see G1) |
| Hub HTTP e2e (5 rounds, 4 seats) | PASS — `TestSessionHTTPEndToEndFiveRounds`; outcome is never a neutral `rounds_out` |
| **S1 full loop over HTTP** | PASS — `TestS1FullLoopOverHTTP`: negotiation -> PM send/reply/poll -> confirmed -> session -> 5 rounds -> follow-up PMs -> idempotent re-send |
| Invitation negotiation state machine | PASS — 6 tests: distinct fingerprints, expiry, modify cap, rejection, counter-proposal, PM idempotency, claims + real session creation |
| Follow-up derivation and delivery | PASS — 4 tests: source tagging, no private-channel leak, quiet-match no drafts, scoped and idempotent delivery |
| G9 endpoint shapes | PASS — `TestDoRotatesTokenOn401AndRetriesOnce`, `Test401WithoutRefresherFailsLoudly`, `TestPublishAgentCardFieldsUsesRealGatewayShape`, `TestPublishAgentCardFieldsRejectsUnknownFieldLocally` |
| SSE role filtering / `Last-Event-ID` recovery | PASS — `TestSSEStreamsRespectRolesAndLastEventID` |
| Match determinism from invitation + seed | PASS — `TestMatchIsDeterministicFromInvitationAndSeed` |
| Real binary smoke test (`:18095`) | PASS — `/health` 200; invitation create 201; unconfigured send 503 `EIGENFLUX_PORT_UNCONFIGURED`; accept reply 200 confirmed; session from negotiation 201; state 200; premature follow-up 409 `SESSION_NOT_FINISHED` |
| `go test -race ./...` | **NOT RUN** — `-race` needs cgo; no `gcc` on this host |
| `flutter --version` (3.47.9 / Dart 3.13.7) | **PASS** — 2026-11-06, installed SDK verified byte-for-byte against official sha256 |
| `flutter pub get` | **PASS** — 122 packages resolved, `pubspec.lock` generated |
| `flutter analyze` | **PASS** — `No issues found!` (0 issues / 0 errors / 0 warnings) |
| `flutter test` | **PASS — 3 tests** (`All tests passed!`). Required two environment-only workarounds: priming the `sqlite3` hook cache with the sha256-verified official DLL, and unsetting proxy vars for the local `flutter_tester` WebSocket. No source change. |
| EigenFlux live-stack e2e | **NOT RUN** — `docker info` cannot reach the daemon; WSL feature absent and `wsl.exe`/`reg.exe` blocked by command-security policy (G10) |

Reproduce:

```bash
cd hub
go test -count=1 ./...
go vet ./...
bash scripts/build.sh                 # writes hub/build/fluxspace-hub
node hub/testdata/export_golden.js   # from repository root

# Flutter is not on system PATH; use the absolute path (Windows):
FLUTTER=/c/Users/louvr/.workbuddy/toolchains/flutter/bin/flutter
cd fluxspace_client && "$FLUTTER" pub get && "$FLUTTER" analyze

# On this host the proxy breaks the local flutter_tester WebSocket:
env -u http_proxy -u https_proxy -u HTTP_PROXY -u HTTPS_PROXY \
    NO_PROXY=localhost,127.0.0.1 "$FLUTTER" test
```

Go 1.27.3 is on `PATH` as of 2026-11-04 23:12; the toolchain copy at
`C:\Users\louvr\.workbuddy\toolchains\go` remains a fallback. Flutter was
installed under `C:\Users\louvr\.workbuddy\toolchains\flutter` (user directory,
no admin rights needed); the SDK is not on the system PATH.

## Environment prerequisites still owned by the user

1. **Enable WSL 2.** Re-verified 2026-11-04: the `LxssManager` registry key is
   absent, so the WSL optional feature is NOT enabled on this host — removing
   a blacklist entry alone is not enough. The interactive account is
   non-admin, so this needs an elevated shell (`wsl --install`, or enable
   `Microsoft-Windows-Subsystem-Linux` + `VirtualMachinePlatform`, then
   reboot). Hardware is fine: Windows 11 Pro build 26300, virtualization on,
   64 GB RAM. `wsl.exe` is additionally rejected by command-security policy;
   Kingsoft processes (`kxescore`, `kxetray`, `kxewsc`) are running, so add
   `wsl.exe` to that product's trust/whitelist too. If WSL cannot be enabled,
   the per-user Docker VMM backend (Beta) is an untested alternative.
2. **Install Docker Desktop** (per-user mode requires no admin rights), then
   `docker compose up -d` (PostgreSQL, Redis, etcd, Elasticsearch, Kibana —
   see `docker-compose.yml`) and `bash scripts/local/start_local.sh`.
3. ~~Install Flutter 3.47.9~~ — **DONE 2026-11-06 by the AI** (ADR-0002
   decision 4). The original local archive was truncated (618 MB, no EOCD);
   the official archive is 1.80 GB. A fresh copy from the Flutter China mirror
   (`storage.flutter-io.cn`, ~20 MB/s vs ~90 KB/s from the US host) was
   verified byte-for-byte: 1,939,451,626 bytes, sha256
   `d8e2b5977e09082c6440f89dc1aa887b94c8fbcc3b39222797046295a3f68a9e` matching
   the official release JSON, `unzip -t` clean. Installed to
   `C:\Users\louvr\.workbuddy\toolchains\flutter`; `flutter --version`,
   `pub get` and `analyze` all pass.
   `flutter test` passes 3 tests after two host-only workarounds. **Remaining:**
   pin the twelve `any` constraints using the now-generated `pubspec.lock`, and
   decide whether to normalise that lock's mirror URLs (see below).
   `pubspec.lock` currently records `https://pub.flutter-io.cn` for all 117
   hosted packages because the mirror was required to download at usable speed.
   Six sampled packages' sha256 values were compared against the official
   `pub.dev` API and matched, so the artifacts are identical, but committing
   mirror URLs would hard-code a China-only source into the project. The lock
   is therefore left untracked pending the owner's choice.
4. **Decide how to rebase onto `origin/main`.** ADR-0002 decision 3 requires
   remote alignment before the 2,081 untracked root-module files are considered
   for any commit. The first `git ls-remote` returned a transient 502; a retry
   succeeded (`main` = `6aefaa0a61cf32ab2b24398333690c6863aa83e3`). A read-only
   API comparison (no fetch, no clone, no remote configured) shows 2,069 of
   2,081 untracked paths exist upstream and 1,802 shared paths match in size,
   but the snapshot is stale: 267 paths differ, four files from the newest
   upstream commit are missing locally, and `CLAUDE.md` / `api_service.go`
   differ in content. All 66 Flux Theatre tracked files are new work with no
   upstream counterpart. The exact historical base commit is undetermined.
   **This is now a merge-strategy decision, not a connectivity problem.** No
   remote was configured and no fetch was attempted.
5. ~~Add `C:\Users\louvr\.workbuddy\toolchains\go\bin` to PATH~~ — resolved:
   `go version` reports `go1.27.3 windows/amd64` on `PATH` (verified
   2026-11-04 23:12). Keep the toolchain copy as a fallback only.
6. ~~Commit ownership is yours~~ — **resolved 2026-11-04**: the owner
   delegated commit review to the AI. Changes are reviewed and committed
   autonomously without per-commit approval; pushing and merging to `main`
   remain separate owner decisions.

## Known gaps

See `docs/architecture/fluxspace-contracts.md` §9 (G1–G8). Most important:

- **G1 — calibration targets are unreachable in the frozen prototype.**
  The GDD/story 005 ask for ~90% / 40-60% / ~10% path win rates and ≥60% of
  480 matches reaching a non-rounds-out ending. The prototype delivers 10%
  non-rounds-out. The Go port faithfully reproduces the maths (golden tests
  pass), so this is a design/calibration gap, not a porting defect.
  `TestCalibrationGDDGapIsTracked` pins the gap so a future fix must update
  the record explicitly. **Deciding whether to retune the balance or relax
  the targets is a design decision — do not silently tune numbers to make a
  test green.**

  Further measurement found the Go calibrator reporting 0% on all three
  paths and 0% non-rounds-out, while the prototype reports 10%. These are two
  different simulators, not two measurements of the same thing:
  `fluxspace/_balance.js` drives the full `Runtime` (per-persona
  `decideAction`, branch events, a freshly generated game and rotating
  companions for every match, six mode/AI combinations), whereas
  `generator/calibrator.go` replays one fixed skeleton 480 times with a
  simplified three-strategy seat picker and no branch events. Golden tests
  establish function-level parity only. **Before any retuning, choose one
  simulator as the authoritative baseline and make the other agree with it**;
  otherwise the tuning is being done against a phantom.

  **Owner decisions 2026-11-06 — RESOLVED by ADR-0002 + ADR-0003.** ADR-0002
  fixed the authoritative baseline as `fluxspace/_balance.js` (full Runtime).
  ADR-0003 then **relaxed the targets**: the three-path win-rate gates and the
  >=60% non-`rounds_out` gate are retired, because the authoritative baseline
  does not measure three-path win rates at all and no replacement percentage
  has been measured. The observed 10% non-`rounds_out` is registered as an MVP
  regression floor. `fluxspace/` stays frozen and the golden chain is
  untouched; `hub/generator/calibrator.go` is demoted to a structural
  self-check. `TestCalibrationGDDGapIsTracked` now records the two simulators'
  measured values instead of treating "validated" as an error. **Building a
  full-Runtime-equivalent Go calibrator is explicitly not done** (ADR-0003
  "Not claimed done"). S2's exit criterion is rewritten accordingly.
- **G4 — RESOLVED (ADR-0004, option A).** Story 002 is re-hung onto S3 with
  explicit dependencies on the S4 authorised replay clip and the live model
  adapter. Phase 1 still uses the local rule engine; that gap is registered,
  not hidden.
- **G7 — RESOLVED (ADR-0004, option A).** The explicitly labelled demo clip is
  accepted as an MVP interim substitute for the GDD's "real + authorized"
  onboarding clip. This is a registered deviation to be replaced once S4
  delivers real consented replays.
- **G9 — RESOLVED (static verification).** The invented refresh URL and the
  wrong profile-fields request shape were both corrected against the root
  module; see `fluxspace-contracts.md` §7. Runtime confirmation is part of G10.
- **G10 — live-stack integration is still unverified.** The whole S1 loop runs
  over real HTTP against a local gateway stand-in, but the EigenFlux stack
  (WSL 2 + Docker daemon) has never run here. S1 cannot be signed off until a
  real PM invitation round-trips through that stack.
- ~~**G11 — Agent Card projection after a match is not invoked.**~~ **Closed
  2026-11-07 (ADR-0006).** The follow-up sender now projects into the Agent
  Card when `HUB_CARD_PROJECTION_FIELDS` opts in, and only into a blank
  `agent_description` / `human_description`; occupied fields are never replaced
  and the delivery record says what was written, skipped or failed. Default is
  disabled. `pkg/agentcard/registry.go` still has no FluxSpace-owned field, so
  projection can only fill a descriptor once - recorded as the remaining
  product gap in ADR-0006.

**Documentation status:** `EPIC.md` now marks the ten legacy stories
`Superseded`, names the four authoritative sources and indexes the five
vertical slices. `docs/architecture/fluxspace-contracts.md` §3 no longer says
"only `/health` is implemented"; it records the nine live endpoints, the two
phase-1 additions, the SSE recovery semantics and the spectator auth gate.
