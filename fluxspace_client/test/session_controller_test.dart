import 'package:flutter_test/flutter_test.dart';
import 'package:fluxspace_client/models/hub_models.dart';
import 'package:fluxspace_client/services/hub_api.dart';
import 'package:fluxspace_client/services/token_store.dart';
import 'package:fluxspace_client/session/session_controller.dart';

import 'support/fake_hub.dart';

MatchSetup setup() => MatchSetup(
      invitationId: 'inv_demo_1',
      me: const HubParty(
        agentId: 'agent_alpha',
        companionId: 'comp_alpha',
        ownerId: 'owner_alpha',
        displayName: 'Lin Shen',
      ),
      partner: const HubParty(
        agentId: 'agent_beta',
        companionId: 'comp_beta',
        ownerId: 'owner_beta',
        displayName: 'Su Wan',
      ),
    );

void main() {
  late FakeHubApi api;
  late MemoryTokenStore tokens;
  late SessionController controller;

  setUp(() {
    api = FakeHubApi();
    tokens = MemoryTokenStore();
    controller = SessionController(api: api, tokens: tokens);
  });

  tearDown(() async {
    controller.dispose();
    await api.dispose();
  });

  group('connect', () {
    test('an unreachable Hub fails without claiming a companion was bound', () async {
      api.healthy = false;
      await controller.connect(companionId: 'comp_alpha');

      expect(controller.stage, SessionStage.failed);
      expect(controller.error?.code, 'HUB_UNREACHABLE');
      expect(api.calls, <String>['health']);
    });

    test('binding happens before the client considers itself ready', () async {
      await controller.connect(companionId: 'comp_alpha');

      expect(controller.stage, SessionStage.ready);
      expect(api.calls, <String>['health', 'bind:comp_alpha']);
    });

    test('an open Hub cannot bind, and that is not treated as a client failure', () async {
      // The Hub has no owner to bind to, so it answers 401 AUTH_REQUIRED. The
      // client carries on without an ownership binding instead of refusing to
      // work against the one mode a single machine can demo.
      final FakeHubApi anonymous = FakeHubApi(ownerIdValue: '');
      final SessionController open = SessionController(api: anonymous, tokens: MemoryTokenStore());
      addTearDown(() async {
        open.dispose();
        await anonymous.dispose();
      });
      anonymous.bindError = const HubException(
        status: 401,
        code: 'AUTH_REQUIRED',
        message: 'binding a companion requires an authenticated owner',
      );

      await open.connect(companionId: 'comp_alpha');

      expect(open.stage, SessionStage.ready);
      expect(open.log.any((LogLine l) => l.text.contains('not bound to an owner')), isTrue);
    });

    test('a real binding conflict still stops the connect', () async {
      api.bindError = const HubException(
        status: 409,
        code: 'COMPANION_ALREADY_BOUND',
        message: 'comp_alpha is already bound to another owner',
      );

      await controller.connect(companionId: 'comp_alpha');

      expect(controller.stage, SessionStage.failed);
      expect(controller.error?.code, 'COMPANION_ALREADY_BOUND');
    });
  });

  group('startMatch', () {
    test('negotiates, opens the session, seats the caller and stores the seat token', () async {
      await controller.startMatch(setup());

      expect(
        api.calls,
        <String>[
          'createInvitation:inv_demo_1:2',
          'accept:inv_demo_1:agent_beta',
          'createSession:inv_demo_1',
          'join:sess_1:seat_0:owner_alpha',
          'events:sess_1:seat_0:token_seat_0:0',
        ],
      );
      expect(controller.stage, SessionStage.seated);
      expect(controller.seatId, 'seat_0');
      expect(controller.isSeated, isTrue);
      // The token is the only credential for that seat's private stream, so it
      // has to survive a restart.
      expect(await tokens.read(TokenKeys.seatToken('sess_1')), 'token_seat_0');
      expect(controller.mySeat?.displayName, 'Lin Shen');
    });

    test('a partner who never confirms stops the flow before a session exists', () async {
      api.confirmOnAccept = false;

      await controller.startMatch(setup());

      expect(controller.error?.code, 'INVITATION_NOT_CONFIRMED');
      expect(api.calls, isNot(contains('createSession:inv_demo_1')));
      expect(controller.isSeated, isFalse);
    });

    test('a refusal from the Hub is surfaced with its code, not swallowed', () async {
      api.createError = const HubException(
        status: 409,
        code: 'OWNER_SEAT_CONFLICT',
        message: 'one owner cannot invite themselves into a match',
      );

      await controller.startMatch(setup());

      expect(controller.error?.code, 'OWNER_SEAT_CONFLICT');
      expect(controller.stage, SessionStage.failed);
      expect(api.calls, <String>['createInvitation:inv_demo_1:2']);
    });

    test('a refused join leaves the client unseated rather than pretending otherwise', () async {
      api.joinError = const HubException(
        status: 403,
        code: 'OWNER_MISMATCH',
        message: 'seat "seat_0" belongs to owner "owner_beta"',
      );

      await controller.startMatch(setup());

      expect(controller.error?.code, 'OWNER_MISMATCH');
      expect(controller.isSeated, isFalse);
      expect(await tokens.read(TokenKeys.seatToken('sess_1')), isNull);
    });
  });

  group('play', () {
    test('a discussion failure keeps the seat instead of ending the match', () async {
      await controller.startMatch(setup());
      api.discussionError = const HubException(status: 503, code: 'HUB_BUSY', message: 'try again');

      await controller.say('I think the chart was moved.');

      expect(controller.error?.code, 'HUB_BUSY');
      // The seat token is what makes a retry possible; dropping to `failed`
      // would strand a match that is still live on the Hub.
      expect(controller.stage, SessionStage.seated);
      expect(controller.isSeated, isTrue);
    });

    test('say() ignores blank input instead of posting it', () async {
      await controller.startMatch(setup());
      await controller.say('   ');
      expect(api.calls.where((String c) => c.startsWith('discussion:')), isEmpty);
    });

    test('only the current round offers actions, and only while seated', () async {
      expect(controller.availableActions, isEmpty);
      await controller.startMatch(setup());

      expect(
        controller.availableActions.map((HubAction a) => a.id),
        <String>['a1', 'a2'],
      );
    });

    test('submitting a decision refreshes the view from the Hub response', () async {
      await controller.startMatch(setup());
      await controller.act('a1', reasoning: 'the ink was wet');

      expect(api.calls, contains('action:seat_0:a1'));
      expect(controller.error, isNull);
    });

    test('advancing asks for the phase the caller named, and reports the result', () async {
      await controller.startMatch(setup());
      await controller.advance('discuss');

      expect(api.calls, contains('advance:discuss'));
      expect(controller.view?.phase, 'discuss');
    });
  });

  group('resume', () {
    test('a stored seat token reopens the stream without joining again', () async {
      final MemoryTokenStore store = MemoryTokenStore(<String, String>{
        TokenKeys.seatToken('sess_1'): 'stored_token',
      });
      final SessionController resumed = SessionController(api: api, tokens: store);
      addTearDown(resumed.dispose);
      api.status = 'live';

      await resumed.resume('sess_1');

      expect(resumed.stage, SessionStage.seated);
      expect(api.calls, contains('snapshot:sess_1'));
      expect(api.calls, contains('events:sess_1:seat_0:stored_token:7'));
      expect(api.calls.where((String c) => c.startsWith('join:')), isEmpty);
    });

    test('without a token it says so rather than reading an empty stream', () async {
      await controller.resume('sess_1');

      expect(controller.error?.code, 'NO_SEAT_TOKEN');
      expect(api.calls, isEmpty);
    });
  });

  test('a finished match closes the console', () async {
    api.status = 'live';
    await controller.startMatch(setup());
    await controller.advance('discuss');
    expect(api.calls, contains('advance:discuss'));
  });

  group('watch', () {
    test('watching reads the view with the spectator credential and opens the omniscient stream', () async {
      await controller.watch('sess_1', spectatorToken: 'spec-secret');

      expect(api.calls, <String>[
        'spectatorState:sess_1:spec-secret',
        'spectate:sess_1:spec-secret:0',
      ]);
      expect(controller.watching, isTrue);
      // Spectating is not acting: no seat, so no seat-scoped writes.
      expect(controller.isSeated, isFalse);
      expect(controller.stage, SessionStage.watching);
    });

    test('a finished match watched from the start reports finished, not watching', () async {
      api.status = 'finished';
      api.phase = 'resolve';
      await controller.watch('sess_1', spectatorToken: 'spec-secret');
      expect(controller.stage, SessionStage.finished);
    });

    test('reconnect replays from the last event seen, not from the tail', () async {
      await controller.watch('sess_1', spectatorToken: 'spec-secret');
      await api.emit(<String, dynamic>{
        'id': 5,
        'type': 'discussion',
        'session_id': 'sess_1',
        'seat_id': 'seat_1',
        'text': 'the chart was moved',
      });
      await Future<void>.delayed(Duration.zero);

      expect(controller.lastEventId, 5);
      await controller.reconnect();

      expect(api.calls.last, 'spectate:sess_1:spec-secret:5');
    });

    test('reconnect without a match refuses rather than opening an empty stream', () async {
      await controller.reconnect();
      expect(controller.error?.code, 'NO_SESSION');
      expect(api.calls, isEmpty);
    });
  });

  test('a seated client reconnects its own stream from the last event', () async {
    await controller.startMatch(setup());
    await api.emit(<String, dynamic>{'id': 9, 'type': 'decision', 'session_id': 'sess_1'});
    await Future<void>.delayed(Duration.zero);

    await controller.reconnect();

    expect(api.calls.last, 'events:sess_1:seat_0:token_seat_0:9');
  });

  // The automatic-recovery API is additive: the members above keep working
  // exactly as before, while the new observable connection state is exposed
  // for the UI. These use the stock fake and the default (production) policy,
  // so no wall-clock sleep is ever reached.
  group('connection state (additive API)', () {
    test('starts idle with a clean attempt budget and no error', () {
      expect(controller.connectionState.value, SessionConnectionState.idle);
      expect(controller.attemptCount, 0);
      expect(controller.lastConnectionError, isNull);
    });

    test('opening a stream moves the connection to live', () async {
      await controller.watch('sess_1', spectatorToken: 'spec-secret');
      expect(controller.connectionState.value, SessionConnectionState.live);
    });

    test('disconnect reports closed', () async {
      await controller.watch('sess_1', spectatorToken: 'spec-secret');
      await controller.disconnect();
      expect(controller.connectionState.value, SessionConnectionState.closed);
    });

    test('reconnectNow reopens from the last processed event', () async {
      await controller.watch('sess_1', spectatorToken: 'spec-secret');
      await api.emit(<String, dynamic>{'id': 4, 'type': 'discussion', 'session_id': 'sess_1'});
      await Future<void>.delayed(Duration.zero);

      await controller.reconnectNow();

      expect(api.calls.last, 'spectate:sess_1:spec-secret:4');
    });
  });
}
