# ADR-0004: Signed owner design decisions for the seven Flux Theatre gaps

- **Status**: Accepted
- **Date**: 2026-11-06
- **Scope**: `production/epics/flux-theatre/DESIGN_DECISIONS_DRAFT.md`, `docs/architecture/fluxspace-contracts.md` §8 and §9 (G1-G7), and the S3/S4/S5 acceptance criteria
- **Relates to**: ADR-0001 (`adr-0001-fluxspace-boundaries.md`), ADR-0002 (`adr-0002-s1-acceptance-and-g1-baseline.md`), ADR-0003 (`adr-0003-relaxed-calibration-targets.md`)

## Context

`production/epics/flux-theatre/DESIGN_DECISIONS_DRAFT.md` was produced as an
owner-adjudication record, not an effective decision. Its header is an
explicit `DRAFT — NOT A DECISION` warning: "Every item below requires an
explicit owner choice. Engineering must not implement any option until the
owner selects it." It lays out seven open design conflicts (G2, G3, G5,
contracts §8, G6, G7, G4), each with a `Current conflict` section citing
verified paths, line numbers and values, three costed options, and a
`Sign-off record` table left as `TBD` pending the owner's choice. The same
status is recorded in `EPIC.md` "Open decisions owned by design" (items 2-5
map to G2/G3, G5 + contracts §8, G6/G7 and G4) and in
`fluxspace-contracts.md` §9, which describes the draft as "not a decision —
it offers three costed options per design gap with an empty sign-off table".

The paragraphs above describe the state **before** the sign-off. Since the
ruling, `DESIGN_DECISIONS_DRAFT.md` carries a `SIGNED 2026-11-06` note and a
filled sign-off record, `EPIC.md` has a "Decisions taken" table in place of the
open-decisions list, and `fluxspace-contracts.md` §9 marks G1-G7 `RESOLVED`.
They are quoted here as history so the reasoning stays auditable.

These are owner design choices, not engineering tasks: they decide the
source of truth (`design/game-brief.md` versus the ten superseded Chinese
legacy stories), which numbers govern, which acceptance criteria are
authoritative and what may ship in the MVP. Under the authority chain
(`EPIC.md` L23-31) only the owner can settle them. ADR-0001 (accepted
architecture), ADR-0002 (S1 exit wording, G1 baseline, repository
provenance) and ADR-0003 (G1 relaxed calibration targets) do not adjudicate
these seven gaps. On 2026-11-06 the owner ruled to **sign all seven gaps at
once by selecting option A for every gap**. This ADR converts that ruling
into effective decisions.

The seven gaps gate the S3-S5 acceptance criteria, so leaving them open
blocks sign-off rather than merely deferring documentation:

- **S3** ("Four personas and the 2.5D director"; exit criterion "60fps Flame
  scene; Rive idle/walk/expression for four personas; retained screenshot
  evidence under `production/qa/evidence/`") is affected by G2 (the basis
  for Story 002 AC9 persona-behaviour sign-off), G3 (persona YAML
  provenance), G4 (story 002's dependency graph), G6 (Rive/2.5D assets) and
  G7 (the onboarding clip).
- **S4** ("Memory, consequences, replays"; exit criterion "SHA-256 archive
  verification; six viewing modes; all-participant consent for public
  replay") is affected by G5 (story 008/009 numeric values), G4 (its replay
  clip and live model adapter become explicit upstream prerequisites of
  story 002) and G7 (S4 must later supply the real authorised replay that
  replaces the demo).
- **S5** ("Harden before expanding"; exit criterion includes "budget
  downgrade path" and "predictable cost") is affected by contracts §8 (the
  200,000 tokens/companion/month cap and its over-limit downgrade, recorded
  as a blocking gap for story 010 in `EPIC.md` L62).

## Decisions

All seven gaps are resolved by **option A**. The `Selected option (A)` column
below quotes the draft verbatim — option label, core rationale and main
trade-off — so that nothing is paraphrased or simplified. Rows follow the
`Sign-off record` order in the draft.

| Gap | Selected option (A) | Consequence |
|---|---|---|
| 1. G2 | **A — Confirm the existing `hub/models/persona.go` approximation `(Recommended)`**<br><br>*Core rationale:* "It is already implemented, tested and value-clamped, and it is one of the two resolutions explicitly named at L29. It unblocks AC9 with zero code churn and does not touch the frozen engine vector or the golden tests derived from it, which is the lowest-risk path given `fluxspace/` is never edited."<br><br>*Main trade-off:* "You accept a bridge the contract itself calls "not an equivalence claim", so "matches the brief" is only loosely satisfied. The GDD traits stay a parallel vocabulary with no single source of truth, and future trait changes have no defined propagation into the engine vector." | Story 002 AC9 can now be signed off on the documented approximation; the engine vector, `fluxspace/` and the golden chain are untouched. |
| 2. G3 | **A — Retroactive sign-off of the three YAMLs `(Recommended)`**<br><br>*Core rationale:* "The added fields are already written, internally consistent and consumed by the client. Owner sign-off converts implementation-authored content into approved content without discarding work or blocking S3, and it is exactly what the in-file "Pending design sign-off" marker asks for."<br><br>*Main trade-off:* "You inherit content design did not author, and `design/game-brief.md` still does not contain these definitions. The "single source of truth" stays incomplete and future readers depend on a sign-off record rather than the GDD." | The three implementation-added persona YAMLs become approved content, so S3's four-persona copy is unblocked; the GDD still lacks those definitions. |
| 3. G5 | **A — The GDD wins `(Recommended)`**<br><br>*Core rationale:* "`design/game-brief.md` is the declared source of truth for "numeric targets" (`EPIC.md` L27) and the legacy stories are a superseded historical vocabulary only (`EPIC.md` L16, L49). Adopting the GDD keeps the authority chain internally consistent."<br><br>*Main trade-off:* "You lose Story 009 AC4's "1 per character per day" in favour of the GDD's "max 2/day" for the important-memory case, and replace the simpler flat 50-500 FP band with three per-category ranges. Any AC, client pricing display or test that hardcoded the story numbers must be rewritten." | The GDD numeric values govern; the legacy story numbers and everything that hardcodes them are superseded. |
| 4. §8 | **A — Ratify 200,000 tokens `(Recommended)`**<br><br>*Core rationale:* "It is already the documented default; phase 1 spends zero tokens (L308) so ratification changes no immediate behaviour; the over-limit path is an explicit rule-engine downgrade (L308-309); and the figure can be revisited once real model spend begins."<br><br>*Main trade-off:* "You lock in a number the GDD never justified. If live usage diverges you need a second decision to change it, and a hard cap forces a mid-month downgrade to the local rule engine rather than absorbing a spike." | 200,000 tokens/companion/month is ratified as a hard cap; over-limit behaviour downgrades to the local rule engine. |
| 5. G6 | **A — Procure or commission the assets externally `(Recommended)`**<br><br>*Core rationale:* "It is the only option that satisfies the S3 exit criterion as written without redefining acceptance or stalling the slice; it preserves the four distinct persona animations, the 60fps Flame scene and the retained evidence."<br><br>*Main trade-off:* "It introduces real cost and lead time, requires an art bible/spec for four personas plus backgrounds and a vendor, and S3 remains blocked until assets are delivered and integrated." | S3's asset criterion stays as written and S3 stays blocked until assets are procured and integrated. |
| 6. G7 | **A — Accept the labelled demo as an MVP interim substitute `(Recommended)`**<br><br>*Core rationale:* "A real authorized replay cannot exist until onboarding itself has run, so this is a bootstrap problem. The demo is explicitly labelled (`fluxspace-contracts.md` §9 G7; `IMPLEMENTATION_PLAN.md` "Known gaps" §G7) and therefore does not silently claim to be real, and it lets onboarding proceed while S4 builds real replay and consent."<br><br>*Main trade-off:* "MVP opens with a clip the GDD explicitly says must be "real + authorized". You ship a known, labelled deviation that must later be replaced, and any consent or authorization semantics shown in the demo are illustrative only." | The MVP ships a labelled demo clip in place of the GDD's "real + authorized" clip, to be replaced when S4 delivers real replay and consent. |
| 7. G4 | **A — Re-hang story 002 onto S3 with explicit dependencies on S4 artefacts `(Recommended)`**<br><br>*Core rationale:* "It fixes the inversion honestly: story 002 genuinely needs the authorised replay clip (story 008) and a live model adapter (story 004/010). It keeps the slice dependency graph truthful without faking the clip or cutting a required feature."<br><br>*Main trade-off:* "Story 002 is no longer an early win; S3 becomes gated on S4 outputs, pushing onboarding later and possibly reordering the vertical slices, and you lose the ability to demo onboarding before S4 lands." | Story 002 moves to S3 with explicit S4 prerequisites; S3 becomes gated on S4 outputs. |

## Consequences

S1 and S2 are not affected. S1 stays *code complete, live-stack sign-off
pending* (ADR-0002 decision 1; G10/G11 remain open environment/scope gaps),
and S2's G1 is adjudicated separately by ADR-0003. The seven rulings change
S3, S4 and S5 as follows.

1. **G2 — S3.** Story 002 AC9 ("every persona's behaviour and game preference
   matches the brief") may now be signed off against the confirmed
   `hub/models/persona.go` approximation. S3's "four recognisable personas"
   goal is therefore accepted on a documented, value-clamped approximation,
   not on literal equivalence; the GDD traits remain a parallel vocabulary
   with no single source of truth. S4 and S5: no change; `fluxspace/` and the
   golden chain are untouched.
2. **G3 — S3.** The three implementation-added YAMLs (`gu_yuan.yaml`,
   `su_wan.yaml`, `shen_mo.yaml`) are approved content, so S3 no longer waits
   on persona-copy provenance. `design/game-brief.md` still does not contain
   those definitions, so the "single source of truth" stays incomplete and
   future readers depend on this sign-off record. S4 and S5: no change.
3. **G5 — S4.** Story 009 AC4's "1 per character per day" is replaced by the
   GDD's "important-memory alerts max 2/day", and story 008 AC10's flat
   50-500 FP band is replaced by the GDD per-category ranges (scene themes
   50-300 FP, avatar frames 50-200 FP, spectator effects 100-500 FP). These
   feed S4's retention and replay acceptance (stories 008/009 are superseded
   by S4). S3 and S5: no direct numeric change.
4. **§8 — S5.** The 200,000 tokens/companion/month figure is ratified as a
   hard cap, giving S5's "predictable cost" / budget-downgrade exit criterion
   a definite number. Phase 1 spends zero tokens, so nothing changes
   immediately. S3 and S4: no change.
5. **G6 — S3.** Option A keeps the S3 exit criterion exactly as written
   (60fps Flame scene; Rive idle/walk/expression for four personas; retained
   screenshot evidence under `production/qa/evidence/`); acceptance is
   neither redefined nor waived. Because procurement is not complete, S3
   remains blocked, and S4 work depending on S3 outputs stays delayed. S5:
   no change.
6. **G7 — S3/S4.** The MVP opens with the explicitly labelled deterministic
   demo clip instead of the GDD's "real + authorized" clip
   (`design/game-brief.md` L7, L906). This is a labelled deviation that
   counts against the S3 viewer-validation criterion and must later be
   replaced when S4 delivers real replay and consent; any consent or
   authorization semantics shown in the demo are illustrative only. S5: no
   change.
7. **G4 — S3/S4.** Story 002 is re-hung onto S3 with explicit dependencies on
   S4 artefacts: the authorised replay clip (story 008) and the live model
   adapter (story 004/010). S3 becomes gated on S4 outputs, onboarding is
   pushed later, and the slices may be reordered; S4 must produce the replay
   clip and live model adapter before story 002 can be accepted. S5: no
   change.

### S3 is still blocked by G6; `EPIC.md`'s S3 status is not changed

**S3 remains blocked by G6.** Option A selects external procurement and
commissioning of the Rive/2.5D assets, which "introduces real cost and lead
time" and leaves "S3 ... blocked until assets are delivered and integrated".
No `*.riv` file, art bible or `production/qa/evidence/` directory exists yet,
so the S3 exit criterion is unmet. This ADR does **not** change `EPIC.md`'s
S3 status; that file, and `IMPLEMENTATION_PLAN.md`, are synchronised
separately by the owner. The option-B degraded labelled placeholder and the
option-C deferral were **not** selected, so the acceptance bar was neither
lowered nor deferred.

### Two registered, labelled MVP deviations (G7 + G4)

**G7 together with G4 means the MVP carries two registered, explicitly
labelled deviations. They are registered here honestly and must not be
presented as "GDD satisfied":**

1. **The demo clip.** The deterministic demo event stream stands in for the
   GDD's "real + authorized" onboarding clip (G7 option A). It is explicitly
   labelled in `fluxspace-contracts.md` §9 G7 and `IMPLEMENTATION_PLAN.md`
   "Known gaps" §G7, so it does not silently claim to be real.
2. **The rule-engine substitute.** G4 option A names the live model adapter
   (story 004/010) as an S4 prerequisite of story 002; that adapter does not
   exist in phase 1, which "spends zero tokens (local rule engine)"
   (`fluxspace-contracts.md` §8; `IMPLEMENTATION_PLAN.md` "Decisions taken by
   the owner" item 3). The local rule engine therefore stands in for the live
   model, and behaviour claims rest on the rule engine rather than a model
   until S4 delivers the adapter.

Both deviations are owner-accepted and labelled, and both must be replaced
when S4 ships the real authorised replay and the live model adapter. G4
option A keeps the dependency graph truthful ("without faking the clip or
cutting a required feature") precisely so these deviations remain visible.
Neither deviation closes the GDD requirement.

### G5 rewrites every hardcoded story number

**G5 means story 009 AC4's "1 per character per day" and the flat 50-500 FP
band are replaced by the GDD values.** Any acceptance criterion, client
pricing display or test that hardcoded the legacy story numbers must be
rewritten: AC4 becomes the GDD's "max 2/day" important-memory cap, and the
flat band becomes the three per-category ranges (50-300 FP, 50-200 FP,
100-500 FP). This is a documented rewrite obligation, not a claim that such
rewrites are already done.

### §8 is a hard cap with a rule-engine downgrade

**§8 is a hard cap, not a soft target.** At 200,000 tokens/companion/month,
over-limit behaviour is a downgrade to the local rule engine, never a silent
quality reduction (`fluxspace-contracts.md` §8 L308-309). S5's
budget-downgrade path is the acceptance surface for this behaviour. Changing
the number later requires a new owner decision, per the ratified option A's
trade-off.

## Supersedes

This ADR ends the option phase of
`production/epics/flux-theatre/DESIGN_DECISIONS_DRAFT.md`. The draft is
retained as the historical option record: its seven option tables, all
`Current conflict` paragraphs and the original `DRAFT` warning are preserved
verbatim, and its `Sign-off record` is backfilled with `A` / `Owner` /
`2026-11-06`. The effective decisions live in this ADR (ADR-0004); the
draft's option rows remain historical alternatives and must not be
implemented from. ADR-0001, ADR-0002 and ADR-0003 remain in force; this ADR
only adjudicates G2, G3, G5, contracts §8, G6, G7 and G4. G1 is adjudicated
by ADR-0003, not here. G8/G9 are unchanged, and G10/G11 remain open
environment/scope gaps unaffected by this sign-off.
