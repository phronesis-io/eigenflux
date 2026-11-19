import 'package:flutter_test/flutter_test.dart';
import 'package:fluxspace_client/session/reconnect_policy.dart';

/// Every test injects `sleep` (and, where relevant, `random`), so no wall-clock
/// delay is ever awaited and every computed value is reproducible.
void main() {
  group('computeDelays (pure curve)', () {
    test('doubles from 500ms and caps at 30s', () {
      final ReconnectPolicy policy = ReconnectPolicy();

      expect(policy.computeDelays(8), <Duration>[
        const Duration(milliseconds: 500),
        const Duration(seconds: 1),
        const Duration(seconds: 2),
        const Duration(seconds: 4),
        const Duration(seconds: 8),
        const Duration(seconds: 16),
        const Duration(seconds: 30),
        const Duration(seconds: 30),
      ]);
    });

    test('honours a custom initial, factor and cap', () {
      final ReconnectPolicy policy = ReconnectPolicy(
        initial: const Duration(milliseconds: 100),
        factor: 3,
        cap: const Duration(milliseconds: 1000),
      );

      expect(policy.computeDelays(5), <Duration>[
        const Duration(milliseconds: 100),
        const Duration(milliseconds: 300),
        const Duration(milliseconds: 900),
        const Duration(milliseconds: 1000), // 2700 -> capped
        const Duration(milliseconds: 1000), // 8100 -> capped
      ]);
    });

    test('a single computeDelay is 1-based and clamps attempt < 1', () {
      final ReconnectPolicy policy = ReconnectPolicy();

      expect(policy.computeDelay(1), const Duration(milliseconds: 500));
      expect(policy.computeDelay(2), const Duration(seconds: 1));
      expect(policy.computeDelay(0), const Duration(milliseconds: 500));
    });

    test('computeDelays defaults to maxAttempts entries', () {
      final ReconnectPolicy policy = ReconnectPolicy(maxAttempts: 3);

      expect(policy.computeDelays(), hasLength(3));
    });

    test('the pure functions never call the injected sleep', () {
      int sleeps = 0;
      final ReconnectPolicy policy = ReconnectPolicy(
        sleep: (Duration _) async {
          sleeps++;
        },
      );

      policy.computeDelays(8);
      policy.jitteredDelay(3);

      expect(sleeps, 0);
    });
  });

  group('jitter bounds', () {
    test('lower bound (random 0) is exactly half the base delay', () {
      final ReconnectPolicy policy = ReconnectPolicy(random: () => 0);

      expect(policy.jitteredDelay(1), const Duration(milliseconds: 250));
      expect(policy.jitteredDelay(3), const Duration(seconds: 1));
    });

    test('upper bound (random 1000) is exactly the base delay', () {
      final ReconnectPolicy policy = ReconnectPolicy(random: () => 1000);

      expect(policy.jitteredDelay(1), const Duration(milliseconds: 500));
      expect(policy.jitteredDelay(3), const Duration(seconds: 2));
    });

    test('every sampled random value lands within [0.5, 1.0] of the base', () {
      const Duration base = Duration(seconds: 2); // attempt 3
      for (final int r in <int>[0, 1, 137, 500, 863, 999, 1000]) {
        final Duration jittered = ReconnectPolicy(random: () => r).jitteredDelay(3);
        expect(jittered >= const Duration(seconds: 1), isTrue,
            reason: 'r=$r produced $jittered, below the lower bound');
        expect(jittered <= base, isTrue,
            reason: 'r=$r produced $jittered, above the upper bound');
      }
    });
  });

  group('attempt budget', () {
    test('waitBeforeRetry uses the injected sleep and increments the counter', () async {
      final List<Duration> slept = <Duration>[];
      final ReconnectPolicy policy = ReconnectPolicy(
        maxAttempts: 4,
        random: () => 0,
        sleep: (Duration d) async {
          slept.add(d);
        },
      );

      await policy.waitBeforeRetry();
      await policy.waitBeforeRetry();
      await policy.waitBeforeRetry();

      expect(policy.attempt, 3);
      expect(policy.exhausted, isFalse);
      expect(slept, <Duration>[
        const Duration(milliseconds: 250), // 500ms * 0.5
        const Duration(milliseconds: 500), // 1000ms * 0.5
        const Duration(milliseconds: 1000), // 2000ms * 0.5
      ]);
    });

    test('exhausted flips true exactly at maxAttempts', () async {
      final ReconnectPolicy policy = ReconnectPolicy(
        maxAttempts: 2,
        sleep: (Duration _) async {},
      );

      expect(policy.exhausted, isFalse);
      await policy.waitBeforeRetry();
      expect(policy.exhausted, isFalse);
      await policy.waitBeforeRetry();
      expect(policy.exhausted, isTrue);
    });

    test('reset restores the initial delay and the full budget', () async {
      final List<Duration> slept = <Duration>[];
      final ReconnectPolicy policy = ReconnectPolicy(
        maxAttempts: 3,
        random: () => 0,
        sleep: (Duration d) async {
          slept.add(d);
        },
      );

      await policy.waitBeforeRetry(); // 250ms
      await policy.waitBeforeRetry(); // 500ms
      expect(policy.attempt, 2);

      policy.reset();

      expect(policy.attempt, 0);
      expect(policy.exhausted, isFalse);
      await policy.waitBeforeRetry(); // back to 250ms
      expect(slept.last, const Duration(milliseconds: 250));
    });
  });
}
