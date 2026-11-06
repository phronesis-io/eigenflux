# ADR-0003: Relaxed calibration targets for the MVP

- **Status**: Accepted
- **Date**: 2026-11-06
- **Scope**: `design/game-brief.md`, `production/epics/flux-theatre/story-005-generator-skeleton.md`, `hub/generator/calibrator.go`, `hub/generator/calibrator_test.go`, G1 and the S2 exit criterion
- **Relates to**: ADR-0001, ADR-0002, `production/epics/flux-theatre/IMPLEMENTATION_PLAN.md` (S2), `docs/architecture/fluxspace-contracts.md` §9 (G1)

## Context

ADR-0002 split the G1 calibration question into two owner sub-decisions.
Sub-decision 1 was taken there: the single authoritative baseline is
`fluxspace/_balance.js`, the frozen full-Runtime harness. It runs
`generateGame` afresh for every match, drives per-persona `decideAction`,
resolves branch events, rotates companions, and covers six groups
(2 strategies `random`/`greedy` x 3 modes `solo`/`coop`/`versus`) x 80
matches = 480 matches. Its measured result is **7 wins / 41 losses / 432
timeouts = 10% non-`rounds_out` endings**.

Sub-decision 2 was explicitly **NOT taken** in ADR-0002: whether to retune the
balance or relax the GDD targets. This ADR takes it. The owner has ruled to
**relax the targets** and to keep `fluxspace/` frozen.

Two facts constrain the ruling:

- `hub/generator/calibrator.go` is a **different simulator, not a second
  measurement of the same thing**. It replays one fixed skeleton 480 times
  with a simplified three-strategy seat picker and no branch events, and
  reports three-path win rates of 0% / 0% / 0% and 0% non-`rounds_out`. It is
  not the authoritative baseline.
- The authoritative baseline **does not measure three-path win rates at
  all**. `_balance.js` measures `random`/`greedy` win-lose-timeout, not
  optimal/random/worst path victory rates. Under the new authoritative
  baseline there is therefore **no measurement data** for the GDD's
  `~90% / 40-60% / ~10%` targets. No percentage for them can be asserted as
  "measured", and this ADR invents none.

Retuning is not a numeric tweak either. Every literal that would move lives
inside the frozen `fluxspace/_check.js`; ADR-0002 Consequences records that
editing it invalidates the `hub/testdata/export_golden.js` bridge (seven
exported functions: `hashStr`, `fingerprintOf`, `estimateMinutes`,
`fatigueMul`, `computeRoundDelta`, `scoreAction`, `evalCondition`), the
1,071-line `golden.json`, and every value-for-value Go assertion built on
them. ADR-0001 decision 1 states `fluxspace/` "is never edited or extended"
(`adr-0001` L20-21, `EPIC.md` L31, `IMPLEMENTATION_PLAN.md` L8).

## Decision

The owner rules to **relax the GDD calibration targets for the MVP** and to
**leave the prototype frozen**. This is a design ruling, not a porting
defect. The new acceptance wording is deliberately unvarnished:

1. **No three-path win-rate hard gate in the MVP.** The targets `~90%`
   (optimal), `40-60%` (random) and `~10%` (worst)
   (`design/game-brief.md` L35-37; story 005 AC2; the `Validated` thresholds
   `optimal>=0.80 / random 0.30-0.70 / worst<=0.20` in
   `hub/generator/calibrator.go` L162-165) are **retired / unreachable**.
   They are retired because the authoritative baseline does not measure that
   metric, not because a port failed, and because the owner chose to relax
   them rather than break the freeze. No replacement percentage is declared,
   because none has been measured.
2. **Non-`rounds_out` is a recorded regression floor, not a gate.** The
   `>=60% of 480 matches reach a non-`rounds_out` ending` requirement
   (story 005 AC10; `Validated`'s `nonRoundsOut>=0.60`) is **retired /
   unreachable**. The **10%** produced by `fluxspace/_balance.js` is
   registered as the MVP baseline and regression floor: future changes must
   not silently regress it, but reaching `>=60%` is no longer required.
3. **`fluxspace/` stays frozen; the golden chain is unchanged.** No file
   under `fluxspace/` is edited. `_check.js`, `export_golden.js`,
   `golden.json` and the Go value-for-value assertions are left exactly as
   they are. `EPIC.md`, `IMPLEMENTATION_PLAN.md` and
   `fluxspace-contracts.md` are not modified by this change; they are
   synchronised separately by the owner.
4. **The Go calibrator is demoted to a structural self-check.**
   `hub/generator/calibrator.go` is retained for skeleton reachability and
   path-ordering properties (optimal >= worst, random sits between them,
   round-limit endings are never neutral) and for its 480-match smoke run.
   It is **no longer an S2 balance-acceptance criterion**. Its `Validated`
   flag and the four numeric thresholds remain in the code unchanged as
   legacy structural self-check thresholds, documented as such; they are not
   used to accept or reject S2.

## Consequences

- **S2 exit criterion is rewritten accordingly.** The `EPIC.md` L38 /
  `IMPLEMENTATION_PLAN.md` L30 clause "Three-path calibration meets GDD
  targets" is retired by this ruling; those files, plus
  `fluxspace-contracts.md` §9 (G1), are synchronised separately by the owner
  and are not touched here. S2 keeps the 480-match run, the structural-
  fingerprint non-repetition check, and the structural reachability/ordering
  checks. The non-`rounds_out` figure becomes a recorded 10% regression
  floor in the authoritative baseline, not a `>=60%` pass condition. With
  the targets retired, G1 no longer blocks S2 on calibration grounds; the
  live-stack sign-off (G10, ADR-0002 decision 1) remains a separate open
  item.
- **`TestCalibrationGDDGapIsTracked` changes meaning.** It no longer pins
  "target unmet" as a failure and no longer errors when `Validated` becomes
  true, because meeting a retired target is no longer an error. It now
  records and logs both simulators' measured values (authoritative
  `_balance.js` 10% non-`rounds_out`; Go structural self-check 0%) and
  asserts the fact that the two simulators are different measurements. The
  other 67 tests and all settlement maths are untouched; the suite stays at
  68 green tests.
- **Restoring three-path win rates as an acceptance item is future work, not
  claimed here.** Before any `~90% / 40-60% / ~10%` figure can become a gate
  again, measurement capability for that metric must FIRST be added to the
  authoritative baseline (or reconciled to it, ADR-0002 decision 2). This
  ADR does not add that capability and does not assert any such percentage.

## Not claimed done

**Rebuilding `hub/generator/calibrator.go` into a full-Runtime-equivalent
implementation was NOT done.** This ADR deliberately leaves the Go
calibrator as a structural self-check instead of making it a second copy of
the authoritative baseline. Doing so would require porting, at minimum:

- `generateGame` regeneration per match (the Go calibrator currently reuses
  one fixed skeleton 480 times);
- per-persona `decideAction` driven by the six-dimension personality model;
- branch events and their triggers (currently excluded from calibration);
- the six mode/strategy groups (`solo`/`coop`/`versus` x `random`/`greedy`)
  and rotating companions.

Even with that work, three-path win rates are a metric the prototype does
not define, so equivalence to the Runtime would not by itself supply the
missing three-path measurement. Duplicating the frozen specification also
risks a second, drifting source of truth. The work is therefore recorded as
future work and is explicitly **not** a deliverable of this ADR. No
percentage was fabricated as a "measured" value to fill the gap.
