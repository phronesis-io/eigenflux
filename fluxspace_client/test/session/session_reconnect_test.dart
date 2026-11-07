import 'dart:async';

import 'package:flutter_test/flutter_test.dart';
import 'package:fluxspace_client/services/hub_api.dart';
import 'package:fluxspace_client/services/token_store.dart';
import 'package:fluxspace_client/session/reconnect_policy.dart';
import 'package:fluxspace_client/session/session_controller.dart';

import 'reconnect_hub.dart';

/// Flushes microtasks and zero-duration timers. `Future.delayed(Duration.zero)`
/// is an event-loop turn, not a wall-clock wait, so every test is deterministic
/// regardless of how long the machine actually takes.
Future<void> settle([int turns = 60]) async {
  for (int i = 0; i < turns; i++) {
    await Future<void>.delayed(Duration.zero);
  }
}

void main() {
  late ReconnectHubApi api;
  late SessionController controller;
  late List<SessionConnectionState> states;
  late List<Duration> delays;
  late void Function() stateListener;

  setUp(() {
    api = ReconnectHubApi();
    states = <SessionConnectionState>[];
    delays = <Duration>[];
  });

  tearDown(() async {
    // Remove the state listener before disposing, then let dispose() cancel
    // any subscription, pending sleep and retry loop. dispose() is idempotent.
    controller.connectionState.removeListener(stateListener);
    controller.dispose();
    await api.dispose();
  });

  /// A policy whose sleep completes immediately and records every delay, and
  /// whose randomness is pinned so jitter is deterministic.
  ReconnectPolicy immediatePolicy({int maxAttempts = 3}) => ReconnectPolicy(
        maxAttempts: maxAttempts,
        random: () => 0, // jitter factor 0.5
        sleep: (Duration d) async {
          delays.add(d);
        },
      );

  void attach(ReconnectPolicy policy) {
    controller = SessionController(api: api, tokens: MemoryTokenStore(), reconnectPolicy: policy);
    states.add(controller.connectionState.value);
    stateListener = () => states.add(controller.connectionState.value);
    controller.connectionState.addListener(stateListener);
  }

  group('observable state', () {
    test('state transitions are observable end to end', () async {
      attach(immediatePolicy());
      expect(controller.connectionState.value, SessionConnectionState.idle);

      await controller.watch('sess_1', spectatorToken: 'spec');
      await settle();
      expect(controller.connectionState.value, SessionConnectionState.live);

      await api.end(0); // unexpected end while live -> reconnecting
      await settle();
      await api.emitOn(1, <String, dynamic>{'id': 1, 'type': 'evt', 'session_id': 'sess_1'});
      await settle(); // recovered event -> live
      expect(controller.connectionState.value, SessionConnectionState.live);

      await controller.disconnect();

      expect(states, <SessionConnectionState>[
        SessionConnectionState.idle,
        SessionConnectionState.connecting,
        SessionConnectionState.live,
        SessionConnectionState.reconnecting,
        SessionConnectionState.live,
        SessionConnectionState.closed,
      ]);
    });

    test('attempt count and the last error are exposed for the UI', () async {
      attach(immediatePolicy());
      expect(controller.attemptCount, 0);
      expect(controller.maxReconnectAttempts, 3);
      expect(controller.lastConnectionError, isNull);

      await controller.watch('sess_1', spectatorToken: 'spec');
      api.fail(0, const HubException(status: 0, code: 'TRANSPORT', message: 'dropped'));
      await settle();

      expect(controller.attemptCount, greaterThanOrEqualTo(1));
      expect(controller.lastConnectionError?.code, 'TRANSPORT');
    });
  });

  group('bounded recovery', () {
    test('max consecutive attempts exhausts and reports failed', () async {
      api.autoFailError = const HubException(status: 0, code: 'TRANSPORT', message: 'socket dropped');
      attach(immediatePolicy(maxAttempts: 3));

      await controller.watch('sess_1', spectatorToken: 'spec');
      await settle();

      expect(controller.connectionState.value, SessionConnectionState.failed);
      expect(controller.attemptCount, 3);
      expect(controller.lastConnectionError?.code, 'TRANSPORT');
      expect(api.streamCount, 4); // initial + exactly 3 retries, then stop
      expect(states, containsAll(<SessionConnectionState>[
        SessionConnectionState.reconnecting,
        SessionConnectionState.failed,
      ]));
      // Each wait is the base curve scaled by the pinned 0.5 jitter.
      expect(delays, <Duration>[
        const Duration(milliseconds: 250),
        const Duration(milliseconds: 500),
        const Duration(milliseconds: 1000),
      ]);
    });

    test('a received event resets the backoff budget', () async {
      attach(immediatePolicy(maxAttempts: 5));

      await controller.watch('sess_1', spectatorToken: 'spec');
      await api.end(0);
      await settle(); // retry 1 -> 250ms
      expect(delays, <Duration>[const Duration(milliseconds: 250)]);

      await api.emitOn(1, <String, dynamic>{'id': 9, 'type': 'evt', 'session_id': 'sess_1'});
      await settle();
      expect(controller.attemptCount, 0);
      expect(controller.connectionState.value, SessionConnectionState.live);

      await api.end(1);
      await settle(); // failure after success starts again from the initial 250ms
      expect(delays, <Duration>[
        const Duration(milliseconds: 250),
        const Duration(milliseconds: 250),
      ]);
    });

    test('a finished match does not retry when its stream ends', () async {
      api.status = 'finished';
      attach(immediatePolicy());

      await controller.watch('sess_1', spectatorToken: 'spec');
      await api.end(0);
      await settle();

      expect(api.streamCount, 1);
      expect(controller.connectionState.value, isNot(SessionConnectionState.reconnecting));
    });
  });

  group('no loss, no duplicates', () {
    test('every retry carries the last processed Last-Event-ID', () async {
      attach(immediatePolicy(maxAttempts: 5));

      await controller.watch('sess_1', spectatorToken: 'spec');
      expect(api.lastEventIds, <int>[0]);

      await api.emitOn(0, <String, dynamic>{'id': 5, 'type': 'evt', 'session_id': 'sess_1'});
      await settle();
      expect(controller.lastEventId, 5);

      await api.end(0);
      await settle();
      expect(api.streamCount, 2);
      expect(api.lastEventIds, <int>[0, 5]);
      expect(api.calls, contains('spectate:sess_1:spec:5'));

      await api.emitOn(1, <String, dynamic>{'id': 6, 'type': 'evt', 'session_id': 'sess_1'});
      await settle();
      expect(controller.lastEventId, 6);

      await api.end(1);
      await settle();
      expect(api.lastEventIds.last, 6);
      expect(api.calls, contains('spectate:sess_1:spec:6'));
    });

    test('replayed event ids are delivered only once', () async {
      attach(immediatePolicy(maxAttempts: 5));

      await controller.watch('sess_1', spectatorToken: 'spec');
      await api.emitOn(0, <String, dynamic>{'id': 5, 'type': 'evt_5', 'session_id': 'sess_1'});
      await settle();

      await api.end(0);
      await settle();

      // The retry replays id 5 (a duplicate) and then delivers a new id 6.
      await api.emitOn(1, <String, dynamic>{'id': 5, 'type': 'evt_5', 'session_id': 'sess_1'});
      await api.emitOn(1, <String, dynamic>{'id': 6, 'type': 'evt_6', 'session_id': 'sess_1'});
      await settle();

      int countOf(String text) => controller.log
          .where((LogLine l) => l.kind == LogKind.event && l.text == text)
          .length;

      expect(countOf('evt_5'), 1); // the replay was suppressed
      expect(countOf('evt_6'), 1);
      expect(controller.lastEventId, 6);
      expect(controller.connectionState.value, SessionConnectionState.live);
      expect(controller.attemptCount, 0); // reset by the recovered event
    });
  });

  group('explicit stop / dispose', () {
    test('dispose cancels a pending retry and leaves no live loop', () async {
      final Completer<void> gate = Completer<void>();
      attach(ReconnectPolicy(
        maxAttempts: 5,
        random: () => 0,
        sleep: (Duration _) => gate.future, // a genuinely pending sleep
      ));

      await controller.watch('sess_1', spectatorToken: 'spec');
      await api.end(0);
      await settle();

      // A retry is sleeping; no second stream has been opened yet.
      expect(api.streamCount, 1);
      expect(controller.connectionState.value, SessionConnectionState.reconnecting);

      controller.dispose();
      gate.complete(); // the timer would normally have fired here
      await settle();

      expect(api.streamCount, 1); // no reopen -> no live loop
    });

    test('disconnect prevents any further retry', () async {
      final Completer<void> gate = Completer<void>();
      attach(ReconnectPolicy(
        maxAttempts: 5,
        random: () => 0,
        sleep: (Duration _) => gate.future,
      ));

      await controller.watch('sess_1', spectatorToken: 'spec');
      await api.end(0);
      await settle();
      expect(api.streamCount, 1);

      await controller.disconnect();
      expect(controller.connectionState.value, SessionConnectionState.closed);

      gate.complete();
      await settle();
      expect(api.streamCount, 1);
    });

    test('close() is an explicit stop and a stream end afterwards does not retry', () async {
      attach(immediatePolicy());
      await controller.watch('sess_1', spectatorToken: 'spec');

      await controller.close();
      expect(controller.connectionState.value, SessionConnectionState.closed);

      await api.end(0);
      await settle();
      expect(api.streamCount, 1);
    });
  });
}
