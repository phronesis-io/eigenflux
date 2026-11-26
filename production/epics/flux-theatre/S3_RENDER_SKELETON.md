# S3 — Render-layer skeleton (placeholder art, asset-swap ready)

- **Date**: 2026-11-07
- **Status**: render skeleton complete; **S3 as a whole is still blocked on G6
  asset procurement** (ADR-0004, `S3_ASSET_PROCUREMENT.md`).
- **Scope**: `fluxspace_client/lib/theatre/**` and
  `fluxspace_client/test/theatre/**` only. No existing file was modified, no
  dependency was added, `pubspec.yaml` / `main.dart` / `routes.dart` /
  `pages/theatre_page.dart` are untouched. This slice renders with **pure
  Flutter built-ins** — `rive` and `flame` are declared in `pubspec.yaml` but
  are deliberately NOT used here, and `flutter pub get` was never run.

## Why this exists

S3's exit criterion is "60fps Flame scene; Rive idle/walk/expression for four
personas; retained screenshot evidence". Those `.riv` files, the 2.5D stage kit
and the portraits have not been procured (G6). Rather than leave the whole
render layer unwritten until the art arrives, this slice builds the **render
layer skeleton** so that integration becomes a one-file swap, not a rewrite:

- the data vocabulary (`TheatreFrame` and friends),
- the full composition (stage, seats, spotlight, reveal, replay scrubber,
  composed screen),
- the layout and animation behaviour, and
- the widget tests that pin that behaviour

are all written now, against a pure-Flutter `PlaceholderAssets` pack. When the
procured assets land, only the art implementation is replaced; the frames, the
widgets, the layout, the controller and the tests do not change.

> **Placeholders are NOT a substitute for the procured Rive/2.5D assets.** The
> shapes and gradients in `placeholder_assets.dart` are a *layout and
> behaviour harness*. They carry no persona body language, no idle/walk/
> expression rigs, no 2.5D parallax, no `stage.json` geometry, no 60fps Flame
> scene and no "Official" badge artwork. They cannot satisfy S3's exit
> criterion, they are not evidence for `production/qa/evidence/s3/`, and they
> are not a ship-able visual identity. They exist so the skeleton is runnable
> and testable today; the real art still has to be bought and integrated.

## What is implemented

| File | Role |
|---|---|
| `lib/theatre/theatre_models.dart` | Plain, immutable render models: `TheatreFrame`, `TheatreSeat`, `TheatreVote`, `TheatreReveal`, `TheatreSeatState`. No Hub / `dio` / `dart:convert` dependency. |
| `lib/theatre/placeholder_assets.dart` | Abstract `TheatreAssets` contract + `PlaceholderAssets` (gradients / shapes / `CustomPainter`, no image assets). |
| `lib/theatre/stage_view.dart` | Stage composition: backdrop, ambient, round/phase indicator, active-speaker spotlight, N seats. `LayoutBuilder` + `Wrap`, never an unbounded `Row`. |
| `lib/theatre/seat_card.dart` | Persona card: portrait, name, state chip (`thinking` / `speaking` / `voted` / `eliminated`), optional vote badge, "Official" badge. |
| `lib/theatre/vote_spotlight.dart` | Animated voting reveal: one `AnimationController` (`TickerProviderStateMixin`, disposed properly), winner emphasis, tally chips. |
| `lib/theatre/postmatch_replay.dart` | Round-by-round scrubber over `List<TheatreFrame>`: play/pause, step, slider, divergence-point label. |
| `lib/theatre/theatre_screen.dart` | Drop-in composed widget. The single widget the lead adds to the app. |
| `test/theatre/theatre_screen_test.dart` | Widget tests: smoke, seat count, spotlight follows `activeSpeakerId`, scrubber changes frame, placeholder assets never throw, zero RenderFlex overflow at 800x600 and 1280x720, plus a meta-test proving the overflow harness detects a real overflow. |
| `test/theatre/theatre_models_test.dart` | Unit tests for the tolerant mapping and the "never invent missing fields" rule. |

### Data model and the "never invent" rule

The render layer speaks its own vocabulary, deliberately **not** the Hub wire
models. `TheatreFrame` is one renderable snapshot (round, phase, seats, active
speaker, votes, reveal, summary, view mode, light grade). Optional scalars are
**nullable**; missing fields stay `null` and render as "not recorded". Optional
collections stay empty. Nothing is defaulted to a plausible value — in
particular there is no invented winner, round number, persona name or state.

`TheatreSeat.isOfficial` is a tri-state `bool?`: `null` = not recorded,
`true` = show the "Official" badge, `false` = known real user companion. It is
never collapsed to `false`, because the GDD forbids disguising official
companions as real users. `TheatreSeatState` has exactly the four chip values;
there is no `unknown`/`idle` member that would fabricate a fact.

The documented mapping entry point is `TheatreFrame.fromMap(Map<String,
dynamic>)` (plus `theatreFramesFromMaps(Object?)` for a whole timeline). It is
tolerant of wrong types (numeric strings are coerced for ints; anything else
becomes `null`) but never supplies defaults. Recognised keys:

```
round, total_rounds, phase, title, summary, active_speaker_id,
view_mode, light_grade,
seats[]  : { id, display_name, persona_id, state, official }
votes[]  : { voter_id, target_id, label }
reveal   : { winner_id, note, tally: { <seat_id>: int } } | null
```

`state` accepts the four `TheatreSeatState` labels; any other value becomes
`null`. The integrator owns the Hub-view -> map adapter (see below), so a Hub
field rename is one adapter change, not a widget rewrite.

## The exact asset-swap contract

`TheatreAssets` (in `placeholder_assets.dart`) is the entire seam. A real
Rive/2.5D pack is **one class** implementing all five methods:

| Method | Contract |
|---|---|
| `Widget portrait(String? personaId, double size)` | Persona portrait. `personaId` may be `null` ("not recorded") or an unrecognised id; `size` may be zero. Must fill the box and never throw — a missing/unknown persona renders a clearly labelled neutral placeholder, never another persona's art. |
| `Widget backdrop()` | Full-bleed stage background. Must fill any box, including a zero-sized one during layout. |
| `Widget spotlight(double intensity, bool reducedMotion)` | Active-speaker light. `intensity` 0..1. `reducedMotion == true` must yield a **static, intentional pose** (no ambient loop, no drift). |
| `Widget voteFlourish(double progress)` | Decorative reveal art. `progress` 0..1 from the reveal `AnimationController`. The pack must **not** own its own animation loop — timing lives in `vote_spotlight.dart`. |
| `Widget ambient(String? lightGrade)` | Mood / light-grade overlay. `lightGrade` is one of `neutral` / `warm` / `cold` / `alert` (the four `stage.json` grades) or `null` ("not recorded" = neutral). |

Every method must return a `Widget` that fills the box it is given without
throwing, for any argument the render layer can pass. A pack that throws on a
null persona id or a zero-size layout pass is a rejected delivery.

**The swap itself is one line at the call site:** pass the real pack as the
`assets` argument of `TheatreScreen`:

```dart
TheatreScreen(
  frames: recordedFrames,
  assets: const RiveTheatreAssets(/* load the procured .riv files */),
);
```

`TheatreScreen`'s `assets` parameter defaults to `const PlaceholderAssets()`.
That default — and the concrete `PlaceholderAssets` class — is the only thing
that changes. Everything that stays unchanged when assets arrive:

- `theatre_models.dart` (frame vocabulary + mapping contract),
- `stage_view.dart`, `seat_card.dart`, `vote_spotlight.dart`,
- `postmatch_replay.dart`, `theatre_screen.dart`,
- the layout (`LayoutBuilder` / `Wrap` / `SingleChildScrollView`), the
  spotlight keying off `activeSpeakerId`, the scrubber, the animation timing,
  and **all tests** in `test/theatre/`.

No widget in `lib/theatre/` imports a Rive, Flame, Hub, `dio` or concrete asset
type. The procured pack can therefore be developed and reviewed in isolation.

## How the integrator wires `TheatreScreen`

`TheatreScreen` constructor (simple by design):

```dart
const TheatreScreen({
  Key? key,
  List<TheatreFrame>? frames,          // replay / archive: scrubber enabled
  ValueListenable<TheatreFrame>? frame,// live: follows the latest value
  TheatreAssets assets = const PlaceholderAssets(),
  int? divergenceRound,                // optional same-spec reprise marker
  String? title,                       // optional heading
});
```

Supply exactly one of `frames` / `frame`:

- **Recorded match / S4 replay archive**: build a `List<TheatreFrame>` (one per
  round or per key moment) and pass `frames:`. The round-by-round scrubber is
  shown; the selected frame drives the stage, seats, spotlight and reveal.
  `divergenceRound` labels the round where a same-spec reprise first diverged.
- **Live match**: hold a `ValueNotifier<TheatreFrame>` fed from the omniscient
  spectator stream / `SessionController` and pass `frame:`. The screen rebuilds
  on each notification; no scrubber is shown. If both are supplied, `frame`
  takes precedence.

`TheatreScreen` is a drop-in body: add it as the theatre route's content (or a
fullscreen overlay page) inside the existing `MaterialApp`. It provides its own
`Material` + `SafeArea` + `SingleChildScrollView`, so it needs no `Scaffold`.

### Building frames from Hub views (owned by the integrator / lead)

The render layer deliberately does **not** import `SessionView`, `HubEvent` or
the archive types. The lead writes a small adapter that projects a Hub view (or
an S4 archive view) into the documented map and calls `TheatreFrame.fromMap`.
A sketch — the field names on the left are the Hub/archive view, on the right
the `fromMap` keys:

```dart
// Conceptual adapter; lives outside lib/theatre/ (integration is the lead's).
Map<String, dynamic> toRenderMap(SessionView view, String? speakerId) => <String, dynamic>{
  'round': view.round,
  'total_rounds': view.totalRounds,
  'phase': view.phase,
  'title': view.skeleton?.title,
  'active_speaker_id': speakerId,          // from the discussion/turn event
  'view_mode': 'omniscient',
  'seats': <Map<String, dynamic>>[
    for (final HubSeat s in view.seats)
      <String, dynamic>{
        'id': s.seatId,
        'display_name': s.displayName,
        'persona_id': null,                // record "not recorded", do not guess
        'official': s.official,
        // 'state' left absent when the Hub has not recorded one
      },
  ],
  // 'votes' / 'reveal' / 'summary' only when the corresponding event arrived.
};

final TheatreFrame frame = TheatreFrame.fromMap(toRenderMap(view, speakerId));
```

The adapter is where any defaulting decision belongs, and it must be explicit
there — the render layer will not invent it. For an archive timeline, use
`theatreFramesFromMaps(decodedJsonList)`.

## What S3 still needs (blocked on procurement)

All of the following are **not** delivered by this slice and remain blocked on
G6 / the quote package in `S3_ASSET_PROCUREMENT.md`:

1. **Lot A — four persona Rive rigs**: one `.riv` per persona, artboard
   `character`, state machine `main`, inputs `pose` / `expression` / `speaking`
   / `mood` / `reduced_motion`; recognisably different silhouettes and body
   language (Lin Shen / Su Wan / Gu Yuan / Shen Mo); load under Rive 0.14.11.
2. **Lot B — 2.5D stage kit**: parallax layers, four seat anchors, event
   props, four light grades, `stage.json` geometry; a Flame scene running the
   four GDD camera events at **60fps** (p95 <= 16.7 ms, no frame > 33 ms).
3. **Lot C — UI/portrait support**: portraits at 512/256/96 px, persona card
   frame, an unmistakable greyscale-legible "Official" badge, monospace
   decision-summary type specimen.
4. **Integration work**: load the `.riv` files into Flame components, wire Hub
   SSE events to the five Rive inputs, implement the event director, honour
   reduced motion, and add the `assets/characters/`, `assets/portraits/`,
   `assets/stage/`, `assets/ui/` declarations to `pubspec.yaml` (a lead-side
   change; `pubspec.yaml` is outside this slice's whitelist).
5. **Acceptance & evidence**: the ten testable criteria in
   `S3_ASSET_PROCUREMENT.md` §5, including a client test that loads each `.riv`
   and asserts the `main` state machine + five inputs, a 5-minute
   `FrameTiming` capture, the five-viewer greyscale recognition check, licence
   review, and retained screenshots/a 30-second capture plus a manifest under
   `production/qa/evidence/s3/`.
6. **Art bible** (optional owner decision): §2 numbers in the procurement doc
   are engineering proposals, not reviewed art direction.

Until 1–5 exist, S3's exit criterion ("60fps Flame scene; Rive idle/walk/
expression for four personas; retained screenshot evidence") is **not met**.
This document must not be read as claiming otherwise.

## Verification (this slice)

```
FLUTTER=/c/Users/louvr/.workbuddy/toolchains/flutter/bin/flutter
cd fluxspace_client
env -u http_proxy -u https_proxy -u HTTP_PROXY -u HTTPS_PROXY \
    NO_PROXY=localhost,127.0.0.1 "$FLUTTER" test --no-pub test/theatre
# -> 12 tests, all pass (incl. no-overflow at 800x600 / 1280x720 via the
#    current tester.view.physicalSize / devicePixelRatio API, plus a
#    meta-test proving the overflow harness detects a real RenderFlex overflow)

env -u http_proxy -u https_proxy -u HTTP_PROXY -u HTTPS_PROXY \
    NO_PROXY=localhost,127.0.0.1 "$FLUTTER" analyze --no-pub lib/theatre test/theatre
# -> No issues found!
```

Tests set the surface size with the current Flutter 3.47 test API
(`tester.view.physicalSize`, `tester.view.devicePixelRatio`, and their
`reset*` teardowns); no removed/deprecated `window.physicalSizeTestValue` API
is used. `flutter pub get` was never run and no dependency was added.

A full-project `flutter analyze` additionally reports four errors in
`test/panels/panels_models_test.dart` (`PanelsApi` undefined / `AuditEntry`
type mismatches) — that file belongs to parallel work in `lib/panels/**` /
`test/panels/**`, is outside this slice's whitelist, and was not touched here.
