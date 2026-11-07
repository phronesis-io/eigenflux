# Flux Theatre — S4 Archive Replay Wiring

**Status:** self-contained client slice, ready for the lead to integrate.
**Scope of changes:** `fluxspace_client/lib/panels/**`, `fluxspace_client/test/panels/**`
and this English document. `fluxspace/` (backend) and `lib/theatre/**` are
FROZEN / untouched — the replay reuses the existing `TheatreScreen` as-is.
No new dependency was added; `flutter pub get` was never run. All documentation
is in English.

## What this slice does

The S4 archive backend already exposes six read-only view projections over an
immutable event stream (`GET /hub/archives/{id}/views/{mode}`). Before this
slice, `TheatreScreen` could play a live single-frame snapshot (`frame:
ValueListenable`) but had no way to play a *recorded* match. This slice wires a
real S4 projection into `TheatreScreen(frames: ...)`: the user picks one of the
six modes, taps **Play replay**, and the existing round-by-round scrubber plays
back the recorded timeline instead of the live snapshot.

Data flow:

```
GET /hub/archives/{id}/views/{mode}   (read-only; the only endpoint added)
        |  raw `View` JSON (full wire document)
        v
adaptArchiveView(...)                 lib/panels/archive_frames.dart
        |  List<TheatreFrame> + divergenceRound + archiveId
        v
TheatreScreen(frames:, divergenceRound:, title:)   lib/theatre/ (unchanged)
```

`lib/theatre/` speaks only `TheatreFrame` (`theatre_models.dart`); the adapter is
the single boundary between the Hub wire vocabulary and the render vocabulary.
The adapter has **no dependency on `lib/models/hub_models.dart`**, `dio`, or
`dart:convert` — it takes a plain decoded `Map` and returns plain render models.

### Public signature of the adapter

```dart
// lib/panels/archive_frames.dart
ArchiveReplay adaptArchiveView(Object? viewJson);
List<TheatreFrame> archiveViewToFrames(Object? viewJson); // convenience
const List<String> kArchiveViewModes = ArchiveModes.all;   // the six modes

class ArchiveReplay {
  final List<TheatreFrame> frames;   // -> TheatreScreen.frames
  final String archiveId;            // -> TheatreScreen.title
  final String? viewMode;            // View.mode (null = not recorded)
  final int? divergenceRound;        // -> TheatreScreen.divergenceRound
  bool get hasFrames;
}
```

`Object?` is deliberate: a null, non-map, or empty payload yields
`ArchiveReplay.empty` (an honest "not recorded" state), never a partial or
fabricated timeline. All scalar/collection decoding is delegated to
`TheatreFrame.fromMap`, whose tolerant decoders turn a missing or wrong-typed
value into `null` / an empty collection.

### Why a second GET method was added

`PanelsApi.fetchView` already performs the GET, but returns the intentionally
**lossy** `ArchiveView` typed model used by the observability cards: it keeps
only `events[].seq/type/round/visibility/offset_ms`, a decision *count*, and a
few scalars, and **drops** `events[].phase`, `seat_id`, `payload`, decision
`seat_id`/`official`, `moments[].reason`, etc. Those are exactly the genuinely
recorded fields a faithful replay needs. `PanelsApi.fetchViewJson(id, mode,
{query})` therefore returns the raw wire document via the same `viewPath` and
read-only `_fetch` (a `dio.get`). It is **GET only**. `PanelsApi` has no write
methods at all: no consent grant, no reprise creation, no publish, no authorize,
no budget usage can be triggered from this slice. The full replay flow requests
only the five documented GET read paths (proven in `test/panels/`).

## The six viewing modes

All six are projections over the SAME immutable event stream
(`hub/archive/views.go: Modes()`); a `View` carries exactly one projection
collection, the unused fields being omitted by Go's `omitempty`. The adapter
maps whichever collections are genuinely present, in the order
`events -> rounds -> moments -> comparison`, so an unknown or missing `mode`
still maps real data and never fabricates any.

| # | mode | Hub projection collection | Replay frames built from |
| --- | --- | --- | --- |
| 1 | `full` | `events[]` (`TimelineEvent`) | one frame per event |
| 2 | `accelerated` | `events[]` | one frame per event (query `speed=2/4`) |
| 3 | `single_character` | `events[]` | one frame per event (query `seat_id`) |
| 4 | `omniscient_comparison` | `rounds[]` (`RoundComparison`) | one frame per round |
| 5 | `key_moments` | `moments[]` (`Moment`) | one frame per moment |
| 6 | `same_spec_comparison` | `comparison` (`RepriseComparison`) | one frame per `comparison.rounds[]` |

Query parameters the panel already passes through (`archive_panel.dart`):
`speed=2` for `accelerated`, `seat_id` (from the archive config seats) for
`single_character`, `compare_with=<second archive id>` for
`same_spec_comparison`. The **Play replay** button is gated on a selected
archive AND a non-empty adapted frame list; otherwise an explicit
`not recorded` empty state is shown. Layout uses `Wrap` (no unbounded `Row`).

## Exact mapping table — Hub JSON field -> TheatreFrame field

Every Hub name below is transcribed from the Go `json:"..."` tags in
`hub/archive/views.go`, `hub/archive/reprise.go`, `hub/api/archives.go` and
`hub/models/session.go`. Nothing is guessed. `TheatreFrame.fromMap` keys are
the documented ones in `lib/theatre/theatre_models.dart`.

### Mapped (genuinely recorded -> render field)

| Hub JSON field (Go tag) | Struct | `fromMap` key | `TheatreFrame` field | Notes |
| --- | --- | --- | --- | --- |
| `mode` | `View` | `view_mode` | `TheatreFrame.viewMode` | on every frame; empty => `null` |
| `events[].round` | `TimelineEvent` | `round` | `TheatreFrame.round` | timeline modes |
| `events[].phase` | `TimelineEvent` | `phase` | `TheatreFrame.phase` | `observe`/`discuss`/`act`/`resolve`; only the timeline projection carries a phase |
| `rounds[].round` | `RoundComparison` | `round` | `TheatreFrame.round` | omniscient mode |
| `rounds[].decisions[].seat_id` | `DecisionAtRound` | `seats[].id` | `TheatreSeat.id` | omniscient mode |
| `rounds[].decisions[].official` | `DecisionAtRound` | `seats[].official` | `TheatreSeat.isOfficial` | tri-state; wrong type => `null` |
| `moments[].round` | `Moment` | `round` | `TheatreFrame.round` | key_moments mode |
| `moments[].reason` | `Moment` | `summary` | `TheatreFrame.summary` | the one genuine human-readable recap string; used verbatim, no editorial text |
| `comparison.rounds[].round` | `RoundDiff` | `round` | `TheatreFrame.round` | same_spec mode |
| `comparison.rounds[].differences[].seat_id` | `SeatDiff` | `seats[].id` | `TheatreSeat.id` | same_spec mode |

Two genuinely recorded values are surfaced on the **screen**, not on a frame
(they are not `TheatreFrame` fields):

| Hub JSON field | Destination |
| --- | --- |
| `View.archive_id` | `TheatreScreen(title:)` — the archive id, via `ArchiveReplay.archiveId` |
| `comparison.divergence_point` | `TheatreScreen(divergenceRound:)`, when recorded and `> 0` |

`divergence_point == 0` is a real "never diverged" and is deliberately **not**
surfaced as a divergence marker; a missing/wrong-typed value stays "not
recorded". A divergence is never inferred from any other field.

### Consumed but NOT mapped (recorded, no TheatreFrame slot)

These fields are genuinely recorded by the Hub but have no corresponding
`TheatreFrame` field, so the adapter reads/does-not-fabricate them rather than
inventing a place for them. A future, fuller player would consume them.

- `View`: `session_id`, `speed`, `seat_id` (single-seat filter), `duration_ms`,
  `event_count`.
- `TimelineEvent`: `seq`, `type`, `seat_id`, `visibility`, `ts`, `offset_ms`,
  `payload` (opaque `json.RawMessage`).
- `RoundComparison` / `DecisionAtRound`: `agreement`, `synergy`, `delta`,
  `action_id`, `summaries`, `state_snapshot`.
- `Moment`: `event_id`, `kind`, `magnitude`, `ref`.
- `RepriseComparison`: `original_id`, `reprise_id`, `skeleton_hash`,
  `identical_config`, `divergence_seat`, `original_outcome`,
  `reprise_outcome`, `outcome_changed`; `SeatDiff.original_action` /
  `reprise_action`.

### "Not recorded" in every frame (never fabricated)

These `TheatreFrame` fields have **no source in any of the six views**. They stay
`null` / empty and the theatre widgets render the literal `not recorded` (header
`round not recorded` / `phase not recorded`, `StageView` "no seats recorded",
`VoteSpotlight` "no reveal recorded"). The adapter never sets them.

| Field | Why it is not recorded |
| --- | --- |
| `activeSpeakerId` | The Hub records `events[].seat_id` (the *actor* of an event), never "the current speaker". Electing one would fabricate a speaker. |
| `TheatreSeat.state` | No `thinking`/`speaking`/`voted`/`eliminated` state exists in any view. Never guessed. |
| `TheatreSeat.displayName` / `personaId` | Not present in any view (`display_name` / `persona_id` live in the archive `config` from a different endpoint). |
| `votes` / `TheatreVote` | The six views record no ballots at all — no `voter_id`, `target_id`, abspections or labels. |
| `reveal` / `winnerId` / `tally` / `note` | The six views record no reveal, no tally and no winner. A winner is never derived from an outcome, a decision or a metric delta. |
| `totalRounds` | `config.total_rounds` is genuinely recorded by `GET /hub/archives/{id}` but **not** in any view; this adapter is deliberately view-only. The scrubber therefore shows `Round N / ?`. |
| `title` (per-frame) | No match/skeleton title is carried by any view. The heading comes from `archive_id` at the screen level. |
| `lightGrade` | No ambient `neutral`/`warm`/`cold`/`alert` grade is recorded by any view; forwarded as `null` => neutral placeholder overlay. |

This follows the project invariant (ADR-0005, "do not invent metadata") and the
`TheatreFrame` contract: missing data is `null` / empty, never a plausible
default.

## What a full replay player still lacks

This slice proves the *wiring* — a recorded timeline now reaches the existing
scrubber. It is **not** a finished replay player:

1. **Real-time event pacing.** The Hub records `events[].ts`, `offset_ms` and
   `View.duration_ms`, but the adapter discards them (no `TheatreFrame` slot).
   `PostmatchReplay` advances frames with a fixed 900 ms `Timer`; playback is
   evenly indexed, not time-accurate. `accelerated` mode's `speed` is likewise
   not applied to playback.
2. **Audio.** No audio track is recorded or returned by the six views
   (`discussion`/`information_reveal` events carry text payloads only). There is
   no voice, ambience or soundtrack, and no audio synchronisation.
3. **Scrubbing by wall-clock.** The `Slider` scrubs by frame *index*
   (`0..frames.length-1`), not by `offset_ms`/`ts`. There is no seek-to-time,
   no timeline ruler, and no variable-duration frames.
4. **Interpretation of the opaque blobs.** `events[].payload`,
   `DecisionAtRound.state_snapshot`, metric `delta`s, `summaries` and persona
   snapshots are not decoded, so the stage shows no decision actions, no metric
   overlays, no portraits, no per-seat state and no vote/reveal choreography —
   only `round`, `phase`, seat ids, a `summary` and the divergence marker.
5. **Client-side integrity verification.** `GET /hub/archives/{id}` strips the
   frozen `events`/`settlement` bytes; no GET returns the full archive document,
   so a client cannot independently recompute `content_hash` / verify the
   stream. Verification remains a server-side `GET /hub/archives/{id}/verify`.
6. **A divergence timeline.** Only the scalar `divergence_point` is surfaced;
   `comparison.rounds[]` differences drive frames, but the two runs are not
   shown side-by-side and outcomes are not compared on stage.

## Assets: placeholder art only

The procured **Rive / 2.5D assets are NOT present**. `TheatreScreen` therefore
still defaults to `PlaceholderAssets` (`lib/theatre/placeholder_assets.dart`):
pure-Flutter gradients and shapes drawn with `CustomPainter`, with no image
assets, no `rive`, no `flame`. It is a layout/behaviour harness — it carries no
persona body language, no 2.5D parallax and no 60 fps rig, and cannot satisfy
S3's exit criterion on its own. The header shows a "placeholder art" chip. This
slice does not change that: when the real pack arrives, only `TheatreAssets`
(`TheatreScreen(assets: ...)`) is swapped; the frame vocabulary, this adapter,
the mapping contract, the scrubber and the tests are asset-agnostic and remain
unchanged.

## Files changed / added

| File | Change |
| --- | --- |
| `lib/panels/archive_frames.dart` | **New.** The documented adapter: `View` JSON -> `List<TheatreFrame>` (+ `ArchiveReplay`, six-mode list, divergence extraction). No `hub_models.dart` dependency. |
| `lib/panels/panels_api.dart` | Added read-only `fetchViewJson(id, mode, {query})` (raw `View` JSON). No write endpoints. |
| `lib/panels/archive_panel.dart` | Caches the adapted replay in state; adds the six-mode-gated **Play replay** card (Wrap, no unbounded Row), empty/`not recorded` state, and `Navigator.push` into `TheatreScreen(frames:, divergenceRound:, title:)`. |
| `test/panels/archive_frames_test.dart` | **New.** Adapter mapping with realistic Hub fixtures, missing/unknown/wrong-type fields, six modes, empty/null payloads, divergence extraction, raw-JSON fetch, GET-only proof. |
| `test/panels/archive_replay_widget_test.dart` | **New.** Button gating, tapping opens `TheatreScreen` with the right frame count / divergence / title, GET-only path proof, 800x600 no-overflow. |
| `production/epics/flux-theatre/ARCHIVE_REPLAY.md` | **New.** This document. |

## Test results

Commands (each prefixed with
`env -u http_proxy -u https_proxy -u HTTP_PROXY -u HTTPS_PROXY NO_PROXY=localhost,127.0.0.1`,
run from `fluxspace_client/`, using the absolute Flutter binary):

- `flutter analyze` -> **No issues found!**
- `flutter test test/panels` -> **81 tests, all passed** (49 pre-existing + 32
  new). Covers adapter mapping, the six modes, missing/wrong-type/unknown
  fields, empty and null payloads, divergence extraction, button gating, tapping
  opens `TheatreScreen` with the right frame count/divergence/title, proof that
  only read-only GET paths are requested, and no `RenderFlex` overflow at
  800x600. All tests are network-free via the injected `ScriptedFetcher`.
