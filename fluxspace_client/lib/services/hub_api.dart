import '../models/hub_models.dart';

/// The Hub operations the client needs, as an interface.
///
/// A concrete class would be enough for the app, but the session controller
/// must be testable without a socket, and a fake `HubApi` is far cheaper to
/// write than an HTTP stub that reimplements the Hub's validation. Tests
/// implement this; `HubClient` is the only production implementation.
abstract class HubApi {
  /// The owner this client acts for, derived by the Hub from its token. Empty
  /// when the Hub runs unauthenticated.
  String get ownerId;

  /// `GET /health`. This is the only route ADR-0009 exempts from
  /// authentication, which is why it is also the connectivity probe.
  Future<bool> health();

  /// `POST /hub/companions`. Binding is idempotent and transfer is refused, so
  /// calling this twice is safe and calling it for someone else's companion is
  /// not possible.
  Future<void> bindCompanion(String companionId);

  /// `POST /hub/invitations`. Creates the negotiation, not the match: the
  /// Hub returns three proposals and the parties must confirm one.
  Future<NegotiationView> createInvitation(MatchDraft draft);

  /// `POST /hub/invitations/{id}/reply` with `action: accept`. Local replies
  /// exist so the flow can be driven without a real EigenFlux PM round trip;
  /// `POST .../send` needs a gateway and answers 503 without one.
  Future<NegotiationView> acceptInvitation(
    String invitationId, {
    required String agentId,
    int? selectedProposal,
  });

  /// `POST /hub/invitations/{id}/session`. Fails with 409 until every party has
  /// confirmed, so the caller must not treat a draft as a match.
  Future<SessionView> createSessionFromInvitation(String invitationId, {int seed = 0});

  /// `POST /hub/sessions/{id}/join`. Returns the seat's stream token, which is
  /// the only credential for that seat's private event stream.
  Future<JoinResult> join(String sessionId, {required String seatId, required String ownerId});

  /// `GET /hub/sessions/{id}`.
  Future<SessionView> state(String sessionId);

  /// `GET /hub/sessions/{id}/snapshot`, the resume path after a disconnect.
  Future<SessionView> snapshot(String sessionId);

  /// `GET /hub/sessions/{id}/events?seat_id=...` with `X-Seat-Token`.
  ///
  /// The stream ends when the Hub finishes the match; the caller must not
  /// assume it stays open forever.
  Stream<HubEvent> events(String sessionId, {required String seatId, required String seatToken});

  /// `POST /hub/sessions/{id}/discussion`.
  Future<void> postDiscussion(
    String sessionId, {
    required String seatId,
    required String text,
    String channel = 'public',
  });

  /// `POST /hub/sessions/{id}/actions`. [actionId] must come from the current
  /// round's pool; the Hub answers 400 ILLEGAL_ACTION_ID otherwise.
  Future<SessionView> submitAction(
    String sessionId, {
    required String seatId,
    required String actionId,
    required String reasoning,
    double confidence = 1,
  });

  /// `POST /hub/sessions/{id}/advance`.
  Future<SessionView> advance(String sessionId, String phase);
}

/// A Hub refusal, carrying the stable wire code.
///
/// The code matters more than the message: the client branches on codes
/// (`COMPANION_NOT_YOURS` is a configuration problem, `AUTH_INVALID` is a
/// credential problem), and never on English prose the Hub may reword.
class HubException implements Exception {
  const HubException({required this.status, required this.code, required this.message});

  /// 0 when the request never reached the Hub.
  final int status;
  final String code;
  final String message;

  /// A retryable connectivity failure rather than a refusal.
  bool get isTransport => status == 0;

  /// 401 means the token is missing or wrong; 403 means it is valid but not
  /// allowed to touch this object. Conflating them hides a real distinction:
  /// one is fixed by logging in, the other by using the right account.
  bool get isAuthFailure => status == 401 || status == 403;

  bool get isUnavailable => status == 503;

  @override
  String toString() => status == 0 ? 'HubException($code: $message)' : 'HubException($status $code: $message)';
}

/// Maps a Dio failure onto [HubException], preferring codes and statuses over
/// message text.
HubException hubExceptionFrom(Object error) => error is HubException
    ? error
    : HubException(status: 0, code: 'TRANSPORT', message: error.toString());
