import 'dart:async';
import 'dart:convert';

import 'package:dio/dio.dart';

import '../models/hub_models.dart';
import 'hub_api.dart';
import 'sse.dart';

/// The Hub HTTP client. The Hub is a game-engine service; all EigenFlux
/// identity/social traffic goes through a separate adapter.
///
/// Every call goes through [_guard], so a Hub refusal reaches the caller as a
/// [HubException] with the wire code intact instead of a Dio string. The codes
/// are the contract (contracts §3-§11); recovering them by parsing prose would
/// be the client-side version of trusting a hand-written document.
class HubClient implements HubApi {
  HubClient({String? baseUrl, String? ownerToken, String? ownerId, Dio? dio})
      : baseUrl = baseUrl ?? defaultBaseUrl,
        _ownerToken = ownerToken ?? '',
        _ownerId = ownerId ?? '',
        _dio = dio ?? Dio() {
    _dio.options
      ..baseUrl = this.baseUrl
      ..connectTimeout = const Duration(seconds: 3)
      ..receiveTimeout = const Duration(seconds: 10)
      ..sendTimeout = const Duration(seconds: 3);
    _dio.interceptors.add(InterceptorsWrapper(
      onRequest: (RequestOptions options, RequestInterceptorHandler handler) {
        // Set per request rather than once in BaseOptions: the token can arrive
        // after the client is constructed (the user pastes it, or the keychain
        // read resolves later) and a stale header would outlive it.
        if (_ownerToken.isNotEmpty) {
          options.headers['Authorization'] = 'Bearer $_ownerToken';
        }
        handler.next(options);
      },
    ));
  }

  /// 8095 avoids EigenFlux gateway 8080, ws 8088, console 8090 and the
  /// existing offline replay service 8092.
  static const String defaultBaseUrl = 'http://127.0.0.1:8095';

  final String baseUrl;
  final Dio _dio;

  String _ownerToken;
  String _ownerId;

  @override
  String get ownerId => _ownerId;

  /// Replaces the credential after a login or a rotation. The next request
  /// carries it; nothing is cached per connection.
  void setCredentials({required String ownerToken, required String ownerId}) {
    _ownerToken = ownerToken;
    _ownerId = ownerId;
  }

  /// Story 001 AC5/6: the client must be able to reach GET /health. It is also
  /// the only route ADR-0009 exempts from authentication, which makes it the
  /// one probe that works before the user has a token.
  @override
  Future<bool> health() async {
    try {
      final Response<dynamic> response = await _dio.get<dynamic>(
        '/health',
        // A 4xx here still means the process is up, which is what this answers.
        options: Options(validateStatus: (int? status) => status != null && status < 500),
      );
      return response.statusCode == 200;
    } catch (_) {
      return false;
    }
  }

  @override
  Future<void> bindCompanion(String companionId) => _guard<void>(() async {
        await _dio.post<dynamic>('/hub/companions', data: <String, dynamic>{'companion_id': companionId});
      });

  @override
  Future<NegotiationView> createInvitation(MatchDraft draft) => _guard<NegotiationView>(() async {
        final Response<dynamic> response = await _dio.post<dynamic>('/hub/invitations', data: draft.toJson());
        return NegotiationView.fromJson(_map(response.data));
      });

  @override
  Future<NegotiationView> acceptInvitation(
    String invitationId, {
    required String agentId,
    int? selectedProposal,
  }) =>
      _guard<NegotiationView>(() async {
        final Response<dynamic> response = await _dio.post<dynamic>(
          '/hub/invitations/$invitationId/reply',
          data: <String, dynamic>{
            // An empty message id means "not delivered over PM". The Hub uses it
            // only for reply dedup, so a local reply must not invent one.
            'message_id': '',
            'kind': 'fluxspace.invitation.reply.v1',
            'invitation_id': invitationId,
            'action': 'accept',
            'agent_id': agentId,
            if (selectedProposal != null) 'selected_proposal': selectedProposal,
          },
        );
        return NegotiationView.fromJson(_map(response.data));
      });

  @override
  Future<SessionView> createSessionFromInvitation(String invitationId, {int seed = 0}) =>
      _guard<SessionView>(() async {
        final Response<dynamic> response = await _dio.post<dynamic>(
          '/hub/invitations/$invitationId/session',
          data: <String, dynamic>{'seed': seed},
        );
        // This is the one endpoint that wraps the view: it answers
        // {"session": ..., "invitation_id": ...}.
        return _requireView(_map(_map(response.data)['session']));
      });

  @override
  Future<JoinResult> join(String sessionId, {required String seatId, required String ownerId}) =>
      _guard<JoinResult>(() async {
        final Response<dynamic> response = await _dio.post<dynamic>(
          '/hub/sessions/$sessionId/join',
          data: <String, dynamic>{'seat_id': seatId, 'owner_id': ownerId},
        );
        final Map<String, dynamic> body = _map(response.data);
        final SessionView view = _requireView(_map(body['session']));
        return JoinResult(session: view, streamToken: '${body['stream_token'] ?? ''}');
      });

  @override
  Future<SessionView> state(String sessionId) => _guard<SessionView>(() async {
        final Response<dynamic> response = await _dio.get<dynamic>('/hub/sessions/$sessionId');
        return _requireView(_map(response.data));
      });

  @override
  Future<SessionView> snapshot(String sessionId) => _guard<SessionView>(() async {
        final Response<dynamic> response = await _dio.get<dynamic>('/hub/sessions/$sessionId/snapshot');
        // The snapshot envelope wraps the view; its `ts` is the Hub's clock.
        return _requireView(_map(_map(response.data)['session']));
      });

  @override
  Future<SessionView> spectatorState(String sessionId, {required String spectatorToken}) =>
      _guard<SessionView>(() async {
        final Response<dynamic> response = await _dio.get<dynamic>(
          '/hub/sessions/$sessionId',
          options: Options(headers: <String, dynamic>{'X-Spectator-Token': spectatorToken}),
        );
        return _requireView(_map(response.data));
      });

  @override
  Stream<HubEvent> events(
    String sessionId, {
    required String seatId,
    required String seatToken,
    int lastEventId = 0,
  }) =>
      _eventStream(
        '/hub/sessions/$sessionId/events',
        query: <String, dynamic>{'seat_id': seatId},
        headers: <String, String>{
          // The header is preferred to a query parameter because query strings
          // land in server logs and shell history (contracts §3).
          'X-Seat-Token': seatToken,
        },
        lastEventId: lastEventId,
      );

  @override
  Stream<HubEvent> spectate(
    String sessionId, {
    required String spectatorToken,
    int lastEventId = 0,
  }) =>
      _eventStream(
        '/hub/sessions/$sessionId/spectate',
        headers: <String, String>{'X-Spectator-Token': spectatorToken},
        lastEventId: lastEventId,
      );

  /// One SSE request and its framing.
  ///
  /// The seat stream and the omniscient stream differ only in path and in which
  /// credential they carry, so they share this. Duplicating the framing logic
  /// would mean two places to forget `Last-Event-ID`.
  Stream<HubEvent> _eventStream(
    String path, {
    Map<String, dynamic>? query,
    required Map<String, String> headers,
    int lastEventId = 0,
  }) async* {
    final Response<ResponseBody> response;
    try {
      response = await _dio.get<ResponseBody>(
        path,
        queryParameters: query,
        options: Options(
          responseType: ResponseType.stream,
          headers: <String, dynamic>{
            ...headers,
            'Accept': 'text/event-stream',
            if (lastEventId > 0) 'Last-Event-ID': '$lastEventId',
          },
          // A refusal arrives as a normal JSON body even when the request asked
          // for a stream, so the status is inspected below instead of thrown.
          validateStatus: (int? status) => status != null,
        ),
      );
    } catch (error) {
      throw _transport(error);
    }

    final ResponseBody? body = response.data;
    final int status = response.statusCode ?? 0;
    if (status != 200 || body == null) {
      final String text = await _drain(body);
      throw _fromBody(status, text);
    }

    final SseParser parser = SseParser();
    await for (final String chunk in body.stream.cast<List<int>>().transform(utf8.decoder)) {
      for (final SseRecord record in parser.add(chunk)) {
        final HubEvent? event = _decode(record);
        if (event != null) yield event;
      }
    }
    for (final SseRecord record in parser.flush()) {
      final HubEvent? event = _decode(record);
      if (event != null) yield event;
    }
  }

  @override
  Future<void> postDiscussion(
    String sessionId, {
    required String seatId,
    required String text,
    String channel = 'public',
  }) =>
      _guard<void>(() async {
        await _dio.post<dynamic>(
          '/hub/sessions/$sessionId/discussion',
          data: <String, dynamic>{'seat_id': seatId, 'text': text, 'channel': channel},
        );
      });

  @override
  Future<SessionView> submitAction(
    String sessionId, {
    required String seatId,
    required String actionId,
    required String reasoning,
    double confidence = 1,
  }) =>
      _guard<SessionView>(() async {
        final Response<dynamic> response = await _dio.post<dynamic>(
          '/hub/sessions/$sessionId/actions',
          data: <String, dynamic>{
            'seat_id': seatId,
            'action_id': actionId,
            'reasoning_summary': reasoning,
            'confidence': confidence,
          },
        );
        return _requireView(_map(response.data));
      });

  @override
  Future<SessionView> advance(String sessionId, String phase) => _guard<SessionView>(() async {
        final Response<dynamic> response = await _dio.post<dynamic>(
          '/hub/sessions/$sessionId/advance',
          data: <String, dynamic>{'phase': phase},
        );
        return _requireView(_map(response.data));
      });

  // --- plumbing ------------------------------------------------------------

  Future<T> _guard<T>(Future<T> Function() body) async {
    try {
      return await body();
    } on HubException {
      rethrow;
    } on DioException catch (error) {
      throw _fromResponse(error.response, error);
    } catch (error) {
      throw _transport(error);
    }
  }

  HubException _fromResponse(Response<dynamic>? response, Object fallback) {
    final int status = response?.statusCode ?? 0;
    if (status == 0) return _transport(fallback);
    final Object? data = response?.data;
    if (data is Map) {
      final Object? envelope = data['error'];
      if (envelope is Map) {
        return HubException(
          status: status,
          code: '${envelope['code'] ?? 'UNKNOWN'}',
          message: '${envelope['message'] ?? ''}',
        );
      }
    }
    return HubException(status: status, code: 'HTTP_$status', message: '$data');
  }

  HubException _fromBody(int status, String text) {
    if (status == 0) return HubException(status: 0, code: 'TRANSPORT', message: text);
    try {
      final Object? decoded = jsonDecode(text);
      if (decoded is Map && decoded['error'] is Map) {
        final Map<dynamic, dynamic> envelope = decoded['error'] as Map<dynamic, dynamic>;
        return HubException(
          status: status,
          code: '${envelope['code'] ?? 'UNKNOWN'}',
          message: '${envelope['message'] ?? ''}',
        );
      }
    } catch (_) {
      // Not JSON: fall through to the raw text.
    }
    return HubException(status: status, code: 'HTTP_$status', message: text);
  }

  HubException _transport(Object error) =>
      HubException(status: 0, code: 'TRANSPORT', message: error.toString());

  Future<String> _drain(ResponseBody? body) async {
    if (body == null) return '';
    final StringBuffer buffer = StringBuffer();
    await for (final String chunk in body.stream.cast<List<int>>().transform(utf8.decoder)) {
      buffer.write(chunk);
    }
    return buffer.toString();
  }

  /// Decodes one SSE payload. A malformed frame is skipped rather than thrown:
  /// one bad frame should not tear down a live match, and the Hub's event ids
  /// keep the stream resumable from the last good one.
  HubEvent? _decode(SseRecord record) {
    try {
      final Object? decoded = jsonDecode(record.data);
      if (decoded is Map<String, dynamic>) return HubEvent.fromJson(decoded);
    } catch (_) {
      return null;
    }
    return null;
  }

  static Map<String, dynamic> _map(Object? value) =>
      value is Map<String, dynamic> ? value : <String, dynamic>{};

  /// Parses a session view and refuses an empty one.
  ///
  /// The field decoders are tolerant so a cosmetic Hub change cannot crash the
  /// UI, but tolerance here would be a bug: an envelope this client did not
  /// expect parses into a view with no id, no seats and no rounds, and the
  /// console would render an empty match rather than say the shape changed.
  /// This is how a wrapper mistake gets caught — silently, it looks like a
  /// match with nothing in it.
  static SessionView _requireView(Map<String, dynamic> json) {
    final SessionView view = SessionView.fromJson(json);
    if (view.sessionId.isEmpty) {
      throw const HubException(
        status: 200,
        code: 'UNEXPECTED_RESPONSE',
        message: 'the Hub answered 200 but the body carried no session; '
            'the response shape may have changed',
      );
    }
    return view;
  }
}
