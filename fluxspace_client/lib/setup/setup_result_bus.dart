import 'package:flutter/foundation.dart';

import 'setup_state.dart';

/// A single, app-wide hand-off for a completed SetupResult.
///
/// HomePage and TheatrePage are sibling pages under an IndexedStack and share
/// no ancestor widget that could hold this state. Publishing the wizard's result
/// here lets them communicate without changing the app shell, its routes, or the
/// number of bottom-navigation tabs.
///
/// This is a ValueNotifier, so a page can subscribe with addListener while it is
/// mounted and read value directly. Only the most recent result is kept; callers
/// call clear so a page that mounts later does not re-apply a stale config.
class SetupResultBus extends ValueNotifier<SetupResult?> {
  SetupResultBus._() : super(null);

  /// The one instance shared by the whole app.
  static final SetupResultBus instance = SetupResultBus._();

  /// Publish a completed setup result to every mounted listener.
  ///
  /// Setting a fresh result notifies listeners, so a page can copy the four
  /// configuration values into its controllers. This never opens a connection.
  void publish(SetupResult result) => value = result;

  /// Forget the last published result.
  ///
  /// Resetting to null keeps a stale configuration from being applied again and
  /// makes the bus trivially isolated between tests.
  void clear() => value = null;
}
