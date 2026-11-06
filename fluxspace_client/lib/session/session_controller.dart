import 'dart:async';

import 'package:flutter/foundation.dart';

import '../models/hub_models.dart';
import '../services/hub_api.dart';
import '../services/token_store.dart';

/// What the client is doing right now. Kept coarse on purpose: a finer
/// enumeration would encode Hub internals, and the Hub already reports the
/// session phase.
enum SessionStage {
  /// Nothing attempted yet.
  idle,

  /// Health checked, companion bound, no match open.
  ready,

  /// Negotiating: proposals exist, parties have not both confirmed.
  negotiating,

  /// A match exists and this client holds a seat in it.
  seated,

  /// The Hub finished the match.
  finished,

  /// The last operation failed; [SessionController.error] says how.
  failed,
}

/// One line in the match log: an SSE event, a discussion line, or a client
/// message. Types are kept so the UI can style them, and so a test can assert
/// on what happened without matching English prose.
enum LogKind { event, discussion, notice, failure }

class LogLine {
  const LogLine({required this.at, required this.kind, required this.text, this.eventType = ''});

  final DateTime at;
  final LogKind kind;
  final String text;

  /// The Hub event type for [LogKind.event], empty otherwise.
  final String eventType;

  @override
  String toString() => '$kind: $text';
}

/// Everything needed to open a match, as one value.
class MatchSetup {
  const MatchSetup({
    required this.invitationId,
    required this.me,
    required this.partner,
    this.seats = 2,
    this.seed = 0,
  });

  /// Stable per match: the Hub derives the skeleton seed from it, so the same
  /// id always yields the same three proposals.
  final String invitationId;

  /// The party this client acts for. Its owner must match the token's owner,
  /// or the Hub refuses the join.
  final HubParty me;

  /// The other party. On this MVP the client confirms on their behalf, because
  /// replies that arrive over EigenFlux PM are consumed by `POST .../poll`,
  /// which needs a gateway. That is a stand-in, not a peer.
  final HubParty partner;

  final int seats;
  final int seed;
}

/// Drives one match from invitation to settlement, against [HubApi].
///
/// Deliberately free of widgets and of `Dio`: the flows worth testing are
/// "what does the client do when the Hub says 409 INVITATION_NOT_CONFIRMED"
/// and "does it keep the seat token across a reconnect", not "does the button
/// sit at the right offset".
class SessionController extends ChangeNotifier {
  SessionController({
    required HubApi api,
    required TokenStore tokens,
    DateTime Function()? clock,
  })  : _api = api,
        _tokens = tokens,
        _clock = clock ?? DateTime.now;

  final HubApi _api;
  final TokenStore _tokens;
  final DateTime Function() _clock;

  SessionStage _stage = SessionStage.idle;
  SessionView? _view;
  NegotiationView? _negotiation;
  HubException? _error;
  String _seatId = '';
  String _seatToken = '';
  String _companionId = '';
  final List<LogLine> _log = <LogLine>[];
  StreamSubscription<HubEvent>? _events;
  bool _busy = false;

  SessionStage get stage => _stage;
  SessionView? get view => _view;
  NegotiationView? get negotiation => _negotiation;
  HubException? get error => _error;
  String get seatId => _seatId;
  String get companionId => _companionId;
  bool get busy => _busy;

  /// True when this client holds a seat token for the current session, i.e.
  /// it can read that seat's private stream.
  bool get isSeated => _seatToken.isNotEmpty && _seatId.isNotEmpty;

  List<LogLine> get log => List<LogLine>.unmodifiable(_log);

  /// The seat this client occupies, if any.
  HubSeat? get mySeat => _view == null || _seatId.isEmpty ? null : _view!.seat(_seatId);

  /// The actions available in the current round. Empty before the match starts
  /// or after it ends: an action button that the Hub would reject is worse
  /// than no button.
  List<HubAction> get availableActions {
    final SessionView? view = _view;
    if (view == null || view.isFinished) return const <HubAction>[];
    return view.currentRound?.actions ?? const <HubAction>[];
  }

  /// Binds the companion and checks the Hub is reachable and the token works.
  ///
  /// [companionId] is bound here because ADR-0009 makes every governance route
  /// require it: a client that skips binding can still play, but cannot be
  /// granted or refused permissions, which would make the first budget call
  /// fail for a reason the player cannot see.
  Future<void> connect({required String companionId}) async {
    _companionId = companionId;
    await _run(() async {
      if (!await _api.health()) {
        throw const HubException(
          status: 0,
          code: 'HUB_UNREACHABLE',
          message: 'the Hub did not answer GET /health',
        );
      }
      try {
        await _api.bindCompanion(companionId);
      } on HubException catch (failure) {
        // An unauthenticated Hub refuses to bind: it has no owner to bind to
        // (POST /hub/companions answers 401 AUTH_REQUIRED). That is the Hub
        // being consistent, not a client failure, so the client carries on
        // without an ownership binding and says so. Any other code — a
        // conflict, a 403 — still stops the connect.
        if (failure.code != 'AUTH_REQUIRED' || _api.ownerId.isNotEmpty) rethrow;
        _note('unauthenticated Hub: companion $companionId is not bound to an owner');
      }
      _note('connected as ${_api.ownerId.isEmpty ? 'anonymous' : _api.ownerId}');
      _stage = SessionStage.ready;
    });
  }

  /// Opens a negotiation, confirms both parties, creates the match, and joins
  /// seat 0 as [setup.me].
  ///
  /// The acceptance of the partner's side is a local stand-in (see
  /// [MatchSetup.partner]); with a gateway the reply would arrive over PM and be
  /// picked up by `POST /hub/invitations/{id}/poll` instead.
  Future<void> startMatch(MatchSetup setup) async {
    await _run(() async {
      final NegotiationView created = await _api.createInvitation(MatchDraft(
        invitationId: setup.invitationId,
        initiator: setup.me,
        responder: setup.partner,
        seats: setup.seats,
        seed: setup.seed,
      ));
      _negotiation = created;
      _stage = SessionStage.negotiating;
      _note('${created.proposals.length} proposal(s) offered, ${created.estimatedMinutes} min');

      if (!created.isConfirmed) {
        _negotiation = await _api.acceptInvitation(created.id, agentId: setup.partner.agentId);
      }
      if (!(_negotiation?.isConfirmed ?? false)) {
        // Not an error: a real partner may still be deciding. Say so instead of
        // throwing, because the Hub will answer 409 if we proceed anyway.
        throw HubException(
          status: 409,
          code: 'INVITATION_NOT_CONFIRMED',
          message: 'invitation ${created.id} is ${_negotiation?.status ?? 'unknown'}; '
              'the partner has not confirmed',
        );
      }

      final SessionView view = await _api.createSessionFromInvitation(created.id, seed: setup.seed);
      _view = view;
      _note('match ${view.sessionId} created: ${view.skeleton?.title ?? 'untitled'}');

      // seat_0 is the initiator's seat (hub/invitation BuildClaims). Joining is
      // what reserves the owner's live-match slot (ADR-0009).
      await _takeSeat(view.sessionId, 'seat_0', setup.me.ownerId);
    });
  }

  /// Rejoins an existing match from persisted credentials.
  ///
  /// Two separate things are needed: the seat token, which only join issues, and
  /// the session view. If the token is gone the Hub will not reissue it — join
  /// is single-use — so the caller has to join again, and this reports that
  /// rather than silently reading nothing.
  Future<void> resume(String sessionId, {String seatId = 'seat_0'}) async {
    await _run(() async {
      final String? stored = await _tokens.read(TokenKeys.seatToken(sessionId));
      if (stored == null || stored.isEmpty) {
        throw HubException(
          status: 0,
          code: 'NO_SEAT_TOKEN',
          message: 'no stream token stored for $sessionId; join the seat again',
        );
      }
      _view = await _api.snapshot(sessionId);
      _seatId = seatId;
      _seatToken = stored;
      _stage = _view!.isFinished ? SessionStage.finished : SessionStage.seated;
      _note('resumed ${_view!.sessionId} at round ${_view!.round}/${_view!.totalRounds}');
      _listen();
    });
  }

  /// Joins another seat: the path used by the second client of a match.
  Future<void> joinSeat(String sessionId, String seatId, String ownerId) => _run(() async {
        await _takeSeat(sessionId, seatId, ownerId);
      });

  /// Posts a discussion line for this client's seat.
  Future<void> say(String text) async {
    final String trimmed = text.trim();
    if (trimmed.isEmpty || !isSeated) return;
    await _run(() async {
      await _api.postDiscussion(_view!.sessionId, seatId: _seatId, text: trimmed);
      // Echo locally: the Hub publishes the message as an event, but the round
      // trip is longer than a player's patience, and a message that vanishes
      // until the next poll reads as a bug.
      _append(LogLine(at: _clock(), kind: LogKind.discussion, text: trimmed));
    }, quiet: true);
  }

  /// Submits a decision for the current round.
  Future<void> act(String actionId, {String reasoning = ''}) async {
    if (!isSeated) return;
    await _run(() async {
      _view = await _api.submitAction(
        _view!.sessionId,
        seatId: _seatId,
        actionId: actionId,
        reasoning: reasoning.isEmpty ? 'chosen from the round pool' : reasoning,
      );
      _note('decision recorded: $actionId');
    });
  }

  /// Advances the phase. The Hub owns the transition rules, so an out-of-order
  /// request comes back as a 409 and is surfaced, not retried.
  Future<void> advance(String phase) async {
    if (_view == null) return;
    await _run(() async {
      _view = await _api.advance(_view!.sessionId, phase);
      _note('phase ${_view!.phase}, round ${_view!.round}/${_view!.totalRounds}');
      if (_view!.isFinished) {
        _stage = SessionStage.finished;
        _note('match finished: ${_view!.outcome}');
      }
    });
  }

  /// Refreshes the view without touching the stream. Used after an event that
  /// changes the public state, and by the UI's pull-to-refresh equivalent.
  Future<void> refresh() async {
    if (_view == null) return;
    await _run(() async {
      _view = await _api.state(_view!.sessionId);
    }, quiet: true);
  }

  /// Closes the stream. The Hub keeps the match; resuming needs the stored token.
  Future<void> disconnect() async {
    await _events?.cancel();
    _events = null;
  }

  @override
  void dispose() {
    // Not awaited: dispose cannot be async, and the subscription's own cancel
    // future has no failure mode worth surfacing here.
    unawaited(_events?.cancel());
    _events = null;
    super.dispose();
  }

  // --- internals -----------------------------------------------------------

  Future<void> _takeSeat(String sessionId, String seatId, String ownerId) async {
    final JoinResult joined = await _api.join(sessionId, seatId: seatId, ownerId: ownerId);
    _view = joined.session;
    _seatId = seatId;
    _seatToken = joined.streamToken;
    _stage = SessionStage.seated;
    await _tokens.write(TokenKeys.seatToken(sessionId), joined.streamToken);
    _note('seated as $seatId (${joined.session.seat(seatId)?.displayName ?? 'unknown'})');
    _listen();
  }

  void _listen() {
    unawaited(_events?.cancel());
    final SessionView? view = _view;
    if (view == null || _seatToken.isEmpty) return;
    _events = _api
        .events(view.sessionId, seatId: _seatId, seatToken: _seatToken)
        .listen(_onEvent, onError: (Object error) {
      final HubException failure = hubExceptionFrom(error);
      // A closed stream is normal at match end; anything else is not.
      if (failure.code == 'TRANSPORT' && (_view?.isFinished ?? false)) return;
      _error = failure;
      _append(LogLine(
        at: _clock(),
        kind: LogKind.failure,
        text: 'stream: ${failure.code} ${failure.message}',
      ));
    }, cancelOnError: false);
  }

  void _onEvent(HubEvent event) {
    _append(LogLine(
      at: _clock(),
      kind: LogKind.event,
      eventType: event.type,
      text: _describe(event),
    ));
    // Pull the authoritative view after events that change public state. The
    // event payload is a summary; the view is the source of truth, and mixing
    // the two is how a client ends up showing a round the Hub never started.
    const Set<String> refreshing = <String>{
      'match_start',
      'round_begin',
      'phase_changed',
      'resolution',
      'match_end',
    };
    if (refreshing.contains(event.type)) {
      unawaited(refresh());
      if (event.type == 'match_end') _stage = SessionStage.finished;
    }
  }

  String _describe(HubEvent event) {
    final Map<String, dynamic> p = event.payload;
    switch (event.type) {
      case 'match_start':
        return 'match started';
      case 'round_begin':
        return 'round ${p['round']} begins';
      case 'information_reveal':
        return 'clue revealed';
      case 'discussion':
        return 'seat ${p['seat_id']}: ${p['text'] ?? ''}';
      case 'decision':
        return 'seat ${p['seat_id']} chose ${p['action_id'] ?? '?'}';
      case 'resolution':
        return 'round resolved (agreement ${p['agreement'] ?? '?'})';
      case 'phase_changed':
        return 'phase ${p['phase']}';
      case 'match_end':
        return 'match over: ${p['outcome'] ?? 'unknown'}';
      default:
        // Unknown types are logged, not dropped: a new Hub event the client
        // cannot name is still evidence that something happened.
        return event.type;
    }
  }

  Future<void> _run(Future<void> Function() body, {bool quiet = false}) async {
    _busy = true;
    _error = null;
    if (!quiet) notifyListeners();
    try {
      await body();
    } on HubException catch (failure) {
      _error = failure;
      // A failure inside a live match must not erase the fact that this client
      // still holds a seat: the seat token is what makes a retry possible, and
      // dropping to `failed` would strand it.
      if (_view == null) _stage = SessionStage.failed;
      _append(LogLine(
        at: _clock(),
        kind: LogKind.failure,
        text: '${failure.code}: ${failure.message}',
      ));
    } catch (error) {
      _error = hubExceptionFrom(error);
      if (_view == null) _stage = SessionStage.failed;
      _append(LogLine(at: _clock(), kind: LogKind.failure, text: '$error'));
    } finally {
      _busy = false;
      notifyListeners();
    }
  }

  void _note(String text) => _append(LogLine(at: _clock(), kind: LogKind.notice, text: text));

  void _append(LogLine line) {
    _log.add(line);
    // The log is for a human reading a match, not an audit trail; the Hub owns
    // the authoritative event history. Capping keeps a long match from growing
    // the widget tree without bound.
    if (_log.length > 500) _log.removeRange(0, _log.length - 500);
    notifyListeners();
  }
}
