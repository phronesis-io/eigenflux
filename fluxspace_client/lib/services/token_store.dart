import 'package:flutter_secure_storage/flutter_secure_storage.dart';

/// Where credentials live.
///
/// An interface rather than direct use of the plugin: the platform keychain is
/// unavailable under `flutter test`, and a controller that could only be tested
/// on Windows would be a controller with no tests on CI.
abstract class TokenStore {
  Future<String?> read(String key);
  Future<void> write(String key, String value);
  Future<void> delete(String key);
}

/// Storage keys, in one place so a typo cannot silently create a second slot
/// that looks empty forever.
abstract class TokenKeys {
  const TokenKeys._();

  /// The Hub base URL chosen in the first-run setup wizard. Stored beside the
  /// credentials so a restarted client reconnects to the same Hub rather than
  /// silently falling back to the default address.
  static const String hubUrl = 'fluxspace.hub.url';

  /// The owner bearer token (ADR-0009 §2.1).
  static const String ownerToken = 'fluxspace.hub.owner_token';

  /// The owner id the token authenticates. Stored beside the token because the
  /// Hub derives the id from the token and the client must agree with it, not
  /// guess it.
  static const String ownerId = 'fluxspace.hub.owner_id';

  /// The companion id the client acts for, collected by the setup wizard. On an
  /// authenticated Hub the owner derives from the token, but the companion is
  /// still a piece of client configuration the rest of the app needs.
  static const String companionId = 'fluxspace.hub.companion_id';

  /// Per-seat stream token, issued once at join. Keyed by session so joining a
  /// second match cannot overwrite the first one's credential.
  static String seatToken(String sessionId) => 'fluxspace.hub.seat.$sessionId';
}

/// Platform keychain storage: Windows Credential Manager, macOS Keychain,
/// libsecret on Linux.
///
/// The plugin's default options are used on purpose. Pinning platform-specific
/// options here would tie this file to a plugin minor version and, more
/// importantly, would let a reader believe the defaults were chosen after
/// review when they were not: the defaults are what the platform recommends,
/// and anything narrower should be a deliberate, documented change.
class SecureTokenStore implements TokenStore {
  SecureTokenStore({FlutterSecureStorage? storage})
      : _storage = storage ?? const FlutterSecureStorage();

  final FlutterSecureStorage _storage;

  @override
  Future<String?> read(String key) => _storage.read(key: key);

  @override
  Future<void> write(String key, String value) => _storage.write(key: key, value: value);

  @override
  Future<void> delete(String key) => _storage.delete(key: key);
}

/// In-memory storage for tests and for the case where the user declined to
/// persist a token. Holds nothing on disk, and forgets everything on restart —
/// which is a feature when the alternative is a plaintext token on a shared
/// machine.
class MemoryTokenStore implements TokenStore {
  MemoryTokenStore([Map<String, String>? initial]) : _values = <String, String>{...?initial};

  final Map<String, String> _values;

  @override
  Future<String?> read(String key) async => _values[key];

  @override
  Future<void> write(String key, String value) async => _values[key] = value;

  @override
  Future<void> delete(String key) async => _values.remove(key);
}
