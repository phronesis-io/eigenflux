/// Wire models for the FluxSpace Hub, transcribed from
/// `docs/architecture/fluxspace-contracts.md` §3-§5.
///
/// These are hand-written rather than generated. Nothing here needs value
/// equality, and a codegen pass in the build would mean `flutter test` could
/// not run on a clean checkout without `build_runner` first — a real cost for
/// types this small.
///
/// Field names are the wire names. Where Dart's style would suggest a
/// different spelling (the JSON says `seat_id`), the Dart getter is camelCase
/// and `fromJson` is the only place the wire name appears.
library;

/// The six-dimension companion vector (0-100). Real inputs to the decision
/// engine, not display copy.
class Personality {
  const Personality({
    this.risk = 0,
    this.logic = 0,
    this.empathy = 0,
    this.coop = 0,
    this.aggression = 0,
    this.intuition = 0,
  });

  factory Personality.fromJson(Map<String, dynamic> json) => Personality(
        risk: _d(json['risk']),
        logic: _d(json['logic']),
        empathy: _d(json['empathy']),
        coop: _d(json['coop']),
        aggression: _d(json['aggression']),
        intuition: _d(json['intuition']),
      );

  final double risk;
  final double logic;
  final double empathy;
  final double coop;
  final double aggression;
  final double intuition;
}

/// One bound seat. [joined] is the only mutable fact: a seat exists from
/// creation but carries no stream token until its owner joins.
class HubSeat {
  const HubSeat({
    required this.seatId,
    required this.ownerId,
    required this.agentId,
    required this.companionId,
    required this.displayName,
    required this.official,
    required this.joined,
    required this.personality,
  });

  factory HubSeat.fromJson(Map<String, dynamic> json) => HubSeat(
        seatId: _s(json['seat_id']),
        ownerId: _s(json['owner_id']),
        agentId: _s(json['agent_id']),
        companionId: _s(json['companion_id']),
        displayName: _s(json['display_name']),
        official: json['official'] == true,
        joined: json['joined'] == true,
        personality: Personality.fromJson(_m(json['personality'])),
      );

  final String seatId;
  final String ownerId;
  final String agentId;
  final String companionId;
  final String displayName;
  final bool official;
  final bool joined;
  final Personality personality;
}

/// A progress or danger gauge.
class HubMetric {
  const HubMetric({
    required this.key,
    required this.label,
    required this.value,
    required this.max,
    required this.good,
  });

  factory HubMetric.fromJson(Map<String, dynamic> json) => HubMetric(
        key: _s(json['key']),
        label: _s(json['label']),
        value: _d(json['value']),
        max: _d(json['max']),
        good: _s(json['good']),
      );

  final String key;
  final String label;
  final double value;
  final double max;

  /// `high` or `low`: which direction is good. The UI must not assume that a
  /// falling gauge is bad — several of these are dangers.
  final String good;

  double get fraction => max == 0 ? 0 : (value / max).clamp(0.0, 1.0);
}

/// One candidate decision from the current round's action pool.
class HubAction {
  const HubAction({
    required this.id,
    required this.label,
    required this.family,
    required this.risk,
    required this.infoGain,
    required this.teamHarm,
    required this.contest,
  });

  factory HubAction.fromJson(Map<String, dynamic> json) => HubAction(
        id: _s(json['id']),
        label: _s(json['label']),
        family: _s(json['family']),
        risk: _s(json['risk']),
        infoGain: _d(json['info_gain']),
        teamHarm: _d(json['team_harm']),
        contest: _d(json['contest']),
      );

  final String id;
  final String label;
  final String family;
  final String risk;
  final double infoGain;
  final double teamHarm;
  final double contest;
}

/// A piece of information revealed in a round.
class HubClue {
  const HubClue({required this.key, required this.label, required this.text});

  factory HubClue.fromJson(Map<String, dynamic> json) => HubClue(
        key: _s(json['key']),
        label: _s(json['label']),
        text: _s(json['text']),
      );

  final String key;
  final String label;
  final String text;
}

/// One act of the theatre.
class HubRound {
  const HubRound({
    required this.index,
    required this.title,
    required this.objective,
    required this.tension,
    required this.reveal,
    required this.actions,
  });

  factory HubRound.fromJson(Map<String, dynamic> json) => HubRound(
        index: _i(json['index']),
        title: _s(json['title']),
        objective: _s(json['objective']),
        tension: _i(json['tension']),
        reveal: _list(json['reveal'], HubClue.fromJson),
        actions: _list(json['actions'], HubAction.fromJson),
      );

  final int index;
  final String title;
  final String objective;
  final int tension;
  final List<HubClue> reveal;
  final List<HubAction> actions;
}

/// The topology layer of a generated game.
class HubSkeleton {
  const HubSkeleton({
    required this.type,
    required this.themeId,
    required this.title,
    required this.themeText,
    required this.rules,
    required this.rounds,
    required this.fingerprint,
    required this.estimatedMinutes,
    required this.dedupDegraded,
  });

  factory HubSkeleton.fromJson(Map<String, dynamic> json) => HubSkeleton(
        type: _s(json['type']),
        themeId: _s(json['theme_id']),
        title: _s(json['title']),
        themeText: _s(json['theme_text']),
        rules: _strings(json['rules']),
        rounds: _list(json['rounds'], HubRound.fromJson),
        fingerprint: _s(json['fingerprint']),
        estimatedMinutes: _i(json['estimated_minutes']),
        // Set when the freshness window could not be honoured. Surfaced in the
        // UI rather than swallowed: a replayed game is a different game.
        dedupDegraded: json['dedup_degraded'] == true,
      );

  final String type;
  final String themeId;
  final String title;
  final String themeText;
  final List<String> rules;
  final List<HubRound> rounds;
  final String fingerprint;
  final int estimatedMinutes;
  final bool dedupDegraded;

  /// Rounds are 1-based on the wire (Round.Index) and so is `SessionView.round`.
  HubRound? round(int index) {
    for (final HubRound r in rounds) {
      if (r.index == index) return r;
    }
    return null;
  }
}

/// The social contract: recorded and displayed, never system-enforced.
class HubSocialContract {
  const HubSocialContract({
    required this.sharing,
    required this.privateChannels,
    required this.replayConsent,
    required this.visibility,
  });

  factory HubSocialContract.fromJson(Map<String, dynamic> json) => HubSocialContract(
        sharing: _s(json['sharing']),
        privateChannels: _s(json['private_channels']),
        replayConsent: _s(json['replay_consent']),
        visibility: _s(json['visibility']),
      );

  final String sharing;
  final String privateChannels;
  final String replayConsent;
  final String visibility;
}

/// A validated decision, as recorded by the Hub.
class HubDecision {
  const HubDecision({
    required this.actionId,
    required this.reasoningSummary,
    required this.confidence,
    required this.source,
  });

  factory HubDecision.fromJson(Map<String, dynamic> json) => HubDecision(
        actionId: _s(json['action_id']),
        reasoningSummary: _s(json['reasoning_summary']),
        confidence: _d(json['confidence']),
        source: _s(json['source']),
      );

  final String actionId;
  final String reasoningSummary;
  final double confidence;

  /// `rule_engine` | `model` | `fallback`. Shown to the player: the client
  /// must never present a rule-engine decision as model reasoning.
  final String source;
}

/// The whole observable session.
class SessionView {
  const SessionView({
    required this.sessionId,
    required this.invitationId,
    required this.status,
    required this.round,
    required this.totalRounds,
    required this.phase,
    required this.seats,
    required this.metrics,
    required this.outcome,
    required this.skeleton,
    required this.decisions,
    required this.lastEventId,
    required this.socialContract,
  });

  factory SessionView.fromJson(Map<String, dynamic> json) {
    final Map<String, dynamic> metrics = _m(json['metrics']);
    return SessionView(
      sessionId: _s(json['session_id']),
      invitationId: _s(json['invitation_id']),
      status: _s(json['status']),
      round: _i(json['round']),
      totalRounds: _i(json['total_rounds']),
      phase: _s(json['phase']),
      seats: _list(json['seats'], HubSeat.fromJson),
      metrics: <String, HubMetric>{
        for (final MapEntry<String, dynamic> e in metrics.entries)
          e.key: HubMetric.fromJson(_m(e.value)),
      },
      outcome: _s(json['outcome']),
      skeleton: json['skeleton'] == null ? null : HubSkeleton.fromJson(_m(json['skeleton'])),
      decisions: <String, HubDecision>{
        for (final MapEntry<String, dynamic> e in _m(json['decisions']).entries)
          e.key: HubDecision.fromJson(_m(e.value)),
      },
      lastEventId: _i(json['last_event_id']),
      socialContract: HubSocialContract.fromJson(_m(json['social_contract'])),
    );
  }

  final String sessionId;
  final String invitationId;
  final String status;
  final int round;
  final int totalRounds;
  final String phase;
  final List<HubSeat> seats;
  final Map<String, HubMetric> metrics;
  final String outcome;
  final HubSkeleton? skeleton;
  final Map<String, HubDecision> decisions;
  final int lastEventId;
  final HubSocialContract socialContract;

  bool get isFinished => status == 'finished';
  bool get isLive => status == 'live';

  HubRound? get currentRound => skeleton?.round(round);

  HubSeat? seat(String seatId) {
    for (final HubSeat s in seats) {
      if (s.seatId == seatId) return s;
    }
    return null;
  }
}

/// One SSE data frame. The Hub sends `id: <n>` then `data: {json}`.
class HubEvent {
  const HubEvent({required this.id, required this.type, required this.sessionId, required this.payload});

  factory HubEvent.fromJson(Map<String, dynamic> json) {
    final Map<String, dynamic> copy = Map<String, dynamic>.of(json)
      ..remove('id')
      ..remove('type')
      ..remove('session_id');
    return HubEvent(
      id: _i(json['id']),
      type: _s(json['type']),
      sessionId: _s(json['session_id']),
      payload: copy,
    );
  }

  final int id;
  final String type;
  final String sessionId;

  /// Everything except the envelope. Kept as a map because the event catalogue
  /// is open (nine types today) and a client that crashed on an unknown type
  /// would break on the next Hub release.
  final Map<String, dynamic> payload;

  @override
  String toString() => 'HubEvent($id, $type)';
}

/// The negotiation state machine, reduced to what a client needs to display.
class NegotiationView {
  const NegotiationView({
    required this.id,
    required this.status,
    required this.seats,
    required this.proposals,
    required this.estimatedMinutes,
    required this.socialContract,
  });

  factory NegotiationView.fromJson(Map<String, dynamic> json) {
    final Map<String, dynamic> invitation = _m(json['invitation']);
    return NegotiationView(
      id: _s(json['id']),
      status: _s(json['status']),
      seats: _i(json['seats']),
      proposals: _list(invitation['proposals'], HubProposal.fromJson),
      estimatedMinutes: _i(invitation['estimated_minutes']),
      socialContract: HubSocialContract.fromJson(_m(invitation['social_contract_draft'])),
    );
  }

  final String id;
  final String status;
  final int seats;
  final List<HubProposal> proposals;
  final int estimatedMinutes;
  final HubSocialContract socialContract;

  bool get isConfirmed => status == 'confirmed';
}

/// One of the three generated game proposals in an invitation.
class HubProposal {
  const HubProposal({
    required this.index,
    required this.type,
    required this.seats,
    required this.minutes,
    required this.summary,
    required this.fingerprint,
  });

  factory HubProposal.fromJson(Map<String, dynamic> json) => HubProposal(
        index: _i(json['index']),
        type: _s(json['type']),
        seats: _i(json['seats']),
        minutes: _i(json['minutes']),
        summary: _s(json['summary']),
        fingerprint: _s(json['fingerprint']),
      );

  final int index;
  final String type;
  final int seats;
  final int minutes;
  final String summary;

  /// The dedup key for the five-match freshness rule. Exposed so the client can
  /// feed completed matches back as `history_fingerprints`.
  final String fingerprint;
}

/// One party in a negotiation. Owner ids matter: the Hub refuses an invitation
/// whose two parties share an owner ("one owner cannot invite themselves").
class HubParty {
  const HubParty({
    required this.agentId,
    required this.companionId,
    required this.ownerId,
    required this.displayName,
  });

  factory HubParty.fromJson(Map<String, dynamic> json) => HubParty(
        agentId: _s(json['agent_id']),
        companionId: _s(json['companion_id']),
        ownerId: _s(json['owner_id']),
        displayName: _s(json['display_name']),
      );

  final String agentId;
  final String companionId;
  final String ownerId;
  final String displayName;

  Map<String, dynamic> toJson() => <String, dynamic>{
        'agent_id': agentId,
        'companion_id': companionId,
        'owner_id': ownerId,
        'display_name': displayName,
      };
}

/// Everything needed to open a negotiation, kept as one value so a caller
/// cannot send a request with half of a party filled in.
class MatchDraft {
  const MatchDraft({
    required this.invitationId,
    required this.initiator,
    required this.responder,
    this.seats = 2,
    this.seed = 0,
  });

  final String invitationId;
  final HubParty initiator;
  final HubParty responder;
  final int seats;

  /// Zero asks the Hub to derive a seed from the invitation id, which is what
  /// makes the three proposals reproducible from the id alone.
  final int seed;

  Map<String, dynamic> toJson() => <String, dynamic>{
        'invitation_id': invitationId,
        'initiator': initiator.toJson(),
        'responder': responder.toJson(),
        'seats': seats,
        'seed': seed,
      };
}

/// The result of joining a seat: the view plus the one-time stream token.
class JoinResult {
  const JoinResult({required this.session, required this.streamToken});

  factory JoinResult.fromJson(Map<String, dynamic> json) => JoinResult(
        session: SessionView.fromJson(_m(json['session'])),
        streamToken: _s(json['stream_token']),
      );

  final SessionView session;

  /// Shown once, never persisted in plaintext outside the platform keychain.
  /// It reads one seat of one session and nothing else (ADR-0009 §2.3).
  final String streamToken;
}

// --- tolerant decoders -----------------------------------------------------
//
// The Hub is the authority on shape, but a client that throws on a missing
// optional field turns a cosmetic server change into a crash. These coerce
// rather than assert, and the only place a wrong type becomes visible is the
// UI showing an empty value.

String _s(Object? v) => v is String ? v : (v == null ? '' : '$v');

double _d(Object? v) {
  if (v is num) return v.toDouble();
  if (v is String) return double.tryParse(v) ?? 0;
  return 0;
}

int _i(Object? v) {
  if (v is int) return v;
  if (v is num) return v.toInt();
  if (v is String) return int.tryParse(v) ?? 0;
  return 0;
}

Map<String, dynamic> _m(Object? v) =>
    v is Map<String, dynamic> ? v : <String, dynamic>{};

List<T> _list<T>(Object? v, T Function(Map<String, dynamic>) fromJson) {
  if (v is! List) return <T>[];
  return <T>[
    for (final Object? item in v)
      if (item is Map<String, dynamic>) fromJson(item),
  ];
}

List<String> _strings(Object? v) {
  if (v is! List) return const <String>[];
  return <String>[for (final Object? item in v) if (item is String) item];
}
