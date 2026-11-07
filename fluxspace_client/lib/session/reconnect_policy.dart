import 'dart:async';
import 'dart:math' show Random;

/// Pure, dependency-free exponential backoff with bounded jitter.
///
/// The base curve is `initial * factor^(attempt-1)`, capped at [cap]. Every
/// actual wait is that base delay scaled by a multiplier drawn from
/// [[jitterMin], [jitterMax]] (default `[0.5, 1.0]`, the band this client asks
/// for) through the injected [random] source. Spreading the multiplier keeps a
/// fleet of clients from retrying in lockstep, while never waiting longer than
/// the un-jittered delay.
///
/// Nothing here reads a clock directly: [sleep] is injected so a test can drive
/// every wait deterministically, and the curve itself is exposed as the pure
/// [computeDelay] / [computeDelays] functions. The default [sleep] and
/// [random] are production-only; a test that injects its own never touches
/// `Future.delayed` or `Random`.
class ReconnectPolicy {
  ReconnectPolicy({
    this.initial = const Duration(milliseconds: 500),
    this.factor = 2.0,
    this.cap = const Duration(seconds: 30),
    this.jitterMin = 0.5,
    this.jitterMax = 1.0,
    this.maxAttempts = 8,
    Future<void> Function(Duration)? sleep,
    int Function()? random,
  })  : assert(jitterMin <= jitterMax, 'jitterMin must not exceed jitterMax'),
        _sleep = sleep ?? _defaultSleep,
        _random = random ?? _defaultRandom;

  /// Delay before the first retry; multiplied by [factor] on each consecutive
  /// failure until [cap] is reached.
  final Duration initial;

  /// Growth factor applied to successive base delays.
  final double factor;

  /// Upper bound for a single base delay.
  final Duration cap;

  /// Lower and upper ends of the jitter multiplier band.
  final double jitterMin;
  final double jitterMax;

  /// Maximum number of consecutive retries before [exhausted] is true.
  final int maxAttempts;

  final Future<void> Function(Duration) _sleep;

  /// Returns an `int` interpreted as a value in `[0, 1000]`: `0` maps to
  /// [jitterMin], `1000` to [jitterMax], values in between interpolate
  /// linearly. Injecting it makes every computed delay reproducible.
  final int Function() _random;

  /// Consecutive retries started since the last [reset].
  int _attempt = 0;

  int get attempt => _attempt;

  /// True once [maxAttempts] consecutive retries have been used.
  bool get exhausted => _attempt >= maxAttempts;

  /// Call after a successfully received event: the next failure starts again
  /// from [initial] and the attempt budget is restored.
  void reset() => _attempt = 0;

  /// The un-jittered delay for a 1-based retry [attempt] (attempt `1` is the
  /// first retry). Pure: no randomness, no sleeping, no clock. Kept in
  /// microseconds so the doubling never loses precision and overflow is capped
  /// before it can happen.
  Duration computeDelay(int attempt) {
    final int n = attempt < 1 ? 1 : attempt;
    double micros = initial.inMicroseconds.toDouble();
    final double capMicros = cap.inMicroseconds.toDouble();
    for (int i = 1; i < n; i++) {
      micros *= factor;
      if (micros >= capMicros) break;
    }
    if (micros > capMicros) micros = capMicros;
    return Duration(microseconds: micros.round());
  }

  /// The full base-delay curve for the first [count] attempts. Pure.
  /// Defaults to [maxAttempts] attempts.
  List<Duration> computeDelays([int? count]) {
    final int n = count ?? maxAttempts;
    return <Duration>[for (int i = 1; i <= n; i++) computeDelay(i)];
  }

  /// The jittered delay for a 1-based retry [attempt], using [random].
  /// Exposed separately from [waitBeforeRetry] so a test can assert the jitter
  /// band without sleeping at all.
  Duration jitteredDelay(int attempt) {
    final Duration base = computeDelay(attempt);
    final int raw = _random();
    final double clamped = raw < 0 ? 0.0 : (raw > 1000 ? 1000.0 : raw.toDouble());
    final double fraction = clamped / 1000.0;
    final double multiplier = jitterMin + (jitterMax - jitterMin) * fraction;
    return Duration(microseconds: (base.inMicroseconds * multiplier).round());
  }

  /// Increments the consecutive-attempt counter, computes the jittered delay
  /// for that attempt, and waits for it through the injected [sleep].
  ///
  /// The policy deliberately owns no cancellation token and never loops: the
  /// caller decides whether a retry is still wanted after the wait returns.
  Future<void> waitBeforeRetry() async {
    _attempt++;
    await _sleep(jitteredDelay(_attempt));
  }

  static Future<void> _defaultSleep(Duration duration) => Future<void>.delayed(duration);

  static final Random _rng = Random();

  static int _defaultRandom() => _rng.nextInt(1001);
}
