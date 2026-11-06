import 'dart:convert';
import 'dart:typed_data';

import 'package:dio/dio.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:fluxspace_client/models/hub_models.dart';
import 'package:fluxspace_client/services/hub_api.dart';
import 'package:fluxspace_client/services/hub_client.dart';

import 'support/fake_hub.dart';

/// Adapter that answers from a script and records what was asked.
///
/// Dio's own adapter would need a socket; the contract under test is the wire
/// shape (headers, bodies, status-to-code mapping), which is entirely visible at
/// this seam.
class RecordingAdapter implements HttpClientAdapter {
  RecordingAdapter(this.respond);

  final ResponseBody Function(RequestOptions options) respond;
  final List<RequestOptions> requests = <RequestOptions>[];

  @override
  Future<ResponseBody> fetch(
    RequestOptions options,
    Stream<Uint8List>? requestStream,
    Future<void>? cancelFuture,
  ) async {
    requests.add(options);
    return respond(options);
  }

  @override
  void close({bool force = false}) {}
}

ResponseBody json(Object body, {int status = 200}) => ResponseBody.fromString(
      jsonEncode(body),
      status,
      headers: <String, List<String>>{
        Headers.contentTypeHeader: <String>['application/json'],
      },
    );

ResponseBody errorBody(String code, String message, {int status = 400}) => json(
      <String, dynamic>{
        'error': <String, dynamic>{'code': code, 'message': message},
      },
      status: status,
    );

HubClient clientWith(RecordingAdapter adapter, {String? token, String? ownerId}) => HubClient(
      ownerToken: token,
      ownerId: ownerId,
      dio: Dio()..httpClientAdapter = adapter,
    );

void main() {
  test('the bearer token is attached when present and absent when not', () async {
    final RecordingAdapter adapter = RecordingAdapter((_) => json(<String, dynamic>{'ok': true}));
    await clientWith(adapter, token: 'tok-alpha', ownerId: 'owner_alpha').health();
    await clientWith(adapter).health();

    expect(adapter.requests.first.headers['Authorization'], 'Bearer tok-alpha');
    expect(adapter.requests.last.headers.containsKey('Authorization'), isFalse);
  });

  test('the owner id is what the caller said, not something derived', () {
    final HubClient client = clientWith(
      RecordingAdapter((_) => json(<String, dynamic>{})),
      ownerId: 'owner_alpha',
    );
    expect(client.ownerId, 'owner_alpha');
    // Rotating the credential must not leave the old one on later requests.
    client.setCredentials(ownerToken: 'tok-beta', ownerId: 'owner_beta');
    expect(client.ownerId, 'owner_beta');
  });

  test('a Hub refusal keeps its wire code instead of becoming a string', () async {
    final RecordingAdapter adapter = RecordingAdapter(
      (_) => errorBody('COMPANION_NOT_YOURS', 'companion belongs to another owner', status: 403),
    );

    await expectLater(
      clientWith(adapter, token: 'tok-beta').bindCompanion('comp_alpha'),
      throwsA(isA<HubException>()
          .having((HubException e) => e.code, 'code', 'COMPANION_NOT_YOURS')
          .having((HubException e) => e.status, 'status', 403)
          .having((HubException e) => e.isAuthFailure, 'isAuthFailure', isTrue)),
    );
  });

  test('join returns the one-time stream token beside the view', () async {
    final RecordingAdapter adapter = RecordingAdapter((RequestOptions options) {
      expect(options.method, 'POST');
      expect(options.path, '/hub/sessions/sess_1/join');
      expect(options.data, <String, dynamic>{'seat_id': 'seat_0', 'owner_id': 'owner_alpha'});
      return json(<String, dynamic>{
        'session': sessionJson(joined: <String>{'seat_0'}),
        'stream_token': 'tok_seat_0',
      });
    });

    final JoinResult joined = await clientWith(adapter, token: 'tok-alpha')
        .join('sess_1', seatId: 'seat_0', ownerId: 'owner_alpha');

    expect(joined.streamToken, 'tok_seat_0');
    expect(joined.session.seat('seat_0')?.displayName, 'Lin Shen');
  });

  test('the invitation body carries both parties with their owners', () async {    final RecordingAdapter adapter = RecordingAdapter((_) => json(negotiationJson()));

    final NegotiationView negotiation = await clientWith(adapter, token: 'tok-alpha').createInvitation(
      const MatchDraft(
        invitationId: 'inv_demo_1',
        initiator: HubParty(
          agentId: 'agent_alpha',
          companionId: 'comp_alpha',
          ownerId: 'owner_alpha',
          displayName: 'Lin Shen',
        ),
        responder: HubParty(
          agentId: 'agent_beta',
          companionId: 'comp_beta',
          ownerId: 'owner_beta',
          displayName: 'Su Wan',
        ),
      ),
    );

    final Map<String, dynamic> body = adapter.requests.single.data as Map<String, dynamic>;
    expect(body['invitation_id'], 'inv_demo_1');
    expect((body['initiator'] as Map<String, dynamic>)['owner_id'], 'owner_alpha');
    expect((body['responder'] as Map<String, dynamic>)['agent_id'], 'agent_beta');
    expect(negotiation.proposals, hasLength(3));
    expect(negotiation.isConfirmed, isFalse);
  });

  test('the event stream parses frames and stops at end of stream', () async {
    final RecordingAdapter adapter = RecordingAdapter((RequestOptions options) {
      expect(options.path, '/hub/sessions/sess_1/events');
      expect(options.headers['X-Seat-Token'], 'tok_seat_0');
      // The seat id stays in the query string as a hint; the token is the
      // credential.
      expect(options.queryParameters['seat_id'], 'seat_0');
      const String frames = ''
          'id: 1\ndata: {"id":1,"type":"round_begin","session_id":"sess_1","round":1}\n\n'
          'id: 2\ndata: {"id":2,"type":"decision","session_id":"sess_1","seat_id":"seat_1"}\n\n';
      return ResponseBody(
        Stream<Uint8List>.fromIterable(<Uint8List>[Uint8List.fromList(utf8.encode(frames))]),
        200,
        headers: <String, List<String>>{
          Headers.contentTypeHeader: <String>['text/event-stream'],
        },
      );
    });

    final List<HubEvent> events = await clientWith(adapter, token: 'tok-alpha')
        .events('sess_1', seatId: 'seat_0', seatToken: 'tok_seat_0')
        .toList();

    expect(events.map((HubEvent e) => e.type), <String>['round_begin', 'decision']);
    expect(events.first.payload['round'], 1);
  });

  test('a refused stream reports the Hub code rather than an empty stream', () async {
    final RecordingAdapter adapter = RecordingAdapter(
      (_) => errorBody('SEAT_TOKEN_REQUIRED', 'this stream requires the seat token issued at join',
          status: 401),
    );

    await expectLater(
      clientWith(adapter, token: 'tok-alpha')
          .events('sess_1', seatId: 'seat_0', seatToken: '')
          .toList(),
      throwsA(isA<HubException>().having((HubException e) => e.code, 'code', 'SEAT_TOKEN_REQUIRED')),
    );
  });

  test('health is false when the Hub answers 5xx and false when nothing answers', () async {
    final RecordingAdapter serverError =
        RecordingAdapter((_) => json(<String, dynamic>{'error': 'down'}, status: 500));
    expect(await clientWith(serverError).health(), isFalse);

    final RecordingAdapter dead = RecordingAdapter((_) => throw DioException.connectionError(
          requestOptions: RequestOptions(path: '/health'),
          reason: 'connection refused',
        ));
    expect(await clientWith(dead).health(), isFalse);
  });

  test('creating a session reads the wrapped envelope, not the top level', () async {
    // This endpoint answers {"session": ..., "invitation_id": ...} while its
    // neighbours answer a bare view. Reading the top level produced a view with
    // no id, no seats and no rounds — which is exactly what happened the first
    // time this client met a real Hub.
    final RecordingAdapter adapter = RecordingAdapter(
      (_) => json(<String, dynamic>{'session': sessionJson(), 'invitation_id': 'inv_demo_1'}),
    );

    final SessionView view = await clientWith(adapter, token: 'tok-alpha')
        .createSessionFromInvitation('inv_demo_1');

    expect(view.sessionId, 'sess_1');
    expect(view.seats, hasLength(2));
  });

  test('a body with no session is refused instead of rendering an empty match', () async {
    final RecordingAdapter adapter = RecordingAdapter((_) => json(<String, dynamic>{'unexpected': true}));

    await expectLater(
      clientWith(adapter, token: 'tok-alpha').state('sess_1'),
      throwsA(isA<HubException>().having((HubException e) => e.code, 'code', 'UNEXPECTED_RESPONSE')),
    );
  });
}
