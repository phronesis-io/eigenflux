import 'package:fluxspace_client/setup/hub_probe.dart';
import 'package:fluxspace_client/setup/setup_state.dart';
import 'package:fluxspace_client/services/token_store.dart';

/// The real `GET /health` payload from hub/api/health.go.
Map<String, dynamic> healthJson(String auth) => <String, dynamic>{
      'status': 'ok',
      'service': 'fluxspace-hub',
      'auth': auth,
    };

HubHealth healthEnabled() => HubHealth.fromResponse(200, healthJson('enabled'));
HubHealth healthDisabled() => HubHealth.fromResponse(200, healthJson('disabled'));

/// A scripted [HubProbe]. No socket is ever opened; it records how it was
/// called so tests can assert that navigation never hides a network call.
class FakeHubProbe implements HubProbe {
  FakeHubProbe({HubHealth? health, BindingCheck? binding})
      : _health = health,
        _binding = binding;

  HubHealth? _health;
  BindingCheck? _binding;

  int healthCalls = 0;
  int bindCalls = 0;
  String? lastHealthUrl;
  String? lastBindToken;
  String? lastBindCompanion;

  void scriptHealth(HubHealth value) => _health = value;
  void scriptBinding(BindingCheck value) => _binding = value;

  @override
  Future<HubHealth> health(String baseUrl, {String? ownerToken}) async {
    healthCalls++;
    lastHealthUrl = baseUrl;
    return _health ?? HubHealth.transport('no scripted health response');
  }

  @override
  Future<BindingCheck> bindCompanion({
    required String baseUrl,
    required String ownerToken,
    required String companionId,
  }) async {
    bindCalls++;
    lastBindToken = ownerToken;
    lastBindCompanion = companionId;
    return _binding ??
        const BindingCheck.failure(
          code: 'NO_SCRIPT',
          message: 'no scripted binding response',
        );
  }
}

SetupState makeState({
  required FakeHubProbe probe,
  TokenStore? store,
  String url = 'http://127.0.0.1:8095',
}) {
  return SetupState(probe: probe, store: store ?? MemoryTokenStore(), initialHubUrl: url);
}
