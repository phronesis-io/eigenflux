# ADR-0012 — S3 art via AI generation + pure-Flutter interim (Rive requirement relaxed)

- **Status:** Accepted
- **Date:** 2026-11-07
- **Relates to:** ADR-0004 G6 (procure Rive/2.5D externally), G7 (labelled demo
  as MVP interim), ADR-0003 (relaxed GDD targets),
  `fluxspace_client/lib/theatre/`

## Context

S3's documented path (ADR-0004 G6) is to procure Rive rigs plus a 2.5D stage kit
from an external vendor. The owner has chosen an alternative: develop the art with
AI image generation and render it through a pure-Flutter `TheatreAssets` pack,
with no Rive runtime at all.

The constraint that forces a decision:

- **AI image generation is strong at static art** — stylised persona portraits
  and 2.5D stage layers — and these drop straight into the existing
  `TheatreAssets` contract. The render layer already proves it can compose and
  animate without Rive: `PlaceholderAssets` is pure Flutter, and no widget in
  `lib/theatre/` imports Rive or Flame.
- **AI cannot reliably emit Rive `.riv` rigs** with the five named inputs
  (`pose`, `expression`, `speaking`, `mood`, `reduced_motion`) driven by Hub
  events at 60fps. That is hand-authored rigging work, not a promptable output.
  So the original S3 exit criterion — "Rive idle/walk/expression for four
  personas" — cannot be met by AI alone today.

This is the same shape as two precedents already accepted: G7 accepted a labelled
demo clip as an MVP interim, and ADR-0003 relaxed GDD win-rate targets because the
authoritative baseline does not measure them. Here we relax the *medium* (Rive rig
→ AI art + Flutter render) while keeping S3's formal exit criterion open and
documented.

## Decision

1. **Drop the Rive runtime dependency** from the theatre render layer. Animation
   is driven by Flutter (image swaps + tweens) against the existing
   `TheatreAssets` contract. The `rive 0.14.11` version lock and the "must load
   under 0.14.11" constraint are retired for S3.
2. **Generate the art with AI image generation**, in the `design/game-brief.md`
   art direction (soft Flux glass, low-saturation violet/cyan, stylised, readable
   at 512px, no baked text):
   - Four persona portraits — Lin Shen (林深), Su Wan (苏晚), Gu Yuan (顾远), Shen Mo
     (沈默) — each posed to read its GDD behaviour (Lin Shen controlled/downcast, Su
     Wan impulsive/leaning, Gu Yuan outward/open, Shen Mo still/facing forward).
   - One 2.5D stage backdrop.
3. **This is a labelled interim, not S3 acceptance.** When the AI pack is active
   the UI must show a visible "AI-art interim — not S3 final" marker, exactly as
   G7 required the demo to be labelled, not disguised. It does NOT satisfy S3's
   original exit criterion.
4. **The full Rive/2.5D procurement path (ADR-0004 G6) stays the documented route
   to S3's formal acceptance** and is deferred, not cancelled. The interim can
   ship behind a flag while procurement is decided.

## Consequences

- Removes the vendor/licensing blocker and the Rive version constraint; the client
  render layer is simpler and easier to keep at 60fps (four static images + tweens
  versus four live bone rigs).
- Animation fidelity is image-swap/transform based, not skeletal deformation.
  Persona body-language differentiation is carried by the *portrait posing* and by
  Flutter-driven transforms, not by Rive state machines. Criterion #6 of the
  procurement checklist (silhouette distinguishability) still applies and is the
  key bar; criteria #1–#5 (Rive load / inputs / frame-budget specifics) are not
  applicable to the interim.
- `PlaceholderAssets` remains the default; `AiArtAssets` is selected by an explicit
  interim flag so nothing is silently swapped.
- NOT a substitute for S3's formal acceptance. The frozen `fluxspace/` prototype
  and the GDD's Rive requirement are untouched; this ADR only changes how the
  Flutter client renders its interim theatre.
