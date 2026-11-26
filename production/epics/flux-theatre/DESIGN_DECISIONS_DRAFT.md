# Design Decisions Draft - Owner Sign-off Required

> **DRAFT — NOT A DECISION. Every item below requires an explicit owner choice. Engineering must not implement any option until the owner selects it.**
> **SIGNED 2026-11-06:** All seven gaps resolved by selecting option A. See adr-0004-signed-design-decisions.md. This file is retained as the historical option record; the effective decisions live in ADR-0004.

## Scope

This draft records open design conflicts for owner adjudication only; it is not an effective decision and carries no implementation authority. The authority chain, in priority order, is `design/game-brief.md` (behaviour, rules, numeric targets, build order) > `production/epics/flux-theatre/IMPLEMENTATION_PLAN.md` (slice plan, exit criteria, verification record) > `docs/architecture/adr-0001-fluxspace-boundaries.md` (accepted architecture decisions) > `docs/architecture/fluxspace-contracts.md` (wire protocol, storage schema, known gaps) > `fluxspace/` (frozen, read-only executable specification). See `production/epics/flux-theatre/EPIC.md` L23-31 and `production/epics/flux-theatre/IMPLEMENTATION_PLAN.md` L1-8. The ten Chinese `story-*.md` files are `Status: Superseded` and serve only as a historical requirements vocabulary (`EPIC.md` L16, L49; `IMPLEMENTATION_PLAN.md` L10-13; `adr-0001-fluxspace-boundaries.md` L63-65). Every section below cites verified paths, line numbers and values; any figure that could not be confirmed is marked "verify before sign-off".

## G2 — Two incompatible persona dimension systems

### Current conflict

`docs/architecture/fluxspace-contracts.md` §1 (L7-29) records two incompatible six-dimension systems: the GDD persona traits `risk, information, social, commitment, expression, competition` on `-2..+2` (L13), and the engine vector `risk, logic, empathy, coop, aggression, intuition` on `0..100` from `fluxspace/_check.js` `scoreAction` (L14). `hub/models/persona.go`'s `ToPersonality` method implements an explicit, tested approximation (its doc comment calls it "a documented MVP approximation, not a claim of equivalence" and clamps every output to `0..100`), but `fluxspace-contracts.md` L27-29 state that this is "**not** an equivalence claim" and that "Story 002 AC9 ('every persona's behaviour and game preference matches the brief') cannot be signed off until design confirms or replaces this mapping." The conflict: AC9 demands behaviour match the brief, while the only implemented bridge is a non-equivalent approximation that design has never confirmed.

### Options

| Option | Core rationale | Main trade-off |
|---|---|---|
| A — Confirm the existing `hub/models/persona.go` approximation `(Recommended)` | It is already implemented, tested and value-clamped, and it is one of the two resolutions explicitly named at L29. It unblocks AC9 with zero code churn and does not touch the frozen engine vector or the golden tests derived from it, which is the lowest-risk path given `fluxspace/` is never edited. | You accept a bridge the contract itself calls "not an equivalence claim", so "matches the brief" is only loosely satisfied. The GDD traits stay a parallel vocabulary with no single source of truth, and future trait changes have no defined propagation into the engine vector. |
| B — Make the GDD traits the single source of truth | Removes the dual vocabulary and gives AC9 a literal, design-authored basis. | The engine vector is consumed by `fluxspace/_check.js` `scoreAction`, which is frozen and "never edited" (`adr-0001-fluxspace-boundaries.md` L20-21). Replacing it forces a `hub/referee` rewrite, invalidates `export_golden.js` / `golden.json` value-for-value parity, and loses the tested bridge and the `0..100` clamping that all downstream scoring relies on. |
| C — Establish an explicit bidirectional conversion table with consistency tests | Keeps both systems intact, turns the "not an equivalence claim" gap into a concrete, testable statement, and gives AC9 a measurable sign-off criterion. | You must define, document and permanently maintain two vocabularies plus their round-trip tests. It is the largest engineering cost of the three and still leaves neither system authoritative. |

### Owner decision needed

Do you confirm the existing `hub/models/persona.go` approximation as the approved basis for signing off Story 002 AC9, or select B or C to replace it?

## G3 — Three persona definitions not in the GDD

### Current conflict

`fluxspace_client/assets/personas/gu_yuan.yaml`, `su_wan.yaml` and `shen_mo.yaml` each carry at L2-3 the marker `# ADDED BY IMPLEMENTATION (not in GDD): occupation, tagline, behavior_notes, family_bias. Pending design sign-off.` `lin_shen.yaml` L2 states it "is the only persona whose behavior_notes / family_bias exist in the GDD". `docs/architecture/fluxspace-contracts.md` §2 (L33-36) confirms: "Only `lin_shen.yaml` has `behavior_notes` and `family_bias` in the GDD. The other three files were filled in during implementation ... Do not treat them as GDD-authored content." The conflict: three of four shipped persona definitions contain fields design never authored, yet `design/game-brief.md` is the declared source of truth.

### Options

| Option | Core rationale | Main trade-off |
|---|---|---|
| A — Retroactive sign-off of the three YAMLs `(Recommended)` | The added fields are already written, internally consistent and consumed by the client. Owner sign-off converts implementation-authored content into approved content without discarding work or blocking S3, and it is exactly what the in-file "Pending design sign-off" marker asks for. | You inherit content design did not author, and `design/game-brief.md` still does not contain these definitions. The "single source of truth" stays incomplete and future readers depend on a sign-off record rather than the GDD. |
| B — Strip the added fields and keep only GDD-authored content | Restores the GDD as the literal source of truth and removes every unapproved field. | You lose `occupation`, `tagline`, `behavior_notes` and `family_bias` for three personas; the client loses copy and behavioural cues it already renders; S3's "four recognisable personas" goal (`IMPLEMENTATION_PLAN.md` L31) is weakened; and the work must be redone if design later wants those fields. |
| C — Have design rewrite the three YAMLs from scratch | Produces genuinely GDD-authored definitions and closes the provenance gap properly. | It is the slowest path, requires design authoring time, and any implemented behaviour tuned to current values must be re-validated against rewritten files. S3 stays blocked until design delivers. |

### Owner decision needed

Do you retroactively sign off the three implementation-added persona YAMLs as approved content, or select B or C?

## G5 — Numeric conflicts

### Current conflict

`docs/architecture/fluxspace-contracts.md` L319 (§9 G5) records two numeric conflicts. (1) Story 009 AC4 "1 per character per day" versus the GDD "important-memory alerts max 2/day": `design/game-brief.md` L387 sets "1 per character per day" for a followed character completing a match, while L390 sets "Max 2 per day" for an important memory. (2) Story 008 AC10 flat "50-500 FP" versus the GDD's per-category price ranges: `design/game-brief.md` L889 lists scene themes 50–300 FP, avatar frames 50–200 FP and spectator effects 100–500 FP. Both stories are among the ten `Superseded` legacy files (`EPIC.md` L49), yet their numbers still conflict with the GDD that outranks them.

### Options

| Option | Core rationale | Main trade-off |
|---|---|---|
| A — The GDD wins `(Recommended)` | `design/game-brief.md` is the declared source of truth for "numeric targets" (`EPIC.md` L27) and the legacy stories are a superseded historical vocabulary only (`EPIC.md` L16, L49). Adopting the GDD keeps the authority chain internally consistent. | You lose Story 009 AC4's "1 per character per day" in favour of the GDD's "max 2/day" for the important-memory case, and replace the simpler flat 50-500 FP band with three per-category ranges. Any AC, client pricing display or test that hardcoded the story numbers must be rewritten. |
| B — The legacy stories win | Preserves the acceptance criteria as originally written and keeps the flat FP band. | It elevates ten `Superseded` Chinese stories above the GDD and ADR-0001, contradicts the authority chain, and leaves the GDD's per-category ranges and "max 2/day" cap unimplemented. The numeric source of truth becomes ambiguous. |
| C — Define a new set of numbers and edit both places | Produces one internally consistent, jointly-owned figure instead of picking a side. | It requires a fresh design decision on every affected value, edits to both `design/game-brief.md` and the legacy/contracts records, and re-verification of every dependent AC and test. It is the largest scope of the three and still needs owner-chosen values. |

### Owner decision needed

For the two numeric conflicts, which source governs — the GDD (A), the legacy stories (B), or a new jointly-authored set (C)?

## §8 — Per-companion monthly token cap

### Current conflict

`docs/architecture/fluxspace-contracts.md` §8 Budgets, L306, reads verbatim: `| Per-companion monthly cap | 200,000 tokens | **New default, GDD silent — confirm** |`. The GDD is entirely silent on a monthly per-companion cap; the other three rows (L303-305) cite GDD lines 787/787/643. L308-309 add that phase 1 spends zero tokens and that over-limit behaviour is a downgrade to the local rule engine, never a silent quality reduction. The conflict: a hard budget number with no design basis is currently documented as a default requiring confirmation.

### Options

| Option | Core rationale | Main trade-off |
|---|---|---|
| A — Ratify 200,000 tokens `(Recommended)` | It is already the documented default; phase 1 spends zero tokens (L308) so ratification changes no immediate behaviour; the over-limit path is an explicit rule-engine downgrade (L308-309); and the figure can be revisited once real model spend begins. | You lock in a number the GDD never justified. If live usage diverges you need a second decision to change it, and a hard cap forces a mid-month downgrade to the local rule engine rather than absorbing a spike. |
| B — Set a different number | Lets design pick a cap grounded in expected usage instead of an arbitrary default. | The owner must supply a specific value (none is recorded anywhere), and every dependent budget test, alert threshold and downgrade trigger must be re-derived. Until a value is given this option cannot be implemented. |
| C — Dynamic usage-based budget with no hard cap | Avoids an arbitrary ceiling and adapts to actual spend. | You lose the predictable cost control that §8 and S5's "predictable cost" goal exist to provide, give up the defined over-limit downgrade behaviour, and must build metering/forecasting logic that does not exist today. |

### Owner decision needed

Do you ratify 200,000 tokens/companion/month as the hard cap; if not, what exact value (B), or do you choose the dynamic no-cap budget (C)?

## G6 — No Rive/2.5D assets

### Current conflict

`production/epics/flux-theatre/IMPLEMENTATION_PLAN.md` L31 sets the S3 exit criterion: "60fps Flame scene; Rive idle/walk/expression for four personas; retained screenshot evidence under `production/qa/evidence/`". `docs/architecture/fluxspace-contracts.md` L320 (§9 G6) states "Story 006 needs four personas × idle/walk/expression plus backgrounds at 60fps; no asset files, art bible or evidence directory exist." Verified in this repository: there is no `*.riv` file, no `*art*bible*` file, `production/qa/evidence/` does not exist, and `fluxspace_client/assets/` contains only the four persona YAML files and nothing else. The conflict: S3's exit criterion demands Rive/2.5D assets and screenshot evidence that do not exist.

### Options

| Option | Core rationale | Main trade-off |
|---|---|---|
| A — Procure or commission the assets externally `(Recommended)` | It is the only option that satisfies the S3 exit criterion as written without redefining acceptance or stalling the slice; it preserves the four distinct persona animations, the 60fps Flame scene and the retained evidence. | It introduces real cost and lead time, requires an art bible/spec for four personas plus backgrounds and a vendor, and S3 remains blocked until assets are delivered and integrated. |
| B — Procedural placeholder art with an explicit degradation label | Unblocks S3 progress now with no external spend, and the degradation is labelled and owner-accepted rather than silent. | You lose the four Rive idle/walk/expression animations and the 2.5D look. The screenshot evidence documents placeholder art, the Rive/Flame requirement is effectively waived for MVP, and the owner must formally accept a degraded S3 acceptance the criterion does not authorise. |
| C — Defer S3 until assets arrive | Keeps the acceptance bar intact without spending on placeholders. | The whole S3 slice (four personas, omniscient/subjective views, event director, viewer validation) stays blocked, S4 work that depends on its outputs is delayed, and the plan has no runnable S3 milestone until an unspecified future date. |

### Owner decision needed

Do you authorise external procurement/commission of the four-persona Rive/2.5D assets, accept the degraded labelled placeholder (B), or defer S3 (C)?

## G7 — Onboarding clip is a labelled demo

### Current conflict

`design/game-brief.md` L7 requires guests to begin with "a real authorized story clip" (also L906 in the "MVP — what must exist to be the game" section). `docs/architecture/fluxspace-contracts.md` L321 (§9 G7) states "No real authorised replay exists. Phase 1 uses a deterministic demo event stream, explicitly labelled. This discounts GDD's 'real + authorized'." `IMPLEMENTATION_PLAN.md`'s "Known gaps" §G7 repeats: "**G7 — onboarding clip is a labelled demo**, not the 'real authorised' clip the GDD requires, because no real replay exists yet." The conflict: the MVP onboarding clip is a labelled demo, while the GDD requires a real, authorized replay.

### Options

| Option | Core rationale | Main trade-off |
|---|---|---|
| A — Accept the labelled demo as an MVP interim substitute `(Recommended)` | A real authorized replay cannot exist until onboarding itself has run, so this is a bootstrap problem. The demo is explicitly labelled (`fluxspace-contracts.md` §9 G7; `IMPLEMENTATION_PLAN.md` "Known gaps" §G7) and therefore does not silently claim to be real, and it lets onboarding proceed while S4 builds real replay and consent. | MVP opens with a clip the GDD explicitly says must be "real + authorized". You ship a known, labelled deviation that must later be replaced, and any consent or authorization semantics shown in the demo are illustrative only. |
| B — Require a real authorized replay clip before onboarding ships | Keeps the GDD intact with no deviation. | Onboarding cannot ship until a real match has been played and its replay consented to — a circular dependency, since the onboarding needed to create that replay is itself blocked. S3 stalls on S4 artefacts. |
| C — Cut the onboarding clip from MVP scope | Removes the conflict without faking a clip. | You lose the GDD's opening experience entirely ("real clip, omniscient director cut, local follow"; `design/game-brief.md` L7, L906). Guest onboarding is reduced and the S3 viewer-validation exit criterion loses its primary content. |

### Owner decision needed

Do you accept the explicitly labelled deterministic demo clip as an MVP interim substitute for the GDD's "real + authorized" onboarding clip, or select B or C?

## G4 — Story 002 dependency inversion

### Current conflict

`docs/architecture/fluxspace-contracts.md` L318 (§9 G4) reads verbatim: "Needs a real authorised replay clip (story 008) and a live model adapter (story 004/010), but lists only story 001." The gap existed in §9, but `EPIC.md`'s "Open decisions owned by design" listed only four items — G1, G2/G3, G5 + contracts §8, and G6/G7 — and omitted G4. `adr-0002-s1-acceptance-and-g1-baseline.md` now backfills G4 into that list and into `IMPLEMENTATION_PLAN.md`'s "Known gaps"; this draft supplies the three options for owner adjudication. The conflict: story 002 declares a dependency on story 001 only, while its actual prerequisites are S4 artefacts.

### Options

| Option | Core rationale | Main trade-off |
|---|---|---|
| A — Re-hang story 002 onto S3 with explicit dependencies on S4 artefacts `(Recommended)` | It fixes the inversion honestly: story 002 genuinely needs the authorised replay clip (story 008) and a live model adapter (story 004/010). It keeps the slice dependency graph truthful without faking the clip or cutting a required feature. | Story 002 is no longer an early win; S3 becomes gated on S4 outputs, pushing onboarding later and possibly reordering the vertical slices, and you lose the ability to demo onboarding before S4 lands. |
| B — Substitute a labelled demo clip plus the local rule engine | Removes the hard dependency on S4 and lets onboarding proceed on phase-1 assets. | It compounds G7's deviation, gives up the "real authorised replay" and "live model adapter" the gap names, and leaves behaviour claims resting on a rule engine rather than a model. The inversion is papered over, not resolved. |
| C — Cut story 002 / onboarding from MVP | Eliminates the inverted dependency cleanly. | You lose guest onboarding and companion activation, which `design/game-brief.md` L906 names as an MVP requirement, and the S3 "four recognisable personas" work loses its entry point. |

### Owner decision needed

Do you approve re-hanging story 002 onto S3 with explicit upstream dependencies on the S4 replay clip and live model adapter, or select B or C?

## Sign-off record

All seven decision fields are signed on 2026-11-06 by the owner: option A is selected for every gap. This sign-off record is complete; the effective decisions live in `docs/architecture/adr-0004-signed-design-decisions.md`, and this file is retained as the historical option record.

| Gap | Option chosen | Owner initials | Date |
|---|---|---|---|
| G2 | A | Owner | 2026-11-06 |
| G3 | A | Owner | 2026-11-06 |
| G5 | A | Owner | 2026-11-06 |
| §8 | A | Owner | 2026-11-06 |
| G6 | A | Owner | 2026-11-06 |
| G7 | A | Owner | 2026-11-06 |
| G4 | A | Owner | 2026-11-06 |

## Not covered here

The following are explicitly outside this draft's adjudication. They remain environment or engineering scope, not owner design choices made here.

- **G1 — calibration baseline and targets.** The owner selected the full-Runtime `fluxspace/_balance.js` as the authoritative baseline on 2026-11-06 (ADR-0002 decision 2; G1 evidence in `docs/architecture/fluxspace-contracts.md` L315; pinned as the prototype baseline in `hub/generator/calibrator_test.go` L41). The owner later **relaxed the GDD targets** (`adr-0003-relaxed-calibration-targets.md`): the three-path win-rate and >=60% non-`rounds_out` gates are retired and the measured 10% is a regression floor. This section records the state when the draft was written. `fluxspace/` is "never edited or extended" (`docs/architecture/adr-0001-fluxspace-boundaries.md` L20-21) and "Never edited" (`EPIC.md` L31); any retune that edits the frozen prototype would break that freeze and invalidate the 7 functions exported from `_check.js` by `hub/testdata/export_golden.js` (L20) and the value-for-value `hub/testdata/golden.json` comparisons. That is why the two G1 sub-decisions were recorded separately, and why the owner chose to relax rather than retune.
- **G10 — live-stack e2e.** A real EigenFlux full-stack end-to-end run has never executed; it requires WSL 2 and a running Docker daemon (`docs/architecture/fluxspace-contracts.md` L324; `IMPLEMENTATION_PLAN.md` "Verification results" and "Known gaps" §G10; `docs/architecture/adr-0001-fluxspace-boundaries.md` L71-74). The S1 loop currently runs over real HTTP against a local gateway stand-in only. ADR-0002 decision 1 keeps the exit criterion verbatim rather than waiving it.
- **G11 — post-match Agent Card projection.** Deliberately deferred to S4, where the owning client holds the Agent token. `GatewayPort.PublishAgentCardFields` is implemented and shape-verified, but the follow-up sender only sends PMs (`docs/architecture/fluxspace-contracts.md` L325; `IMPLEMENTATION_PLAN.md` "Known gaps" §G11).
- **Git remote baseline.** The working tree has no configured `remote` and no `upstream` for branch `feature/flux-theatre-mvp`; 2081 files are untracked. ADR-0002 decision 3 requires remote alignment before any bulk commit. The first `git ls-remote` returned a transient 502, but a retry succeeded (`main` = `6aefaa0a61cf32ab2b24398333690c6863aa83e3`), and a read-only API comparison shows the snapshot is a stale derivative of upstream with all 66 Flux Theatre files being new work. The remaining question is merge strategy, not connectivity. No remote was configured, no fetch attempted, no credentials stored.
