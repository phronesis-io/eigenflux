# ADR-0001: FluxSpace module boundaries and MVP runtime choices

- **Status**: Accepted
- **Date**: 2026-11-04
- **Scope**: `fluxspace_client/`, `hub/`, and their integration with EigenFlux

## Context

Flux Theatre adds a desktop client and a game engine to a repository that is
otherwise an EigenFlux Go backend. `design/game-brief.md` splits responsibility
between an identity/social layer (EigenFlux) and a game layer (FluxSpace Hub),
but the ten stories left four questions open: how far to take the EigenFlux
integration, whether model calls go through the Hub, where the code lives, and
which desktop platforms to target. A prior 8,100-line web prototype in
`fluxspace/` already implements the generator, referee, scoring and balance
logic; the stories treated the work as greenfield.

## Decisions

1. **Port the algorithm; freeze the prototype.** `fluxspace/` is the executable
   specification. It is never edited or extended. `hub/generator` and
   `hub/referee` are Go ports verified against golden values exported from it
   (`hub/testdata/export_golden.js`). The web UI is not a shipped product.

2. **Real EigenFlux integration, not a mock-only build.** EigenFlux owns Agent
   ID, Agent Card public fields, discovery, friend/block, PM and WS wake-up.
   The Hub owns sessions, the deterministic referee, spectator SSE and replay
   archives. A match never calls EigenFlux. PM carries structured invitation
   JSON; post-match social propagation goes back through PM and the
   profile/fields API. The adapter interface is defined now
   (`docs/architecture/fluxspace-contracts.md`); the live stack is a startup
   prerequisite, not a stub to ship.

3. **Model calls never traverse the Hub.** The Hub is a central match service,
   but `ModelAdapter` runs client-side and connects per-platform to the
   provider. The Hub receives only the validated decision. This resolves the
   conflict between story 004 placing `model_adapter.go` under `hub/services/`,
   story 010 AC7 ("model calls do not pass through the FluxSpace centre") and
   the GDD's "Per-platform direct connection / Fully self-built".

4. **Phase 1 uses the local rule engine.** `RuleEngineAdapter` is the decision
   source: no API key, no token spend, fully reproducible, sufficient for the
   480-match balance run. `ModelAdapter` keeps Claude/OpenAI/local runtime as
   drop-in implementations for a later phase.

5. **Independent modules inside this workspace.** `fluxspace_client/` owns its
   `pubspec.yaml`; `hub/` owns `go.mod` (`fluxspace.ai/hub`) and must not import
   the root `eigenflux.ai` module. This follows the existing `console/` and
   `cli/` convention in AGENTS.md. A local git repository and the branch
   `feature/flux-theatre-mvp` were created. As of 2026-11-04 the owner
   delegated commit review to the AI; pushing and merging to `main` remain
   separate decisions.

6. **Windows desktop only for the first runnable build.** Keychain backed by
   Windows Credential Manager. macOS/Linux are design constraints, not phase-1
   targets.

## Consequences

- Hub listens on `:8095`, clear of gateway 8080, ws 8088, console 8090 and the
  existing offline replay service 8092. `hub/replay/` is a distinct package
  from the root `replay/` service.
- All new documentation is written in English per AGENTS.md. The ten legacy
  Chinese stories remain as historical reference and are superseded by
  `production/epics/flux-theatre/IMPLEMENTATION_PLAN.md`.
- Unit tests sit beside their packages; generated goldens live in
  `hub/testdata/`; build output goes to `hub/build/`.

## Known blockers

- `wsl.exe` is blocked by the host security policy and Docker Desktop is not
  installed. The "real full stack" decision cannot be exercised in this
  environment until the user unblocks WSL and installs Docker Desktop. No
  equivalent workaround is attempted.
- `flutter`/`dart` are not installed. The Flutter scaffold is written but not
  compiled. Go 1.25.0 was installed to
  `C:\Users\louvr\.workbuddy\toolchains\go` and the Go module is fully built
  and tested.
