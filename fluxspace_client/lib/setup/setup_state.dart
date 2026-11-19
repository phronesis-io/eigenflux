import 'package:flutter/foundation.dart';

import '../services/token_store.dart';
import 'hub_probe.dart';

/// The wizard's steps, in order. [ownerToken] only appears in the live
/// [SetupState.steps] sequence when the Hub reports auth enabled.
enum SetupStep {
  hubUrl,
  connection,
  ownerToken,
  identity,
  binding,
  save,
  done,
}

/// The values handed back to the app on completion. Also what the wizard
/// returns from `Navigator.pop`.
class SetupResult {
  const SetupResult({
    required this.hubUrl,
    required this.ownerToken,
    required this.ownerId,
    required this.companionId,
    required this.auth,
    required this.bindingChecked,
    required this.bindingOk,
  });

  final String hubUrl;
  final String ownerToken;
  final String ownerId;
  final String companionId;
  final HubAuthMode auth;
  final bool bindingChecked;
  final bool bindingOk;

  String get authLabel => switch (auth) {
        HubAuthMode.enabled => 'enabled',
        HubAuthMode.disabled => 'disabled',
        HubAuthMode.unknown => 'unknown',
      };

  /// True only when a binding check ran AND succeeded. A failed or skipped
  /// check is never reported as verified.
  bool get bindingVerified => bindingChecked && bindingOk;
}

/// A small, testable first-run state machine.
///
/// Both side effects are injected: [probe] (network) and [store] (persistence).
/// The only methods that touch the network are [checkConnection] and
/// [checkBinding], and they are called by explicit button presses, never from
/// navigation or from the constructor. [save] writes local storage only.
///
/// Honesty invariant: [connectionSucceeded] requires a `GET /health` 200 with
/// `status: ok` and a recognised `auth` field. No step advance and no save can
/// succeed while that check failed, so a failed check is never presented as a
/// completed setup.
class SetupState extends ChangeNotifier {
  SetupState({
    required this.probe,
    required this.store,
    String initialHubUrl = kDefaultHubUrl,
  }) : _hubUrl = initialHubUrl;

  final HubProbe probe;
  final TokenStore store;

  String _hubUrl;
  String _ownerToken = '';
  String _ownerId = '';
  String _companionId = '';

  SetupStep _step = SetupStep.hubUrl;
  String? _error;

  HubHealth? _health;
  bool _checking = false;

  BindingCheck? _binding;
  bool _bindingChecked = false;
  bool _bindingBusy = false;

  bool _saving = false;
  SetupResult? _result;

  // --- read-only state -----------------------------------------------------

  String get hubUrl => _hubUrl;
  String get ownerToken => _ownerToken;
  String get ownerId => _ownerId;
  String get companionId => _companionId;
  SetupStep get step => _step;
  String? get error => _error;
  HubHealth? get health => _health;
  bool get checking => _checking;
  BindingCheck? get binding => _binding;
  bool get bindingChecked => _bindingChecked;
  bool get bindingBusy => _bindingBusy;
  bool get saving => _saving;
  SetupResult? get result => _result;
  bool get busy => _checking || _bindingBusy || _saving;

  bool get connectionSucceeded => _health?.checkSucceeded ?? false;

  /// The owner-token step exists only when the Hub reported auth enabled.
  bool get requiresToken =>
      connectionSucceeded && _health!.auth == HubAuthMode.enabled;

  bool get bindingVerified => _bindingChecked && (_binding?.ok ?? false);

  bool get isFirst => _step == steps.first;
  bool get isLast => _step == steps.last;

  List<SetupStep> get steps => <SetupStep>[
        SetupStep.hubUrl,
        SetupStep.connection,
        if (requiresToken) SetupStep.ownerToken,
        SetupStep.identity,
        SetupStep.binding,
        SetupStep.save,
        SetupStep.done,
      ];

  int get stepIndex {
    final int i = steps.indexOf(_step);
    return i < 0 ? 0 : i;
  }

  int get stepCount => steps.length;

  // --- field setters -------------------------------------------------------
  //
  // Pure storage: they deliberately do not call notifyListeners, so a focused
  // TextField is not rebuilt on every keystroke. All UI refresh comes from the
  // explicit transitions and actions below. Editing the URL invalidates the
  // last connection result; editing the companion id invalidates the last
  // binding result, so a stale "verified" state cannot survive an edit.

  void setHubUrl(String value) {
    if (value.trim() != _hubUrl.trim()) {
      _health = null;
      _binding = null;
      _bindingChecked = false;
      _result = null;
    }
    _hubUrl = value;
  }

  void setOwnerToken(String value) => _ownerToken = value;

  void setOwnerId(String value) => _ownerId = value;

  void setCompanionId(String value) {
    if (value.trim() != _companionId.trim()) {
      _binding = null;
      _bindingChecked = false;
    }
    _companionId = value;
  }

  // --- validation (pure) ---------------------------------------------------

  String? validateUrl(String value) {
    final String v = value.trim();
    if (v.isEmpty) return 'Enter the Hub URL to continue.';
    final Uri? uri = Uri.tryParse(v);
    if (uri == null ||
        !uri.hasScheme ||
        uri.host.isEmpty ||
        (uri.scheme != 'http' && uri.scheme != 'https')) {
      return 'Enter a valid http(s) URL, e.g. http://127.0.0.1:8095.';
    }
    return null;
  }

  String? validateToken(String value) => value.trim().isEmpty
      ? 'This Hub requires an owner bearer token. Paste one from HUB_OWNER_TOKENS.'
      : null;

  String? validateOwnerId(String value) =>
      value.trim().isEmpty ? 'Enter your owner id.' : null;

  String? validateCompanionId(String value) =>
      value.trim().isEmpty ? 'Enter your companion id.' : null;

  // --- explicit network actions (the only network calls in this class) -----

  Future<void> checkConnection() async {
    if (busy) return;
    final String? urlError = validateUrl(_hubUrl);
    if (urlError != null) {
      _setError(urlError);
      return;
    }
    _checking = true;
    _error = null;
    _binding = null;
    _bindingChecked = false;
    notifyListeners();
    final HubHealth checked = await probe.health(_hubUrl.trim());
    _health = checked;
    _checking = false;
    if (!checked.checkSucceeded) {
      _setError(checked.failureMessage ?? 'The Hub connection check did not succeed.');
      return;
    }
    _error = null;
    notifyListeners();
  }

  Future<void> checkBinding() async {
    if (busy) return;
    if (!connectionSucceeded) {
      _setError('A successful connection check is required before verifying a companion binding.');
      return;
    }
    final String? idError = validateCompanionId(_companionId);
    if (idError != null) {
      _setError(idError);
      return;
    }
    _bindingBusy = true;
    _error = null;
    notifyListeners();
    final BindingCheck check = await probe.bindCompanion(
      baseUrl: _hubUrl.trim(),
      ownerToken: _ownerToken.trim(),
      companionId: _companionId.trim(),
    );
    _binding = check;
    _bindingChecked = true;
    _bindingBusy = false;
    if (!check.ok) {
      _setError('Companion binding failed (${check.code}): ${check.message}');
      return;
    }
    _error = null;
    notifyListeners();
  }

  // --- transitions ---------------------------------------------------------

  /// Validates the current step and advances one step, or stays put with an
  /// inline [error]. On [SetupStep.save] it performs [save]. Returns whether
  /// the step changed.
  Future<bool> next() async {
    if (busy) return false;
    switch (_step) {
      case SetupStep.hubUrl:
        final String? e = validateUrl(_hubUrl);
        if (e != null) {
          _setError(e);
          return false;
        }
        break;
      case SetupStep.connection:
        if (!connectionSucceeded) {
          _setError('Run the connection check and wait for it to succeed before continuing.');
          return false;
        }
        break;
      case SetupStep.ownerToken:
        final String? e = validateToken(_ownerToken);
        if (e != null) {
          _setError(e);
          return false;
        }
        break;
      case SetupStep.identity:
        final String? e = validateOwnerId(_ownerId) ?? validateCompanionId(_companionId);
        if (e != null) {
          _setError(e);
          return false;
        }
        break;
      case SetupStep.binding:
        // Optional. Skipping never claims a binding succeeded; the result
        // records bindingChecked / bindingOk honestly.
        break;
      case SetupStep.save:
        return await save() != null;
      case SetupStep.done:
        return false;
    }
    _goto(_following(_step));
    return true;
  }

  bool back() {
    if (busy) return false;
    final List<SetupStep> sequence = steps;
    final int i = sequence.indexOf(_step);
    if (i <= 0) return false;
    _error = null;
    _goto(sequence[i - 1]);
    return true;
  }

  /// Persists the four values and moves to [SetupStep.done]. Refuses to write
  /// anything unless the connection check succeeded and every required field is
  /// valid, and surfaces storage failures (e.g. an unavailable platform
  /// keychain) as an inline error rather than claiming success.
  Future<SetupResult?> save() async {
    if (busy) return null;
    final String? urlError = validateUrl(_hubUrl);
    if (urlError != null) {
      _setError(urlError);
      return null;
    }
    if (!connectionSucceeded) {
      _setError('A successful connection check is required before saving.');
      return null;
    }
    if (requiresToken) {
      final String? tokenError = validateToken(_ownerToken);
      if (tokenError != null) {
        _setError(tokenError);
        return null;
      }
    }
    final String? idError = validateOwnerId(_ownerId) ?? validateCompanionId(_companionId);
    if (idError != null) {
      _setError(idError);
      return null;
    }

    _saving = true;
    _error = null;
    notifyListeners();
    try {
      await store.write(TokenKeys.hubUrl, _hubUrl.trim());
      await store.write(TokenKeys.ownerToken, _ownerToken.trim());
      await store.write(TokenKeys.ownerId, _ownerId.trim());
      await store.write(TokenKeys.companionId, _companionId.trim());
    } catch (error) {
      _saving = false;
      _setError('Could not save the Hub configuration: $error');
      return null;
    }
    _saving = false;
    _result = SetupResult(
      hubUrl: _hubUrl.trim(),
      ownerToken: _ownerToken.trim(),
      ownerId: _ownerId.trim(),
      companionId: _companionId.trim(),
      auth: _health!.auth,
      bindingChecked: _bindingChecked,
      bindingOk: _binding?.ok ?? false,
    );
    _goto(SetupStep.done);
    return _result;
  }

  // --- plumbing ------------------------------------------------------------

  SetupStep _following(SetupStep current) {
    final List<SetupStep> sequence = steps;
    final int i = sequence.indexOf(current);
    if (i < 0 || i + 1 >= sequence.length) return SetupStep.identity;
    return sequence[i + 1];
  }

  void _goto(SetupStep step) {
    _step = step;
    _error = null;
    notifyListeners();
  }

  void _setError(String message) {
    _error = message;
    notifyListeners();
  }
}
