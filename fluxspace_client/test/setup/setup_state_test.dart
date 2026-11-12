/// Unit tests for the first-run state machine. No sockets and no platform
/// keychain: [FakeHubProbe] and [MemoryTokenStore] are injected.
library;

import 'package:flutter_test/flutter_test.dart';
import 'package:fluxspace_client/services/token_store.dart';
import 'package:fluxspace_client/setup/hub_probe.dart';
import 'package:fluxspace_client/setup/setup_state.dart';

import 'setup_fakes.dart';

/// Walk from the first step to the save step for a given auth mode.
Future<SetupState> atSave(
  FakeHubProbe probe,
  MemoryTokenStore store, {
  required bool enabled,
  bool bind = false,
}) async {
  final SetupState s = makeState(probe: probe, store: store);
  expect(await s.next(), isTrue); // hubUrl -> connection
  await s.checkConnection();
  expect(s.connectionSucceeded, isTrue);
  expect(await s.next(), isTrue); // connection -> token (on) / identity (off)
  if (enabled) {
    expect(s.step, SetupStep.ownerToken);
    s.setOwnerToken('tok-alpha');
    expect(await s.next(), isTrue); // token -> identity
  }
  expect(s.step, SetupStep.identity);
  s.setOwnerId('owner_alpha');
  s.setCompanionId('comp_alpha');
  expect(await s.next(), isTrue); // identity -> binding
  expect(s.step, SetupStep.binding);
  if (bind) await s.checkBinding();
  expect(await s.next(), isTrue); // binding -> save
  expect(s.step, SetupStep.save);
  return s;
}

void main() {
  group('HubHealth parses the real /health JSON (hub/api/health.go)', () {
    test('auth enabled is a successful, honest check', () {
      final HubHealth h = HubHealth.fromResponse(200, healthJson('enabled'));
      expect(h.reachable, isTrue);
      expect(h.status, 'ok');
      expect(h.service, 'fluxspace-hub');
      expect(h.auth, HubAuthMode.enabled);
      expect(h.authLabel, 'enabled');
      expect(h.failureMessage, isNull);
      expect(h.checkSucceeded, isTrue);
    });

    test('auth disabled is a successful, honest check', () {
      final HubHealth h = HubHealth.fromResponse(200, healthJson('disabled'));
      expect(h.auth, HubAuthMode.disabled);
      expect(h.checkSucceeded, isTrue);
    });

    test('a missing/unrecognised auth field is NOT a successful check', () {
      final HubHealth h = HubHealth.fromResponse(
        200,
        <String, dynamic>{'status': 'ok', 'service': 'fluxspace-hub'},
      );
      expect(h.auth, HubAuthMode.unknown);
      expect(h.checkSucceeded, isFalse);
      expect(h.failureMessage, contains('auth mode'));
    });

    test('a non-200, a non-ok status and a transport error all fail honestly', () {
      expect(HubHealth.fromResponse(500, healthJson('enabled')).checkSucceeded, isFalse);
      expect(
        HubHealth.fromResponse(200, <String, dynamic>{'status': 'starting', 'auth': 'disabled'})
            .checkSucceeded,
        isFalse,
      );
      final HubHealth transport = HubHealth.transport('connection refused');
      expect(transport.reachable, isFalse);
      expect(transport.checkSucceeded, isFalse);
      expect(transport.failureMessage, contains('Could not reach'));
    });
  });

  group('connectivity/auth check', () {
    test('a successful enabled probe adds the owner-token step', () async {
      final SetupState s = makeState(probe: FakeHubProbe(health: healthEnabled()));
      addTearDown(s.dispose);
      expect(await s.next(), isTrue);
      await s.checkConnection();
      expect(s.connectionSucceeded, isTrue);
      expect(s.health!.auth, HubAuthMode.enabled);
      expect(s.requiresToken, isTrue);
      expect(s.steps, contains(SetupStep.ownerToken));
      expect(s.stepCount, 7);
    });

    test('a successful disabled probe omits the owner-token step', () async {
      final SetupState s = makeState(probe: FakeHubProbe(health: healthDisabled()));
      addTearDown(s.dispose);
      expect(await s.next(), isTrue);
      await s.checkConnection();
      expect(s.connectionSucceeded, isTrue);
      expect(s.requiresToken, isFalse);
      expect(s.steps, isNot(contains(SetupStep.ownerToken)));
      expect(s.stepCount, 6);
    });

    test('a failed probe cannot be advanced past and cannot be saved', () async {
      final FakeHubProbe probe = FakeHubProbe(health: HubHealth.transport('connection refused'));
      final MemoryTokenStore store = MemoryTokenStore();
      final SetupState s = makeState(probe: probe, store: store);
      addTearDown(s.dispose);
      expect(await s.next(), isTrue); // -> connection
      await s.checkConnection();
      expect(s.connectionSucceeded, isFalse);
      expect(s.error, isNotNull);
      expect(await s.next(), isFalse); // blocked
      expect(s.step, SetupStep.connection);
      expect(await s.save(), isNull); // refused even when called directly
      expect(s.result, isNull);
      expect(s.step, isNot(SetupStep.done));
      expect(await store.read(TokenKeys.hubUrl), isNull); // nothing written
      expect(probe.healthCalls, 1); // exactly one explicit probe
    });

    test('an unknown auth mode is never claimed as success', () async {
      final FakeHubProbe probe = FakeHubProbe(
        health: HubHealth.fromResponse(
          200,
          <String, dynamic>{'status': 'ok', 'service': 'fluxspace-hub'},
        ),
      );
      final SetupState s = makeState(probe: probe, store: MemoryTokenStore());
      addTearDown(s.dispose);
      expect(await s.next(), isTrue);
      await s.checkConnection();
      expect(s.health!.auth, HubAuthMode.unknown);
      expect(s.connectionSucceeded, isFalse);
      expect(s.error, contains('auth mode'));
      expect(await s.next(), isFalse);
    });

    test('navigation makes no hidden network calls', () async {
      final FakeHubProbe probe = FakeHubProbe(health: healthDisabled());
      final SetupState s = makeState(probe: probe, store: MemoryTokenStore());
      addTearDown(s.dispose);
      expect(await s.next(), isTrue); // -> connection
      expect(probe.healthCalls, 0);
      expect(probe.bindCalls, 0);
      await s.checkConnection();
      expect(probe.healthCalls, 1);
      expect(await s.next(), isTrue); // -> identity
      expect(probe.healthCalls, 1); // no extra probe from navigation
      expect(probe.bindCalls, 0);
    });
  });

  group('validation of empty URL / token / ids', () {
    test('the pure validators report inline messages', () {
      final SetupState s = makeState(probe: FakeHubProbe());
      addTearDown(s.dispose);
      expect(s.validateUrl(''), isNotNull);
      expect(s.validateUrl('not a url'), isNotNull);
      expect(s.validateUrl('ftp://host'), isNotNull);
      expect(s.validateUrl('http://127.0.0.1:8095'), isNull);
      expect(s.validateToken(''), isNotNull);
      expect(s.validateToken('   '), isNotNull);
      expect(s.validateToken('tok-alpha'), isNull);
      expect(s.validateOwnerId(''), isNotNull);
      expect(s.validateCompanionId(''), isNotNull);
    });

    test('an empty URL blocks Next with an inline error', () async {
      final SetupState s = SetupState(
        probe: FakeHubProbe(),
        store: MemoryTokenStore(),
        initialHubUrl: '',
      );
      addTearDown(s.dispose);
      expect(await s.next(), isFalse);
      expect(s.step, SetupStep.hubUrl);
      expect(s.error, contains('Enter the Hub URL'));
    });

    test('an empty token blocks the token step when auth is enabled', () async {
      final SetupState s = makeState(probe: FakeHubProbe(health: healthEnabled()));
      addTearDown(s.dispose);
      expect(await s.next(), isTrue); // connection
      await s.checkConnection();
      expect(await s.next(), isTrue); // ownerToken
      expect(await s.next(), isFalse); // empty token
      expect(s.step, SetupStep.ownerToken);
      expect(s.error, isNotNull);
      s.setOwnerToken('tok-alpha');
      expect(await s.next(), isTrue); // -> identity
    });

    test('empty owner/companion ids block the identity step', () async {
      final SetupState s = makeState(probe: FakeHubProbe(health: healthDisabled()));
      addTearDown(s.dispose);
      expect(await s.next(), isTrue); // connection
      await s.checkConnection();
      expect(await s.next(), isTrue); // identity
      expect(await s.next(), isFalse); // both empty
      s.setOwnerId('owner_alpha');
      expect(await s.next(), isFalse); // companion empty
      s.setCompanionId('comp_alpha');
      expect(await s.next(), isTrue); // -> binding
    });
  });

  group('step-by-step transitions and back navigation', () {
    test('the full enabled flow reaches done', () async {
      final FakeHubProbe probe = FakeHubProbe(health: healthEnabled());
      final SetupState s = await atSave(probe, MemoryTokenStore(), enabled: true);
      addTearDown(s.dispose);
      expect(await s.next(), isTrue); // save performs save() -> done
      expect(s.step, SetupStep.done);
      expect(s.isLast, isTrue);
      expect(s.result, isNotNull);
    });

    test('back() honours the conditional owner-token step', () async {
      final SetupState on = makeState(probe: FakeHubProbe(health: healthEnabled()));
      addTearDown(on.dispose);
      expect(await on.next(), isTrue); // connection
      await on.checkConnection();
      expect(await on.next(), isTrue); // ownerToken
      expect(on.step, SetupStep.ownerToken);
      expect(on.back(), isTrue);
      expect(on.step, SetupStep.connection);
      expect(on.back(), isTrue);
      expect(on.step, SetupStep.hubUrl);
      expect(on.back(), isFalse); // already first

      final SetupState off = makeState(probe: FakeHubProbe(health: healthDisabled()));
      addTearDown(off.dispose);
      expect(await off.next(), isTrue); // connection
      await off.checkConnection();
      expect(await off.next(), isTrue); // identity (no token step)
      expect(off.step, SetupStep.identity);
      expect(off.back(), isTrue);
      expect(off.step, SetupStep.connection);
    });

    test('editing the URL invalidates a stale connection result', () async {
      final SetupState s = makeState(probe: FakeHubProbe(health: healthEnabled()));
      addTearDown(s.dispose);
      expect(await s.next(), isTrue); // connection
      await s.checkConnection();
      expect(s.connectionSucceeded, isTrue);
      s.setHubUrl('http://other-hub:9000');
      expect(s.connectionSucceeded, isFalse);
      expect(await s.next(), isFalse); // must re-check, cannot skip
      expect(s.step, SetupStep.connection);
    });
  });

  group('save and persistence through MemoryTokenStore', () {
    test('all four keys are written and the result is handed back', () async {
      final FakeHubProbe probe = FakeHubProbe(
        health: healthEnabled(),
        binding: const BindingCheck.success(),
      );
      final MemoryTokenStore store = MemoryTokenStore();
      final SetupState s = await atSave(probe, store, enabled: true, bind: true);
      addTearDown(s.dispose);

      final SetupResult? result = await s.save();
      expect(result, isNotNull);
      expect(s.step, SetupStep.done);
      expect(result!.hubUrl, 'http://127.0.0.1:8095');
      expect(result.ownerToken, 'tok-alpha');
      expect(result.ownerId, 'owner_alpha');
      expect(result.companionId, 'comp_alpha');
      expect(result.auth, HubAuthMode.enabled);
      expect(result.authLabel, 'enabled');
      expect(result.bindingVerified, isTrue);

      expect(await store.read(TokenKeys.hubUrl), 'http://127.0.0.1:8095');
      expect(await store.read(TokenKeys.ownerToken), 'tok-alpha');
      expect(await store.read(TokenKeys.ownerId), 'owner_alpha');
      expect(await store.read(TokenKeys.companionId), 'comp_alpha');

      expect(probe.bindCalls, 1);
      expect(probe.lastBindToken, 'tok-alpha');
      expect(probe.lastBindCompanion, 'comp_alpha');
    });

    test('an open Hub saves with no token and never claims a binding', () async {
      final FakeHubProbe probe = FakeHubProbe(health: healthDisabled());
      final MemoryTokenStore store = MemoryTokenStore();
      final SetupState s = await atSave(probe, store, enabled: false);
      addTearDown(s.dispose);

      final SetupResult? result = await s.save();
      expect(result, isNotNull);
      expect(result!.auth, HubAuthMode.disabled);
      expect(result.ownerToken, '');
      expect(await store.read(TokenKeys.ownerToken), '');
      expect(result.bindingVerified, isFalse); // skipped, never claimed
      expect(probe.bindCalls, 0);
    });

    test('a failed companion binding is never reported as verified', () async {
      final FakeHubProbe probe = FakeHubProbe(
        health: healthEnabled(),
        binding: const BindingCheck.failure(code: 'AUTH_INVALID', message: 'bad token'),
      );
      final SetupState s = makeState(probe: probe, store: MemoryTokenStore());
      addTearDown(s.dispose);
      expect(await s.next(), isTrue); // connection
      await s.checkConnection();
      expect(await s.next(), isTrue); // ownerToken
      s.setOwnerToken('tok-alpha');
      expect(await s.next(), isTrue); // identity
      s.setOwnerId('owner_alpha');
      s.setCompanionId('comp_alpha');
      expect(await s.next(), isTrue); // binding
      await s.checkBinding();
      expect(s.error, contains('AUTH_INVALID'));
      expect(s.bindingChecked, isTrue);
      expect(s.bindingVerified, isFalse);
      // The optional check may be skipped, but the result stays honest.
      expect(await s.next(), isTrue); // -> save
      final SetupResult? result = await s.save();
      expect(result, isNotNull);
      expect(result!.bindingOk, isFalse);
      expect(result.bindingVerified, isFalse);
    });

    test('a failing TokenStore is surfaced as an inline error, not success', () async {
      final FakeHubProbe probe = FakeHubProbe(health: healthDisabled());
      final SetupState s = await atSave(probe, FailingTokenStore(), enabled: false);
      addTearDown(s.dispose);
      final SetupResult? result = await s.save();
      expect(result, isNull);
      expect(s.result, isNull);
      expect(s.step, isNot(SetupStep.done));
      expect(s.error, contains('Could not save'));
    });
  });
}

/// A [TokenStore] whose writes fail, to prove a storage failure is never
/// presented as a completed setup (e.g. an unavailable platform keychain).
class FailingTokenStore extends MemoryTokenStore {
  @override
  Future<void> write(String key, String value) async =>
      throw StateError('keychain unavailable');
}
