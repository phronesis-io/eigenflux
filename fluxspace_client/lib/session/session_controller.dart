import 'dart:async';

import 'package:flutter/foundation.dart';

import '../models/hub_models.dart';
import '../services/hub_api.dart';
import '../services/token_store.dart';
import 'reconnect_policy.dart';

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

  /// Watching a match this client has no seat in: the omniscient view, which is
  /// what the Theatre page is for.
  watching,

  /// The Hub finished the match.
  finished,

  /// The last operation failed; [SessionController.error] says how.
  failed,
}

/// The state of the SSE connection, exposed for the UI through
/// [SessionController.connectionState].
///
/// Deliberately separate from [SessionStage]: the stage describes the match,
/// this enum describes the transport that delivers the match to this client.
/// A match can be mid-round ([SessionStage.seated]) while the connection is
/// [reconnecting]; conflating the two is how a client ends up hiding a gap it
/// is actively trying to repair.
enum SessionConnectionState {
  /// No stream has been opened yet.
  idle,

  /// A fresh stream is being established.
  connecting,

  /// A stream subscription is open and healthy (an event has been received or
  /// a fresh subscription has just been attached).
  live,

  /// The stream failed or ended while the session was still live; a bounded
  /// [ReconnectPolicy] retry is in progress.
  reconnecting,

  /// The retry budget was exhausted; automatic recovery stopped. An explicit
  /// [SessionController.reconnect] is still possible.
  failed,

  /// The stream was closed explicitly (disconnect/close/dispose); no automatic
  /// retry will run until a fresh stream is opened.
  closed,
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
///
/// The SSE stream is self-healing. If it errors or ends while the match is
/// still live, the controller reopens it from the last processed
/// `Last-Event-ID` using a bounded [ReconnectPolicy]. Replayed ids are
/// deduplicated, so no event is lost and none is delivered twice. An explicit
/// [disconnect]/[close] and [dispose] stop all automatic retry.
class SessionController extends ChangeNotifier {
  SessionController({
    required HubApi api,
    required TokenStore tokens,
    DateTime Function()? clock,
    ReconnectPolicy? reconnectPolicy,
  })  : _api = api,
        _tokens = tokens,
        _clock = clock ?? DateTime.now {
    // When the controller owns the policy it supplies a sleep it can actually
    // cancel; a bare `Future.delayed` timer would otherwise outlive dispose().
    _policy = reconnectPolicy ?? ReconnectPolicy(sleep: _cancellableSleep);
  }

  final HubApi _api;
  final TokenStore _tokens;
  final DateTime Function() _clock;

  SessionStage _stage = SessionStage.idle;
  SessionView? _view;
  NegotiationView? _negotiation;
  HubException? _error;
  String _seatId = '';
  String _seatToken = '';
  bool _watching = false;
  String _spectatorToken = '';
  /// The highest event id this client has processed. Sent as `Last-Event-ID` on
  /// every reconnect so the gap is replayed rather than skipped.
  int _lastEventId = 0;
  /// Ids of events already delivered. Kept across retries (a replay must be
  /// suppressed) but cleared when a fresh session is opened.
  final Set<int> _seenEventIds = <int>{};
  String _companionId = '';
  final List<LogLine> _log = <LogLine>[];
  StreamSubscription<HubEvent>? _events;
  bool _busy = false;

  // --- connection recovery state ------------------------------------------

  late final ReconnectPolicy _policy;

  /// Public, listenable state of the SSE connection. The UI can bind a
  /// `ValueListenableBuilder` to this without polling the controller.
  final ValueNotifier<SessionConnectionState> connectionState =
      ValueNotifier<SessionConnectionState>(SessionConnectionState.idle);

  /// Incremented on every "stop retrying" event (fresh open, explicit close,
  /// dispose). An in-flight retry only proceeds if its generation still
  /// matches, which is what guarantees no live loop survives a close/dispose.
  int _generation = 0;

  bool _disposed = false;
  bool _connectionClosed = false;

  /// Completes to wake the `Future.any` that is racing a backoff sleep, so a
  /// pending wait unblocks immediately on close/dispose/fresh-open.
  Completer<void>? _cancelWake;

  /// Timer + gate backing the controller-owned cancellable sleep.
  Timer? _retryTimer;
  Completer<void>? _sleepGate;

  /// Consecutive automatic retries attempted for the current outage, and the
  /// most recent error that started one. Both are for the UI to render.
  int _attemptCount = 0;
  HubException? _lastConnectionError;

  SessionStage get stage => _stage;
  SessionView? get view => _view;
  NegotiationView? get negotiation => _negotiation;
  HubException? get error => _error;
  String get seatId => _seatId;
  String get companionId => _companionId;
  bool get busy => _busy;

  /// How many consecutive automatic retries have been started for the current
  /// outage. Resets to zero on a successfully received event and on any fresh
  /// stream. Read together with [connectionState].
  int get attemptCount => _attemptCount;

  /// The last stream error that triggered (or kept) a retry. Kept for display
  /// even after a successful recovery; cleared when a fresh session opens.
  HubException? get lastConnectionError => _lastConnectionError;

  /// The retry budget, exposed so the UI can render "attempt 2 / 8".
  int get maxReconnectAttempts => _policy.maxAttempts;

  /// True when this client holds a seat token for the current session, i.e.
  /// it can read that seat's private stream.
  bool get isSeated => _seatToken.isNotEmpty && _seatId.isNotEmpty && !_watching;

  /// True when the view is being read through the omniscient spectator stream.
  bool get watching => _watching;

  /// The last event id seen. Exposed so the console can show where a reconnect
  /// resumes from — a number the player can compare against the log.
  int get lastEventId => _lastEventId;

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
      // The snapshot is current by definition, so its event id is where the
      // stream must resume: earlier events are already reflected in the state.
      _lastEventId = _view!.lastEventId;
      _stage = _view!.isFinished ? SessionStage.finished : SessionStage.seated;
      _note('resumed ${_view!.sessionId} at round ${_view!.round}/${_view!.totalRounds}');
      _openFresh(fromEventId: _lastEventId);
    });
  }

  /// Watches a match through the omniscient stream.
  ///
  /// This is the owner's view: four companions play, the owner sees everything.
  /// It needs a spectator token, not an owner token, because spectating is not
  /// acting — the Hub accepts that token for `/spectate` and for the two
  /// read-only session views (contracts §3).
  Future<void> watch(String sessionId, {required String spectatorToken}) async {
    await _run(() async {
      _view = await _api.spectatorState(sessionId, spectatorToken: spectatorToken);
      _watching = true;
      _spectatorToken = spectatorToken;
      _seatId = '';
      _seatToken = '';
      _lastEventId = 0;
      _stage = _view!.isFinished ? SessionStage.finished : SessionStage.watching;
      _note('watching ${_view!.sessionId} at round ${_view!.round}/${_view!.totalRounds}');
      _openFresh(fromEventId: 0);
    });
  }

  /// Reopens the stream from the last event this client processed.
  ///
  /// Deliberately does not reload the snapshot first: reloading would advance the
  /// state to the Hub's present and then the replay would be skipped as
  /// "already known", losing exactly the events a reconnect is for.
  Future<void> reconnect() async {
    await _run(() async {
      final SessionView? view = _view;
      if (view == null) {
        throw const HubException(
          status: 0,
          code: 'NO_SESSION',
          message: 'nothing to reconnect to; open or watch a match first',
        );
      }
      _note('reconnecting from event $_lastEventId');
      _openFresh(fromEventId: _lastEventId, clearSeen: false);
    });
  }

  /// An immediate manual reconnect. Alias of [reconnect], kept as a distinct
  /// name for callers that want to express "retry now, bypassing the backoff".
  /// It resets the backoff budget and reopens from [_lastEventId] right away.
  Future<void> reconnectNow() => reconnect();

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
      _view = _watching
          ? await _api.spectatorState(_view!.sessionId, spectatorToken: _spectatorToken)
          : await _api.state(_view!.sessionId);
    }, quiet: true);
  }

  /// Closes the stream and stops all automatic retry. The Hub keeps the match;
  /// resuming needs the stored token or an explicit [reconnect].
  Future<void> disconnect() async {
    _connectionClosed = true;
    _cancelPendingRetry();
    await _events?.cancel();
    _events = null;
    _setConnectionState(SessionConnectionState.closed);
  }

  /// Explicit close. Alias of [disconnect].
  Future<void> close() => disconnect();

  @override
  void dispose() {
    if (_disposed) return; // idempotent: the widget and tests may both call it
    _disposed = true;
    _cancelPendingRetry();
    unawaited(_events?.cancel());
    _events = null;
    // `connectionState` is intentionally NOT disposed: the UI owns a
    // long-lived reference to it and removing a listener after disposal would
    // throw. It is garbage-collected with the controller.
    super.dispose();
  }

  // --- internals -----------------------------------------------------------

  Future<void> _takeSeat(String sessionId, String seatId, String ownerId) async {
    final JoinResult joined = await _api.join(sessionId, seatId: seatId, ownerId: ownerId);
    _view = joined.session;
    _seatId = seatId;
    _seatToken = joined.streamToken;
    // Joining is the opposite of spectating: a client that was watching and then
    // sits down must read its own seat stream from here on.
    _watching = false;
    _stage = SessionStage.seated;
    await _tokens.write(TokenKeys.seatToken(sessionId), joined.streamToken);
    _note('seated as $seatId (${joined.session.seat(seatId)?.displayName ?? 'unknown'})');
    _openFresh(fromEventId: 0);
  }

  /// Opens a brand-new stream for a (re)established session. Cancels any
  /// in-flight automatic retry and resets the backoff budget. [clearSeen] is
  /// false only for a manual reconnect of the SAME session, where replayed ids
  /// must stay deduplicated.
  void _openFresh({required int fromEventId, bool clearSeen = true}) {
    _connectionClosed = false;
    _cancelPendingRetry();
    _policy.reset();
    _attemptCount = 0;
    _lastConnectionError = null;
    if (clearSeen) _seenEventIds.clear();
    _setConnectionState(SessionConnectionState.connecting);
    _openStream(fromEventId: fromEventId, isRetry: false, generation: _generation);
  }

  /// Attaches a fresh subscription to the Hub event stream. [generation] is
  /// captured by the done/error handlers so a stale subscription can never
  /// schedule a retry for a connection that has since been replaced.
  void _openStream({required int fromEventId, required bool isRetry, required int generation}) {
    unawaited(_events?.cancel());
    _events = null;
    final SessionView? view = _view;
    if (view == null) return;
    if (!_watching && _seatToken.isEmpty) return;

    final Stream<HubEvent> stream = _watching
        ? _api.spectate(view.sessionId, spectatorToken: _spectatorToken, lastEventId: fromEventId)
        : _api.events(view.sessionId, seatId: _seatId, seatToken: _seatToken, lastEventId: fromEventId);

    _events = stream.listen(
      _onEvent,
      onDone: () => _onStreamDone(generation),
      onError: (Object error) => _onStreamError(error, generation),
      cancelOnError: true,
    );
    if (!isRetry) _setConnectionState(SessionConnectionState.live);
  }

  void _onStreamDone(int generation) {
    _append(LogLine(
      at: _clock(),
      kind: LogKind.notice,
      text: _view?.isFinished ?? false
          ? 'stream closed: the match is finished'
          : 'stream closed before the match finished',
    ));
    // An end while the match is still live is unexpected: recover. For a
    // finished match (or a closed/disposed controller) this is a no-op.
    _scheduleReconnect(generation);
  }

  void _onStreamError(Object error, int generation) {
    final HubException failure = hubExceptionFrom(error);
    // A closed stream is normal at match end; anything else is not.
    if (failure.code == 'TRANSPORT' && (_view?.isFinished ?? false)) return;
    _error = failure;
    _lastConnectionError = failure;
    _append(LogLine(
      at: _clock(),
      kind: LogKind.failure,
      text: 'stream: ${failure.code} ${failure.message}',
    ));
    _scheduleReconnect(generation);
  }

  /// The session is "live" from the transport's point of view: there is a view,
  /// the Hub has not finished the match, and we have not marked it finished
  /// ourselves (e.g. after a `match_end` event).
  bool get _sessionLive =>
      _view != null && !_view!.isFinished && _stage != SessionStage.finished;

  bool _activeForReconnect(int generation) =>
      !_disposed && !_connectionClosed && generation == _generation && _sessionLive;

  /// Called on an error or an unexpected end. Moves to [reconnecting] and
  /// starts one bounded backoff wait; the next failure repeats the cycle. A
  /// successfully received event resets everything (see [_onEvent]).
  void _scheduleReconnect(int generation) {
    if (!_activeForReconnect(generation)) return;
    unawaited(_events?.cancel());
    _events = null;
    if (_policy.exhausted) {
      _setConnectionState(SessionConnectionState.failed);
      return;
    }
    _attemptCount = _policy.attempt + 1;
    _setConnectionState(SessionConnectionState.reconnecting);
    unawaited(_runReconnect(generation));
  }

  /// Races the policy's backoff sleep against controller-level cancellation so
  /// a pending wait always unblocks promptly on close/dispose/fresh-open. The
  /// generation check afterwards is what actually prevents a reopen.
  Future<void> _runReconnect(int generation) async {
    final Completer<void> wake = Completer<void>();
    _cancelWake = wake;
    final Future<void> wait = _policy.waitBeforeRetry();
    try {
      await Future.any<void>(<Future<void>>[wait, wake.future]);
    } catch (_) {
      // An injected sleep that throws is treated like a cancelled retry.
    }
    if (_cancelWake == wake) _cancelWake = null;
    if (!wake.isCompleted) wake.complete();
    if (!_activeForReconnect(generation)) return;
    _notify();
    // Resume exactly from the last processed id: the Hub replays the gap and
    // [_onEvent] drops any id we have already delivered.
    _openStream(fromEventId: _lastEventId, isRetry: true, generation: generation);
  }

  /// Stops every pending retry and invalidates any in-flight one. Called on a
  /// fresh open, an explicit close, and dispose.
  void _cancelPendingRetry() {
    _generation++;
    final Completer<void>? wake = _cancelWake;
    _cancelWake = null;
    if (wake != null && !wake.isCompleted) wake.complete();
    _cancelSleep();
  }

  /// Controller-owned sleep for the default policy: a `Timer` we can cancel on
  /// demand, so dispose leaves no live timer behind.
  Future<void> _cancellableSleep(Duration duration) {
    final Completer<void> gate = Completer<void>();
    _sleepGate = gate;
    _retryTimer = Timer(duration, () {
      _retryTimer = null;
      if (!gate.isCompleted) gate.complete();
    });
    return gate.future;
  }

  void _cancelSleep() {
    _retryTimer?.cancel();
    _retryTimer = null;
    final Completer<void>? gate = _sleepGate;
    _sleepGate = null;
    if (gate != null && !gate.isCompleted) gate.complete();
  }

  void _onEvent(HubEvent event) {
    // A replayed id (from a Last-Event-ID resume) must not be delivered twice.
    // Ids of 0 carry no SSE id and cannot be deduplicated.
    if (event.id > 0 && !_seenEventIds.add(event.id)) return;
    // Monotonic per session, so max() protects against a replayed event being
    // older than what this client already applied.
    if (event.id > _lastEventId) _lastEventId = event.id;
    // A real event proves the connection works: reset the outage budget and
    // report the connection live. This is the recovery signal after a retry.
    _policy.reset();
    _attemptCount = 0;
    _setConnectionState(SessionConnectionState.live);
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
    if (!quiet) _notify();
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
      _notify();
    }
  }

  void _note(String text) => _append(LogLine(at: _clock(), kind: LogKind.notice, text: text));

  /// Sets [connectionState] and notifies both its own listeners and the
  /// controller's (so the existing `AnimatedBuilder` in the UI also rebuilds).
  /// No-op after dispose.
  void _setConnectionState(SessionConnectionState next) {
    if (_disposed) return;
    if (connectionState.value == next) return;
    connectionState.value = next;
    _notify();
  }

  void _notify() {
    if (!_disposed) notifyListeners();
  }

  void _append(LogLine line) {
    _log.add(line);
    // The log is for a human reading a match, not an audit trail; the Hub owns
    // the authoritative event history. Capping keeps a long match from growing
    // the widget tree without bound.
    if (_log.length > 500) _log.removeRange(0, _log.length - 500);
    _notify();
  }
}
