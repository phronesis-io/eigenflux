# S3 asset procurement package — four personas, 2.5D stage, director

- **Date**: 2026-11-07
- **Status**: ready to quote; **blocked on owner purchase** (ADR-0004 G6)
- **S3 exit criterion this package serves**: 60fps Flame scene; Rive
  idle/walk/expression for four personas; retained screenshot evidence under
  `production/qa/evidence/`
- **Consumes**: `design/game-brief.md` (Art & audio direction, Four MVP personas,
  Scene director, Client page structure, MVP), `fluxspace_client/` (Flame 1.38.2,
  Rive 0.14.11)

> **Art direction anchor**: `design/game-brief.md`, line *Art & audio direction* —
> "Soft Flux glass, low-saturation violet/cyan gradients, distinctive stylized
> portraits, 2.5D miniature stages, readable monospace decision summaries,
> gentle event cues, and reduced-motion support."
>
> There is **no art bible** (`design/art/art-bible.md` does not exist). Every
> dimension, format, budget and palette below is therefore a **proposal**, not a
> constraint handed down by an art bible. If the visual identity is meant to be
> defended rather than negotiated with a vendor, write the art bible first.
>
> `art-director` and `technical-artist` were **not consulted** for this document
> (solo derivation from the brief). Numbers here are engineering proposals to be
> checked against real delivered files, not reviewed art direction.

## Why this document exists

S3 is the only slice with a hard external dependency. Everything else — the Hub,
the archive, the governance controls — is delivered or blocked on a decision, but
S3 cannot start without drawings. That makes the procurement decision the
critical path, and an under-specified purchase the most expensive possible
outcome: four characters that do not read as four people have to be bought twice.

The purpose here is to shrink the owner's remaining work to *choose a tier,
choose a vendor, approve a quote*. Nothing below commits spend.

## 1. What is being bought

Three lots. A vendor may quote any subset, but Lot A is not optional — without it
S3's exit criterion cannot be met at all.

| Lot | Contents | Why it is a separate lot |
|---|---|---|
| **A — Four persona rigs** | One Rive file per persona: idle, walk, expression states, driven by a named state machine | This is the S3 exit criterion. It also carries the most art risk: the personas must be *recognisably different*, not four skins on one rig. |
| **B — 2.5D stage kit** | Parallax stage layers, four seat anchors, event props, four light grades, one `stage.json` geometry contract | Can be bought from a different vendor than the characters, and can ship a version behind them. |
| **C — UI and portrait support** | Portraits at three sizes, persona card frame, unmistakable "Official" badge, monospace decision-summary type spec, replay chrome | Cheapest lot, easiest to defer, but the badge is a trust requirement (GDD forbids disguising official companions as real users). |

### The four personas (from the GDD, not invented here)

| Persona | Occupation | Line | Behaviour the animation must make visible |
|---|---|---|---|
| **Lin Shen** (林深) | Rare-book restorer | "Every page has something to say" | Verifies before acting. Small, controlled motion; looks down to check; slow blink; hand to chin. Low idle amplitude. |
| **Su Wan** (苏晚) | Field reporter | "Get to the scene first, work it out after" | Impulsive. Fast lean-in, wide gestures, quick head turns, large emotional range. High idle amplitude. |
| **Gu Yuan** (顾远) | Community coordinator | "Nobody should face a door alone" | Coordinates. Turns *toward* others, open palms, points to divide labour, nods along. Medium amplitude, outward-facing. |
| **Shen Mo** (沈默) | Independent chess player | "The hardest part is mine" | Competitive and closed. Faces forward, not others; minimal motion; jaw set; takes the hard task alone. Very low idle amplitude, stillness as character. |

The GDD's "spectator-perceivable differences" line is the test these rigs must
pass: Lin Shen and Su Wan must react *visibly* differently to the same clue; Gu
Yuan's coordinating and Shen Mo's solitary grab must be readable without a
caption.

## 2. Technical contract (hard requirements)

The client already declares `flame 1.38.2`, `rive 0.14.11` and `assets/personas/`
in `fluxspace_client/pubspec.yaml`. Delivered files must load under exactly those
versions — a newer Rive runtime export will not open.

### 2.1 Files, paths, naming

```
assets/characters/<persona>/<persona>.riv     e.g. assets/characters/lin_shen/lin_shen.riv
assets/characters/<persona>/source/           tier B/C: .rev / .fig / .psd source
assets/portraits/<persona>_<size>.png         512 / 256 / 96 px, alpha
assets/stage/<theme>/                         layer PNGs + stage.json
assets/ui/                                    badge, frames, 9-slice chrome
```

- Persona ids are **kebab-case and fixed**: `lin-shen`, `su-wan`, `gu-yuan`,
  `shen-mo` (they already key the persona YAMLs).
- File and layer names: `snake_case`, ASCII only, no spaces.
- `pubspec.yaml` must gain `assets/characters/`, `assets/portraits/`,
  `assets/stage/`, `assets/ui/` — the client change is part of the delivery
  checklist, not the vendor's, but a path mismatch silently ships an app with no
  art.

### 2.2 Rive artboard and state machine contract

One `.riv` per persona, one artboard named `character`, one state machine named
`main`, with **exactly** these inputs. A missing input is a rejected delivery,
because the client drives animation from Hub events and cannot invent a mapping.

| Input | Type | Values / range | Driven by |
|---|---|---|---|
| `pose` | enum | `idle`, `walk`, `gesture_open`, `gesture_guard` | Round phase, seat movement |
| `expression` | enum | `neutral`, `warm`, `wary`, `sharp` | Decision outcome, relationship state |
| `speaking` | bool | — | Discussion turn ownership |
| `mood` | number | 0.00–1.00 | Relationship value + recent outcome |
| `reduced_motion` | bool | — | Client accessibility setting |

`reduced_motion = true` must reach a **static, intentional** pose per state — not
a slower loop and not a frozen mid-stride. Amber-and-grey, no ambient drift.

### 2.3 Performance envelope (proposed, to be confirmed against delivered files)

| Constraint | Proposal | Reason |
|---|---|---|
| Artboard size | 512 × 512 per character, feet on the bottom edge | Four characters on a 1920 × 1080 stage at readable scale |
| Bone count | ≤ 40 per persona | Rive skinning cost per frame; four artboards are live at once |
| Deform meshes | ≤ 3 per persona, ≤ 8 × 8 vertices | Same |
| Rive effects | none (no blur/glow/shadow in Rive) | Do it in Flame so the cost is measurable and switchable |
| `.riv` size | ≤ 1.5 MB per persona, ≤ 6 MB total | Windows desktop download and cold-load time |
| Stage kit | ≤ 8 MB; ≤ 4 parallax layers per theme | 2.5D depth without a scene graph |
| Frame budget | p95 ≤ 16.7 ms, no frame > 33 ms over a 5-minute replay | 60fps is the exit criterion; a single 33 ms frame is a visible hitch |
| Reference machine | Windows desktop, integrated GPU, 1920 × 1080 windowed | The MVP target is Windows desktop only (decision 5) |

### 2.4 `stage.json` geometry contract (Lot B)

The 2.5D director is client code; the vendor supplies geometry, not behaviour.
Four camera events from the GDD map onto named anchors:

```json
{
  "theme": "glass_violet",
  "canvas": { "width": 1920, "height": 1080 },
  "layers": [ { "id": "backdrop", "file": "backdrop.png", "depth": 0.10, "pivot": [960, 540] } ],
  "seats":  [ { "id": "seat_1", "x": 520, "y": 760, "scale": 1.0 } ],
  "props":  [ { "id": "branch_marker", "file": "props/branch_marker.png", "anchor": [960, 620] } ],
  "lights": [ { "id": "neutral", "tint": "#ECEAF4", "intensity": 1.0 } ]
}
```

`depth` 0.0 is infinitely far, 1.0 is the seat plane; the client multiplies camera
translation by `1 - depth` for parallax. Four light grades are required
(`neutral`, `warm`, `cold`, `alert`) because the GDD's "emotional moment" camera
event ends in an ambient light shift.

## 3. Three purchase tiers

Effort figures are **planning ranges for comparing quotes, not prices**. They are
in artist-days and exclude project management, revisions beyond two rounds, and
localisation.

| Tier | What you buy | Effort (artist-days) | Passes S3? | Main risk |
|---|---|---|---|---|
| **A — License and adapt** | Existing Rive character packs, recoloured, minimal rig edits, portraits retouched | 5–8 | **No.** Four distinct silhouettes with persona-specific body language are the criterion, and a licensed pack gives you four skins. | Buys a second purchase later. Cheap only if you accept failing acceptance. |
| **B — Custom portraits, licensed rigs** | Four bespoke portraits and expression sets; animation reused from rig templates | 12–18 | **Partly.** Portraits carry recognition; idle/walk will look generic and Su Wan vs Shen Mo will not differ in motion. | The acceptance test is behavioural, not illustrative. Half of it will fail. |
| **C — Full custom** | Four vector characters with one shared rig template, five inputs, expression sets, plus Lots B and C | 30–45 | **Yes**, if the acceptance tests pass | Highest absolute cost; needs the two-wave split below to avoid a single big-bang delivery. |

### Recommendation

Buy **Tier C, delivered in two waves**:

1. **Wave 1** — Lin Shen + Su Wan (the GDD's own contrast pair) + the stage kit.
   This proves the pipeline: rig template, input contract, 60fps budget, evidence
   capture. Roughly half of Tier C's effort.
2. **Wave 2** — Gu Yuan + Shen Mo, using the approved template.

Rationale: the expensive failure mode is four characters that turn out to need a
re-rig after review. Wave 1 costs about half as much to discover that, and it
also unblocks a playable two-character theatre earlier. The client can ship Wave
1 with the remaining two seats using a clearly labelled placeholder artboard —
labelled, not disguised.

If the budget only allows one tier: **Tier B is the wrong compromise** — it fails
the half of the acceptance test that matters most (motion differentiation) while
still costing two thirds of Tier C. Either do A deliberately as a temporary
stand-in, or do C.

## 4. Request for quote — copy this to vendors

> **Subject: Four-character Rive set + 2.5D stage kit — quote request**
>
> We are building a desktop spectator client for an autonomous-character
> deduction theatre (Flutter + Flame + Rive). We need four stylised characters
> with distinct body language, plus a miniature 2.5D stage kit.
>
> **Art direction**: soft glass, low-saturation violet/cyan gradients, stylised
> rather than realistic, readable at 512 px, no baked-in text.
>
> **Lot A (mandatory)**: one `.riv` per character; artboard `character`; state
> machine `main`; inputs `pose` (idle/walk/gesture_open/gesture_guard),
> `expression` (neutral/warm/wary/sharp), `speaking` (bool), `mood` (0–1),
> `reduced_motion` (bool). Must open under Rive runtime 0.14.11. ≤ 40 bones, ≤ 1.5
> MB each.
>
> **Lot B**: 1920×1080 stage, ≤ 4 parallax layers, four seat anchors, one event
> prop, four light grades, plus `stage.json` describing layer depth, pivots and
> anchors.
>
> **Lot C**: portraits at 512/256/96 px, persona card frame, an unmistakable
> "Official companion" badge legible in greyscale, monospace decision-summary
> type specimen.
>
> **Please reply with**:
> 1. A quote per lot, with the effort split between wave 1 (two characters +
>    stage) and wave 2 (two characters).
> 2. Two interactive Rive samples you have shipped, or a short screen recording
>    of each state machine being driven by inputs.
> 3. Your delivery format: `.riv` plus source (`.rev`/`.fig`/`.psd`)?
> 4. Licence terms: perpetual, commercial, derivative works, no attribution
>    required, no per-seat royalty. State any restriction explicitly.
> 5. Revision rounds included, and the review gates you propose.
> 6. Whether you can meet the frame budget on an integrated-GPU Windows desktop,
>    and how you would verify it.
>
> **Out of scope**: UI screens, icons, marketing art, 3D meshes, audio.

## 5. Acceptance criteria (testable, not adjectives)

A delivery is accepted when all of these pass. Each maps to a file or a
measurement; "looks good" is not a criterion.

| # | Criterion | How it is checked |
|---|---|---|
| 1 | All four `.riv` files load under Rive 0.14.11 | A client test loads each file and asserts the `main` state machine exists |
| 2 | Every required input exists with the exact name and type | Same test, asserting the five inputs; a missing one fails the build |
| 3 | Each input produces a visible change | Manual: drive each enum value, capture one frame per value |
| 4 | `reduced_motion = true` is static and intentional | Manual: no ambient loop, no drift, per state |
| 5 | 60fps with four characters + stage + overlay | Flame `FrameTiming` over a 5-minute replay: p95 ≤ 16.7 ms, max ≤ 33 ms |
| 6 | Personas are distinguishable in silhouette | Five viewers, greyscale, 25% scale, no captions: ≥ 4/5 identify Lin Shen vs Su Wan correctly |
| 7 | The "Official" badge is unmistakable | Legible at 100% and 125% scaling, and in greyscale |
| 8 | No text baked into artwork; no state encoded by colour alone | Visual review against the asset list |
| 9 | Written licence covers perpetual commercial use and derivative works | Contract review, not a judgement call |
| 10 | Evidence retained | `production/qa/evidence/s3/` holds one still per state per persona plus a 30-second capture; a manifest maps file → asset → reviewer → date |

Criterion 6 is the one that decides whether the purchase was worth making: it is
the GDD's own "recognition" requirement reduced to a test five people can run in
ten minutes.

## 6. Decisions this document leaves to the owner

| # | Decision | Note |
|---|---|---|
| 1 | Tier A / B / C | Recommendation: C in two waves. B is the trap. |
| 2 | Vendor | Not chosen here; the RFP above is vendor-neutral. |
| 3 | Budget | No price is asserted in this document. Quotes decide. |
| 4 | Whether to write the art bible first | If the visual identity has to survive a vendor negotiation, yes. This package can also be treated as the art bible's first draft. |
| 5 | Whether AI-assisted artwork is acceptable | Affects vendor selection and licence terms; must be declared in the contract either way. |

## 7. What this document does not do

- Does not commit spend, choose a vendor, or set a date.
- Does not define the *behaviour* of the director. Camera moves are client code;
  this buys the geometry and the art they move over.
- Does not replace an art bible, and does not constrain later visual work: the
  numbers in §2 are proposals that the first delivered files will correct.
- Does not cover audio. The GDD's "gentle event cues" need a separate, much
  smaller purchase or a licensed library; no audio asset is required by S3's exit
  criterion.
- Does not include the client integration (loading the `.riv` files into a Flame
  component, wiring Hub events to inputs, capture harness). That is S3
  implementation work and starts as soon as Wave 1 exists.
