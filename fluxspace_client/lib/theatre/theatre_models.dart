/// Plain, immutable render models for the Flux Theatre render layer.
///
/// This is the only vocabulary the widgets in `lib/theatre/` speak. It has no
/// dependency on the Hub wire models (`lib/models/hub_models.dart`), on `dio`,
/// or on `dart:convert`. A [TheatreFrame] is a snapshot of "what the stage
/// should look like right now" — not a session and not an event.
///
/// Keeping the transport vocabulary and the render vocabulary separate is what
/// makes the procured Rive/2.5D asset swap a one-file change: when the assets
/// arrive, only `placeholder_assets.dart` is replaced, because every widget
/// consumes a [TheatreFrame] and a `TheatreAssets` contract, never a Hub type.
///
/// ## Missing data is never invented
///
/// The integrator maps Hub views into these models through
/// [TheatreFrame.fromMap]. A field that is absent or has the wrong type is
/// never given a plausible default: optional scalars stay `null`, optional
/// collections stay empty. Widgets render a missing value as "not recorded".
/// A client that genuinely wants a default must supply it in the adapter,
/// where the choice is visible and owned by the integrator.
library;

/// The observable state of a seat, as the state chip shows it.
///
/// There is deliberately no `unknown`/`idle` member: a seat whose state was
/// not recorded is `null` on [TheatreSeat.state], which renders as
/// "not recorded" rather than as an invented fact about the match.
enum TheatreSeatState { thinking, speaking, voted, eliminated }

/// Stable, human-readable labels for [TheatreSeatState].
extension TheatreSeatStateLabel on TheatreSeatState {
  String get label {
    switch (this) {
      case TheatreSeatState.thinking:
        return 'thinking';
      case TheatreSeatState.speaking:
        return 'speaking';
      case TheatreSeatState.voted:
        return 'voted';
      case TheatreSeatState.eliminated:
        return 'eliminated';
    }
  }
}

/// One persona occupying a seat on the stage. Immutable value object.
class TheatreSeat {
  const TheatreSeat({
    required this.id,
    this.displayName,
    this.personaId,
    this.state,
    this.isOfficial,
  });

  /// Stable seat id (for example `seat_0`). The spotlight and the Hub mapping
  /// both key off it. An empty string is the "not recorded" representation for
  /// a seat with no id — it is never replaced by a guessed id.
  final String id;

  /// Display name; `null` = not recorded.
  final String? displayName;

  /// Persona id used to pick a portrait; `null` = not recorded.
  final String? personaId;

  /// State chip value; `null` = not recorded.
  final TheatreSeatState? state;

  /// Official backfill flag. Tri-state on purpose: `null` = not recorded,
  /// `true` = show the "Official" badge, `false` = known real user companion.
  /// It is never defaulted to `false`, because "not recorded" is not the same
  /// fact as "not official" (the GDD forbids disguising official companions).
  final bool? isOfficial;

  /// Tolerant mapping from one `seats[]` entry. See [TheatreFrame.fromMap].
  factory TheatreSeat.fromMap(Map<String, dynamic> map) => TheatreSeat(
        id: _asString(map['id']),
        displayName: _asStringOrNull(map['display_name']),
        personaId: _asStringOrNull(map['persona_id']),
        state: _seatState(map['state']),
        isOfficial: _asBoolOrNull(map['official']),
      );
}

/// One ballot cast in a vote/reveal round. Immutable value object.
class TheatreVote {
  const TheatreVote({
    required this.voterId,
    this.targetId,
    this.label,
  });

  /// Id of the seat that cast the vote.
  final String voterId;

  /// Id of the seat voted for. `null` = the target was not recorded. This is
  /// not the same as an abstention; an abstention would be recorded explicitly
  /// (for example as a sentinel the integrator maps into [label]).
  final String? targetId;

  /// Optional short human label for the ballot, shown on the seat card badge.
  final String? label;

  factory TheatreVote.fromMap(Map<String, dynamic> map) => TheatreVote(
        voterId: _asString(map['voter_id']),
        targetId: _asStringOrNull(map['target_id']),
        label: _asStringOrNull(map['label']),
      );
}

/// The aggregated result of a vote reveal. Immutable value object.
class TheatreReveal {
  const TheatreReveal({
    this.tally = const <String, int>{},
    this.winnerId,
    this.note,
  });

  /// Vote counts keyed by target seat id. Empty = no tally recorded.
  final Map<String, int> tally;

  /// Winning seat id. `null` = no winner recorded or the reveal has not
  /// happened; the render layer never elects a winner from the tally itself.
  final String? winnerId;

  /// Optional free-text caption.
  final String? note;

  factory TheatreReveal.fromMap(Map<String, dynamic> map) {
    final Map<String, int> tally = <String, int>{};
    final Object? raw = map['tally'];
    if (raw is Map) {
      raw.forEach((dynamic key, dynamic value) {
        final int? count = _asIntOrNull(value);
        if (key is String && count != null) tally[key] = count;
      });
    }
    return TheatreReveal(
      tally: Map<String, int>.unmodifiable(tally),
      winnerId: _asStringOrNull(map['winner_id']),
      note: _asStringOrNull(map['note']),
    );
  }
}

/// A single renderable snapshot of the theatre. Immutable value object.
///
/// A `List<TheatreFrame>` is a replay timeline; a
/// `ValueListenable<TheatreFrame>` is a live stream. Both feed the very same
/// `TheatreScreen` composition.
class TheatreFrame {
  const TheatreFrame({
    this.round,
    this.totalRounds,
    this.phase,
    this.title,
    this.summary,
    this.activeSpeakerId,
    this.viewMode,
    this.lightGrade,
    this.seats = const <TheatreSeat>[],
    this.votes = const <TheatreVote>[],
    this.reveal,
  });

  /// 1-based round number; `null` = not recorded.
  final int? round;

  /// Total rounds in the match; `null` = not recorded.
  final int? totalRounds;

  /// Phase label (`observe` / `discuss` / `act` / `resolve`, or any Hub value);
  /// `null` = not recorded.
  final String? phase;

  /// Match / skeleton title; `null` = not recorded.
  final String? title;

  /// Decision summary / recap text; `null` = not recorded.
  final String? summary;

  /// Seat id of the current speaker; `null` = no active speaker recorded.
  /// The spotlight follows exactly this id and never picks a speaker itself.
  final String? activeSpeakerId;

  /// Replay viewing mode, if known (one of the six archive modes); `null` =
  /// not recorded / live.
  final String? viewMode;

  /// Ambient light grade (`neutral` / `warm` / `cold` / `alert`); `null` =
  /// not recorded. Forwarded to `TheatreAssets.ambient`.
  final String? lightGrade;

  /// Seats to render; empty = none recorded.
  final List<TheatreSeat> seats;

  /// Ballots for this frame; empty = no votes recorded.
  final List<TheatreVote> votes;

  /// Aggregated reveal; `null` = no reveal recorded.
  final TheatreReveal? reveal;

  /// The ballot cast by [seatId], if any. Used by the seat card vote badge.
  TheatreVote? voteFor(String seatId) {
    for (final TheatreVote vote in votes) {
      if (vote.voterId == seatId) return vote;
    }
    return null;
  }

  /// The seat with [seatId], if any.
  TheatreSeat? seatById(String seatId) {
    for (final TheatreSeat seat in seats) {
      if (seat.id == seatId) return seat;
    }
    return null;
  }

  /// The documented mapping entry point.
  ///
  /// The integrator wires a Hub `SessionView`, a `HubEvent`, or an S4 archive
  /// view into a `Map<String, dynamic>` with the keys below and calls this
  /// factory. Keeping the boundary a plain map (rather than importing Hub
  /// types) means this render layer has zero compile-time coupling to the
  /// transport: a future Hub field rename is one adapter change, not a widget
  /// rewrite.
  ///
  /// Recognised keys (all optional):
  /// ```
  /// {
  ///   'round':             int?,
  ///   'total_rounds':      int?,
  ///   'phase':             String?,
  ///   'title':             String?,
  ///   'summary':           String?,
  ///   'active_speaker_id': String?,
  ///   'view_mode':         String?,
  ///   'light_grade':       String?,
  ///   'seats':   [ { 'id', 'display_name', 'persona_id', 'state', 'official' } ],
  ///   'votes':   [ { 'voter_id', 'target_id', 'label' } ],
  ///   'reveal':  { 'winner_id', 'note', 'tally': { <id>: int } } | null,
  /// }
  /// ```
  /// `state` accepts the four [TheatreSeatState] label strings; any other
  /// value becomes `null` (not recorded) rather than a guessed state.
  ///
  /// Tolerance: a wrong type is coerced only where unambiguous (numeric
  /// strings for ints) and otherwise becomes `null` / an empty collection.
  /// Missing keys are never given plausible defaults.
  factory TheatreFrame.fromMap(Map<String, dynamic> map) => TheatreFrame(
        round: _asIntOrNull(map['round']),
        totalRounds: _asIntOrNull(map['total_rounds']),
        phase: _asStringOrNull(map['phase']),
        title: _asStringOrNull(map['title']),
        summary: _asStringOrNull(map['summary']),
        activeSpeakerId: _asStringOrNull(map['active_speaker_id']),
        viewMode: _asStringOrNull(map['view_mode']),
        lightGrade: _asStringOrNull(map['light_grade']),
        seats: _asMapList<TheatreSeat>(map['seats'], TheatreSeat.fromMap),
        votes: _asMapList<TheatreVote>(map['votes'], TheatreVote.fromMap),
        reveal: map['reveal'] is Map
            ? TheatreReveal.fromMap(_stringKeyed(map['reveal']))
            : null,
      );
}

/// Maps a whole replay/archive timeline at once. `null`, a non-list, or a list
/// with non-map entries yields an empty timeline rather than a partial one.
List<TheatreFrame> theatreFramesFromMaps(Object? value) {
  if (value is! List) return const <TheatreFrame>[];
  return List<TheatreFrame>.unmodifiable(<TheatreFrame>[
    for (final Object? item in value)
      if (item is Map) TheatreFrame.fromMap(_stringKeyed(item)),
  ]);
}

// --- tolerant decoders ------------------------------------------------------
// A wrong type is never thrown on. Missing/wrong scalars become `null` (the
// "not recorded" representation); numeric strings are accepted for int fields.

String? _asStringOrNull(Object? value) {
  if (value is String) return value.isEmpty ? null : value;
  return null;
}

String _asString(Object? value) => value is String ? value : '';

int? _asIntOrNull(Object? value) {
  if (value is int) return value;
  if (value is num) return value.toInt();
  if (value is String) return int.tryParse(value);
  return null;
}

bool? _asBoolOrNull(Object? value) {
  if (value is bool) return value;
  if (value is String) {
    if (value == 'true') return true;
    if (value == 'false') return false;
  }
  return null;
}

TheatreSeatState? _seatState(Object? value) {
  if (value is! String) return null;
  for (final TheatreSeatState state in TheatreSeatState.values) {
    if (state.label == value) return state;
  }
  return null;
}

Map<String, dynamic> _stringKeyed(Object? value) {
  if (value is Map<String, dynamic>) return value;
  if (value is Map) {
    return <String, dynamic>{
      for (final MapEntry<dynamic, dynamic> entry in value.entries)
        if (entry.key is String) (entry.key as String): entry.value,
    };
  }
  return <String, dynamic>{};
}

List<T> _asMapList<T>(Object? value, T Function(Map<String, dynamic>) from) {
  if (value is! List) return List<T>.unmodifiable(<T>[]);
  final List<T> items = <T>[];
  for (final Object? item in value) {
    if (item is Map) items.add(from(_stringKeyed(item)));
  }
  return List<T>.unmodifiable(items);
}
