/// Adapter: the Hub's S4 archive `View` JSON -> a Flux Theatre replay timeline.
///
/// The transport side is the response of
/// `GET /hub/archives/{id}/views/{mode}` (`hub/api/archives.go: archiveView`,
/// `hub/archive/views.go: View`, `hub/archive/reprise.go: RepriseComparison`).
/// The render side is [TheatreFrame] (`lib/theatre/theatre_models.dart`), the
/// ONLY vocabulary `lib/theatre/` speaks. This file deliberately has NO
/// dependency on `lib/models/hub_models.dart`, on `dio`, or on `dart:convert`:
/// it takes a plain decoded `Map` and returns plain render models, so a future
/// Hub field rename is one adapter change, not a widget rewrite.
///
/// ## The wire fields this adapter actually consumes
///
/// Every name below is transcribed from the Go `json:"..."` tags. Nothing here
/// is guessed.
///
/// Top-level `View` (`hub/archive/views.go`):
///   `mode`, `archive_id`, `session_id`, `speed`, `seat_id`, `duration_ms`,
///   `event_count`, `events`, `rounds`, `moments`, `comparison`.
///
/// `events[]` is `TimelineEvent`:
///   `seq`, `type`, `round`, `phase`, `seat_id`, `visibility`, `ts`,
///   `offset_ms`, `payload`.
///
/// `rounds[]` is `RoundComparison` (omniscient mode):
///   `round`, `decisions`, `agreement`, `synergy`, `delta`;
///   `decisions[]` is `DecisionAtRound`:
///   `seat_id`, `action_id`, `summaries`, `official`, `state_snapshot`.
///
/// `moments[]` is `Moment` (key_moments mode):
///   `event_id`, `round`, `kind`, `magnitude`, `reason`, `ref`.
///
/// `comparison` is `RepriseComparison` (same_spec_comparison mode,
/// `hub/archive/reprise.go`):
///   `original_id`, `reprise_id`, `skeleton_hash`, `identical_config`,
///   `divergence_point`, `divergence_seat`, `rounds`, `original_outcome`,
///   `reprise_outcome`, `outcome_changed`;
///   `rounds[]` is `RoundDiff`: `round`, `differences`;
///   `differences[]` is `SeatDiff`: `seat_id`, `original_action`,
///   `reprise_action`.
///
/// ## The six viewing modes (GDD table order, `hub/archive/views.go: Modes()`)
///
/// All six are projections over the SAME immutable event stream; the Hub omits
/// the fields a mode does not use, so a `View` carries exactly ONE of the four
/// projection collections. This adapter maps whichever collections are
/// genuinely present, in the order events -> rounds -> moments -> comparison:
///
/// | mode                     | collection used for frames |
/// | ------------------------ | -------------------------- |
/// | `full`                   | `events[]`                 |
/// | `accelerated`            | `events[]`                 |
/// | `single_character`       | `events[]`                 |
/// | `omniscient_comparison`  | `rounds[]`                 |
/// | `key_moments`            | `moments[]`                |
/// | `same_spec_comparison`   | `comparison.rounds[]`       |
///
/// Because it maps "whatever is present", an unknown or missing `mode` still
/// yields the genuinely recorded frames and never fabricates any.
///
/// ## The "not recorded" invariant (ADR-0005)
///
/// A field the Hub did not record in the `View` stays `null` / empty and renders
/// as `not recorded`. Concretely, this adapter NEVER sets:
///
/// - `active_speaker_id` — the Hub records `seat_id` (the actor of an event),
///   not "the current speaker". We do not elect a speaker.
/// - any seat `state` — the Hub records no `thinking`/`speaking`/`voted`/
///   `eliminated` state in any view. We do not guess one.
/// - any `reveal` / `winner_id` / `tally` / `votes` — the Hub records no
///   ballots, no reveal tally and no winner in the six views. We do not derive a
///   winner from an action, an outcome or a decision.
/// - `total_rounds`, `title`, `light_grade` — no `View` carries these
///   (`total_rounds` lives in `GET /hub/archives/{id}`'s `config`, which is a
///   different endpoint and deliberately not an input here).
///
/// The adapter delegates all scalar/collection decoding to
/// [TheatreFrame.fromMap], whose tolerant decoders turn a wrong-typed or
/// missing value into `null` / an empty collection rather than a plausible
/// default.
library;

import 'package:fluxspace_client/theatre/theatre_models.dart';

import 'panels_models.dart';

/// The six S4 viewing modes in GDD order. Re-exported from [ArchiveModes] so a
/// caller of this adapter needs no other import to validate a mode name.
const List<String> kArchiveViewModes = ArchiveModes.all;

/// The result of adapting one Hub `View` document into the render vocabulary.
class ArchiveReplay {
  const ArchiveReplay({
    required this.frames,
    required this.archiveId,
    this.viewMode,
    this.divergenceRound,
  });

  /// The honest result for a null / non-map / wholly empty payload.
  static const ArchiveReplay empty = ArchiveReplay(
    frames: <TheatreFrame>[],
    archiveId: '',
  );

  /// The replay timeline fed to `TheatreScreen(frames: ...)`. Empty when the
  /// Hub recorded no projection entries.
  final List<TheatreFrame> frames;

  /// The genuinely recorded `View.archive_id` (may be empty => "not recorded").
  /// Used as the replay heading.
  final String archiveId;

  /// The genuinely recorded `View.mode`; `null` when not recorded.
  final String? viewMode;

  /// `comparison.divergence_point` when it is a positive, recorded round;
  /// otherwise `null`. A recorded `0` means "never diverged" and is deliberately
  /// NOT surfaced as a divergence marker.
  final int? divergenceRound;

  bool get hasFrames => frames.isNotEmpty;
}

/// The adapter: Hub archive `View` JSON -> [ArchiveReplay] (whose [frames] is a
/// `List<TheatreFrame>`).
///
/// [viewJson] is the decoded JSON object from the GET. Null, a non-map, or a
/// map with no projection collections yields [ArchiveReplay.empty] (an honest
/// "not recorded" state), never a partial or fabricated timeline.
ArchiveReplay adaptArchiveView(Object? viewJson) {
  final Map<String, dynamic> view = _asStringMap(viewJson);
  if (view.isEmpty) return ArchiveReplay.empty;

  final String mode = _stringOrNull(view['mode']) ?? '';
  final String archiveId = _stringOrNull(view['archive_id']) ?? '';

  // Each of the six modes emits exactly one projection; map every collection
  // that is genuinely present so unknown/missing modes still map real data.
  final List<TheatreFrame> frames = <TheatreFrame>[
    ..._timelineFrames(view['events'], mode),
    ..._roundFrames(view['rounds'], mode),
    ..._momentFrames(view['moments'], mode),
    ..._comparisonFrames(view['comparison'], mode),
  ];

  return ArchiveReplay(
    frames: List<TheatreFrame>.unmodifiable(frames),
    archiveId: archiveId,
    viewMode: mode.isEmpty ? null : mode,
    divergenceRound: _divergenceRound(view['comparison']),
  );
}

/// Convenience for callers that need only the `List<TheatreFrame>` timeline.
///
/// `List<TheatreFrame> archiveViewToFrames(Object? viewJson)`.
List<TheatreFrame> archiveViewToFrames(Object? viewJson) =>
    adaptArchiveView(viewJson).frames;

// --- per-projection frame builders -----------------------------------------
// Each builds the documented `TheatreFrame.fromMap` keys using ONLY the Go wire
// fields the Hub genuinely records. Fields with no wire source are omitted.

/// `events[]` -> one frame per `TimelineEvent`.
/// `round` <- `events[].round`, `phase` <- `events[].phase`,
/// `view_mode` <- `View.mode`.
///
/// `events[].seq/type/seat_id/visibility/ts/offset_ms/payload` are genuinely
/// recorded but have no `TheatreFrame` slot (a full real-time player needs them
/// for pacing); they are intentionally not mapped here.
List<TheatreFrame> _timelineFrames(Object? raw, String mode) {
  final List<Map<String, dynamic>> events = _objectList(raw);
  return <TheatreFrame>[
    for (final Map<String, dynamic> e in events)
      TheatreFrame.fromMap(<String, dynamic>{
        'round': e['round'],
        'phase': e['phase'],
        'view_mode': mode,
      }),
  ];
}

/// `rounds[]` -> one frame per `RoundComparison` (omniscient mode).
/// `round` <- `rounds[].round`;
/// `seats[]` <- `rounds[].decisions[].seat_id` (+ `official` when recorded).
///
/// `action_id` / `summaries` / `state_snapshot` / `agreement` / `synergy` /
/// `delta` are genuinely recorded but have no `TheatreFrame` slot, so they are
/// not mapped. A seat carries only its id and official flag; its `state`,
/// `display_name` and `persona_id` are NOT invented.
List<TheatreFrame> _roundFrames(Object? raw, String mode) {
  final List<Map<String, dynamic>> rounds = _objectList(raw);
  return <TheatreFrame>[
    for (final Map<String, dynamic> r in rounds)
      TheatreFrame.fromMap(<String, dynamic>{
        'round': r['round'],
        'view_mode': mode,
        'seats': <Map<String, dynamic>>[
          for (final Map<String, dynamic> d in _objectList(r['decisions']))
            <String, dynamic>{
              'id': d['seat_id'],
              'official': d['official'],
            },
        ],
      }),
  ];
}

/// `moments[]` -> one frame per `Moment` (key_moments mode).
/// `round` <- `moments[].round`, `summary` <- `moments[].reason`,
/// `view_mode` <- `View.mode`.
///
/// `event_id` / `kind` / `magnitude` / `ref` are genuinely recorded but have no
/// `TheatreFrame` slot, so they are not mapped. `moments[].reason` is the one
/// genuine human-readable recap string in the six views, so it is used verbatim
/// as `summary` (no editorial text is composed).
List<TheatreFrame> _momentFrames(Object? raw, String mode) {
  final List<Map<String, dynamic>> moments = _objectList(raw);
  return <TheatreFrame>[
    for (final Map<String, dynamic> m in moments)
      TheatreFrame.fromMap(<String, dynamic>{
        'round': m['round'],
        'view_mode': mode,
        'summary': m['reason'],
      }),
  ];
}

/// `comparison.rounds[]` -> one frame per `RoundDiff` (same_spec_comparison).
/// `round` <- `rounds[].round`;
/// `seats[]` <- `rounds[].differences[].seat_id`.
///
/// `original_action` / `reprise_action` and the comparison-level outcomes have
/// no `TheatreFrame` slot, so they are not mapped. The divergence point is
/// surfaced separately as [ArchiveReplay.divergenceRound] for the screen's
/// `divergenceRound` marker. No winner or state is fabricated.
List<TheatreFrame> _comparisonFrames(Object? raw, String mode) {
  final Map<String, dynamic> comparison = _asStringMap(raw);
  if (comparison.isEmpty) return const <TheatreFrame>[];
  final List<Map<String, dynamic>> rounds = _objectList(comparison['rounds']);
  return <TheatreFrame>[
    for (final Map<String, dynamic> r in rounds)
      TheatreFrame.fromMap(<String, dynamic>{
        'round': r['round'],
        'view_mode': mode,
        'seats': <Map<String, dynamic>>[
          for (final Map<String, dynamic> d in _objectList(r['differences']))
            <String, dynamic>{'id': d['seat_id']},
        ],
      }),
  ];
}

/// Extracts a positive, recorded `comparison.divergence_point` for
/// `TheatreScreen(divergenceRound: ...)`. Returns `null` (not recorded) when the
/// comparison is absent, the field is missing/wrong-typed, or is `0`
/// ("never diverged"). Never infers a divergence from any other field.
int? _divergenceRound(Object? raw) {
  final Map<String, dynamic> comparison = _asStringMap(raw);
  if (comparison.isEmpty) return null;
  final int? point = _intOrNull(comparison['divergence_point']);
  if (point == null || point <= 0) return null;
  return point;
}

// --- tolerant, dependency-free decoders -------------------------------------

String? _stringOrNull(Object? v) => v is String && v.isNotEmpty ? v : null;

int? _intOrNull(Object? v) {
  if (v is int) return v;
  if (v is num) return v.toInt();
  if (v is String) return int.tryParse(v);
  return null;
}

/// A string-keyed map from any decoded JSON value; non-maps become empty.
Map<String, dynamic> _asStringMap(Object? value) {
  if (value is Map<String, dynamic>) return value;
  if (value is Map) {
    return <String, dynamic>{
      for (final MapEntry<dynamic, dynamic> e in value.entries)
        if (e.key is String) e.key as String: e.value,
    };
  }
  return <String, dynamic>{};
}

/// A list of string-keyed maps from any decoded JSON value; a non-list or
/// non-map entries yield an empty list rather than a partial one.
List<Map<String, dynamic>> _objectList(Object? value) {
  if (value is! List) return const <Map<String, dynamic>>[];
  return <Map<String, dynamic>>[
    for (final Object? item in value)
      if (item is Map) _asStringMap(item),
  ];
}
