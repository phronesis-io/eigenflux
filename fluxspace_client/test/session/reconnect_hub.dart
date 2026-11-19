import 'dart:async';

import 'package:fluxspace_client/models/hub_models.dart';
import 'package:fluxspace_client/services/hub_api.dart';

import '../support/fake_hub.dart';

/// A [FakeHubApi] whose event streams are per-attempt and hand-driven.
///
/// The stock fake shares one broadcast controller for every call to
/// [HubApi.events]/[HubApi.spectate], which makes it impossible to fail one
/// attempt and then succeed on the next. This subclass gives each open its own
/// [StreamController] so a test can script an entire outage: emit on attempt 0,
/// fail it, then emit on the retry's attempt 1. No sockets and no wall-clock.
///
/// This is a helper library (not a `*_test.dart`), so the test runner does not
/// try to execute it as a suite; it is only compiled where imported.
class ReconnectHubApi extends FakeHubApi {
  ReconnectHubApi({super.ownerIdValue});

  final List<StreamController<HubEvent>> _streams = <StreamController<HubEvent>>[];

  /// The `lastEventId` each stream was opened with, in order.
  final List<int> lastEventIds = <int>[];

  /// When set, every newly opened stream fails with this error on the next
  /// microtask — after the controller has subscribed. Used to drive a bounded
  /// chain of consecutive failures deterministically.
  Object? autoFailError;

  bool get autoFail => autoFailError != null;

  /// Number of streams opened so far (initial plus retries).
  int get streamCount => _streams.length;

  StreamController<HubEvent> _open(int lastEventId) {
    final StreamController<HubEvent> controller = StreamController<HubEvent>.broadcast();
    _streams.add(controller);
    lastEventIds.add(lastEventId);
    final Object? error = autoFailError;
    if (error != null) {
      scheduleMicrotask(() {
        if (!controller.isClosed) controller.addError(error);
      });
    }
    return controller;
  }

  @override
  Stream<HubEvent> events(
    String sessionId, {
    required String seatId,
    required String seatToken,
    int lastEventId = 0,
  }) {
    calls.add('events:$sessionId:$seatId:$seatToken:$lastEventId');
    return _open(lastEventId).stream;
  }

  @override
  Stream<HubEvent> spectate(String sessionId, {required String spectatorToken, int lastEventId = 0}) {
    calls.add('spectate:$sessionId:$spectatorToken:$lastEventId');
    return _open(lastEventId).stream;
  }

  /// Delivers one event on the stream opened at [index].
  Future<void> emitOn(int index, Map<String, dynamic> payload) async {
    _streams[index].add(HubEvent.fromJson(payload));
  }

  /// Delivers a stream error on the stream opened at [index].
  void fail(int index, Object error) => _streams[index].addError(error);

  /// Ends the stream opened at [index] (the "unexpected stream end" case).
  Future<void> end(int index) async {
    await _streams[index].close();
  }

  @override
  Future<void> dispose() async {
    for (final StreamController<HubEvent> controller in _streams) {
      if (!controller.isClosed) await controller.close();
    }
    await super.dispose();
  }
}
