# ADR-0002: S1 acceptance wording, G1 calibration baseline, and repository provenance

- **Status**: Accepted (four owner decisions recorded); two follow-up decisions remain open
- **Date**: 2026-11-06
- **Scope**: `production/epics/flux-theatre/`, `docs/architecture/fluxspace-contracts.md`, `hub/`, `fluxspace_client/`
- **Relates to**: ADR-0001, `IMPLEMENTATION_PLAN.md`, `fluxspace-contracts.md` §9 (G1, G4, G10)

## Context

The S1 code slice is complete and tested (68 Go tests across 9 packages), but
its exit criterion says a PM invitation must round-trip "through EigenFlux".
That has only been exercised against a local HTTP gateway stand-in. Separately,
S2 is blocked by G1: the GDD asks for ~90% / 40-60% / ~10% three-path win rates
and >=60% non-`rounds_out` endings, while the frozen prototype delivers 10% and
the Go calibrator reports 0%. A repository audit also found that this working
tree is not a clone: it has four local commits, no configured remote, no
upstream, and 2,081 untracked root-module files.

The owner was asked to resolve four questions. All four answers are recorded
below. Two new facts surfaced while executing them and are recorded under
"Consequences" rather than silently absorbed.

## Decisions

1. **Keep the S1 exit criterion verbatim. Do not relax it.** S1 is labelled
   *code complete, live-stack sign-off pending*. A local HTTP stand-in is not
   EigenFlux. G10 stays open until a real PM invitation round-trips through a
   running stack. Rationale: a weakened criterion would hide the single highest
   integration risk in the project and produce a green status that means
   nothing.

2. **G1 authoritative baseline is `fluxspace/_balance.js` (full Runtime).**
   It drives `generateGame` per match, per-persona `decideAction`, branch
   events, rotating companions and six mode/AI combinations. The alternative,
   `hub/generator/calibrator.go`, replays one fixed skeleton 480 times with a
   simplified three-strategy seat picker and no branch events; it is not a
   measurement of the same thing. Golden tests establish function-level parity
   only. All future calibration numbers must be produced by, or reconciled to,
   the full Runtime baseline.

   **The second G1 sub-decision is NOT taken.** Whether to retune the balance
   or relax the GDD targets remains an owner design decision. It is now known
   to be harder than it first appeared: see Consequences.

3. **Align the git remote before touching the 2,081 untracked files.** Do not
   add those files to any commit. AGENTS.md requires every change to be
   developed in a worktree from the latest `origin/main` and merged through a
   PR; the current branch has no provenance at all. Bulk-committing untracked
   files would create a false baseline and mix unreviewed third-party work into
   an AI-authored commit.

4. **Division of labour for the environment.** The AI drafts the English
   design-decision ADR and installs the Flutter SDK. WSL 2 enablement remains
   with the owner: it needs an elevated shell and a reboot, and this account is
   non-admin. Pushing and merging to `main` remain separate owner decisions
   (ADR-0001 decision 5, unchanged).

## Consequences

- **G1 retuning collides with ADR-0001 decision 1.** `fluxspace/` is the
  executable specification and is `never edited or extended`
  (`adr-0001` L20-21, `EPIC.md` L31, `IMPLEMENTATION_PLAN.md` L8). Every
  threshold and payoff that would need changing is a literal inside
  `fluxspace/_check.js`: four generators (L728-1197), `computeRoundDelta`
  (L1394-1442), `scoreAction` (L395-431), `fatigueMul` (L1389-1392),
  `checkEnd` (L1948-1953) and `finish` (L1956+). Editing any of them invalidates
  `hub/testdata/export_golden.js` (seven exported functions), the 1,071-line
  `golden.json`, and every value-for-value Go assertion built on them. Retuning
  is therefore not a numeric tweak: it is a decision to break the freeze and
  rebuild the golden chain, or a decision to leave the prototype alone and
  change the targets. No code was changed pending that choice.

- **Remote alignment: read-only provenance comparison completed.** The 502
  seen on the first probe was transient. On retry, `git ls-remote --heads
  https://github.com/phronesis-io/eigenflux.git main` succeeded, returning
  `6aefaa0a61cf32ab2b24398333690c6863aa83e3` (commit dated 2026-11-06
  09:52:17Z, `test(onboarding): preserve paired prefill behavioral regressions
  (#528)`). `github.com` and `pub.dev` both return HTTP 200. A read-only
  comparison using the public GitHub REST API plus local `git hash-object`
  (no fetch, no clone, no remote configured, no `git config` writes) found:

  - 2,069 of 2,081 untracked local paths exist in official `main`; 1,802 shared
    paths match the remote blob size, and 8 of 10 sampled shared files match
    the remote blob SHA exactly (`go.mod`, `go.sum`, `AGENTS.md`, `README.md`,
    `docker-compose.yml`, `idl/api.thrift`, `docs/dev/pm.md`,
    `scripts/cloud/deploy_main.sh`).
  - The local snapshot is **stale**, not a fork: 267 shared paths differ in
    size (256 are smaller locally); `CLAUDE.md` and
    `api/handler_gen/eigenflux/api/api_service.go` differ in content; four
    files added by the newest upstream commit are absent locally. The exact
    historical commit this snapshot came from cannot be determined without
    walking the full history.
  - None of `hub/`, `fluxspace_client/`, `production/epics/flux-theatre/`,
    `fluxspace/`, `docs/architecture/adr-0002-*` or `production/` exist in
    official `main`. All 66 Flux Theatre tracked files are new work that has
    no upstream counterpart.
  - 12 untracked paths are also local-only (`design/game-brief.md`, the four
    `fluxspace/_*.js`, `fluxspace/index.html`, `project.yaml`,
    `docs/adoption-plan-2026-11-04.md`, `docs/engine-reference/flutter/VERSION.md`,
    `production/epics/index.md` and the two `.workbuddy/memory/` files).

  **Base-commit search (15 read-only API calls, no 403/429):** the local
  `api_service.go` blob `116bd36d…` was introduced by
  `598fc56c6b8db75c30436bc30b7f11b1bf744c55` (2026-10-29T11:02:13Z) and
  replaced by `acaddcdb…` in `7bf9e7c3fea250126e0112e10d776f8f4ffec6e2`
  (2026-11-03T08:54:27Z), so the base lies in **[2026-10-29, 2026-11-03)**.
  The closest sampled tree is `a2afde15a6822cd8507935c24c046b4f24dd56ee`
  (2026-11-03T08:51:23Z): 1,926 of 2,080 shared paths match blob SHA. But it
  is **not an exact match** — 154 shared paths differ and 80 paths exist only
  locally (Flux Theatre files, `.workbuddy/memory/`, `design/game-brief.md`,
  the frozen `fluxspace/` scripts and others). No sampled commit's tree equals
  the local file set. The precise base SHA could not be determined within the
  API budget; the local snapshot is a stale copy with local modifications, not
  a clean checkout of any single upstream commit.

  Consequence for decision 3: the remote exists and is readable, so the owner
  no longer needs to supply a URL. What remains is a decision about **how** to
  rebase this stale, partly local-only snapshot onto `origin/main` — a
  non-trivial merge that touches 2,081 files and has no clean base commit.
  No remote was configured, no fetch attempted, no credentials stored.
  `credential.helper` is `manager`; `gh` is not installed.

- **G4 is now tracked as an open design decision.** `fluxspace-contracts.md`
  §9 lists G4 (story 002 needs a real authorised replay clip and a live model
  adapter but declares only story 001 as a dependency), yet `EPIC.md`'s open
  decisions listed only G1, G2/G3, G5+§8 and G6/G7. G4 has been added there
  and to the sign-off draft.

- **`production/epics/flux-theatre/DESIGN_DECISIONS_DRAFT.md` was a draft, now
  signed.** At the time of this ADR it offered three options per gap (G2, G3,
  G5, §8, G6, G7, G4) with a recommended option and an empty sign-off table.
  On the same day the owner selected option A for all seven; see
  `adr-0004-signed-design-decisions.md`. The draft is retained as the option
  record. The second half of G1 (retune vs relax) was likewise resolved later
  that day by `adr-0003-relaxed-calibration-targets.md` (relax the targets).
  Items 1 and 3 in "Still open" below are therefore superseded by ADR-0003 and
  ADR-0004 respectively, and are kept here only as the historical record of
  what was open when this ADR was written.

- **Flutter 3.47.9 / Dart 3.13.7 is installed and verified (2026-11-06).** The
  original local archive was truncated (618 MB, no EOCD, 443 local file
  headers, zero EOCD records); three independent readers rejected it. The
  official archive is 1.80 GB. `storage.googleapis.com` delivered at ~90 KB/s
  (projected >5 hours), so the transfer was switched to the official Flutter
  China mirror `storage.flutter-io.cn` (~20 MB/s). **Mirror trust is not
  assumed**: the result was checked byte-for-byte against the official release
  metadata — 1,939,451,626 bytes, sha256
  `d8e2b5977e09082c6440f89dc1aa887b94c8fbcc3b39222797046295a3f68a9e`,
  `unzip -t` clean. Installed to
  `C:\Users\louvr\.workbuddy\toolchains\flutter` (user directory; no admin
  rights, no system PATH or registry changes).
  Results: `flutter --version` PASS; `flutter pub get` PASS (122 packages,
  `pubspec.lock` generated); `flutter analyze` PASS (`No issues found!`);
  **`flutter test` PASS — 3 tests**. Two host-network obstacles were cleared
  with **no source change**: (a) `drift`'s transitive `sqlite3 3.9.0`
  native-asset hook fetches `sqlite3.x64.windows.dll` from `github.com`
  releases, which the proxy intermittently rejects with 502 — the DLL was
  obtained via `curl`, sha256-verified against the package's own
  `asset_hashes.dart` (`35d811b3…`) and placed in the hook's shared cache,
  whose own hash check then reuses it; (b) the proxy variables also broke the
  local `flutter_tester` WebSocket, fixed by unsetting them and setting
  `NO_PROXY=localhost,127.0.0.1` for the test process only.

  **Lock-file caveat:** `pubspec.lock` records `https://pub.flutter-io.cn` for
  all 117 hosted packages because the mirror was needed for usable download
  speed. Six sampled sha256 values matched the official `pub.dev` API, so the
  artifacts are identical, but committing mirror URLs would hard-code a
  China-only source. The lock is left **untracked** pending an owner decision.
  Pinning the twelve `any` constraints is likewise a separate task.

## Still open (owner decisions)

> **Update 2026-11-06 (same day):** items 1 and 3 were resolved after this ADR
> was written — by ADR-0003 (relax the G1 targets) and ADR-0004 (select option A
> for all seven design gaps). They remain listed below as the historical state
> at signing time. Items 2, 4, 5 and 6 are still genuinely open.

| # | Question | Why it cannot be inferred |
|---|---|---|
| 1 | ~~Retune `_check.js` (break the ADR-0001 freeze and rebuild goldens), or relax the GDD targets to the measured 10% baseline?~~ **RESOLVED by ADR-0003: relax the targets.** | Was a design decision; `IMPLEMENTATION_PLAN.md` forbids tuning numbers to make a test green. |
| 2 | Choose how to rebase the stale, partly local-only 2,081-file snapshot onto `origin/main` (`6aefaa0a…`) without losing Flux Theatre work or mixing unreviewed third-party files into an AI-authored commit. | Provenance is broadly known but **no exact base commit exists**: the closest sample (`a2afde15…`, 2026-11-03) still has 154 divergent shared paths and 80 local-only paths. A clean `rebase`/`merge` is not available; this is an owner call. |
| 3 | ~~Sign off one option per gap in `DESIGN_DECISIONS_DRAFT.md`.~~ **RESOLVED by ADR-0004: option A for all seven gaps.** | G2/G3/G5/G6/G7/G4 changed what S3-S5 are required to deliver. |
| 4 | Enable WSL 2 and start a Docker daemon. | Needs admin rights and a reboot. G10 cannot close without it. |
| 5 | Approve pinning the twelve `any` versions from `pubspec.lock`, and choose how to handle the lock's `pub.flutter-io.cn` URLs (normalise to `pub.dev` and commit / commit as-is / leave untracked). | Both are source or dependency-behaviour changes. `flutter test` already passes without them, so there is no pressure to decide hastily — but committing mirror URLs would make the project China-only. |
| 6 | Decide whether to push / open a PR / merge to `main`. | ADR-0001 decision 5 reserves these for the owner. Nothing has been pushed. |
