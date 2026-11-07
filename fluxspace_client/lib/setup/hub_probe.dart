import 'package:dio/dio.dart';

/// The Hub's default address (services/hub_client.dart, HubClient.defaultBaseUrl:
/// 8095 keeps clear of the EigenFlux gateway 8080, ws 8088, console 8090 and
/// the offline replay service 8092). Kept as a local constant so the setup
/// slice does not depend on a file another workstream owns.
const String kDefaultHubUrl = 'http://127.0.0.1:8095';

/// The `auth` field of `GET /health` (hub/api/health.go): "enabled" when the
/// process was configured with owner tokens, "disabled" for an explicitly open
/// Hub. [unknown] is used when the field is missing or unrecognised so a check
/// can never silently assume "no auth" (ADR-0009 section 2.1).
enum HubAuthMode { enabled, disabled, unknown }

/// The parsed result of one `GET /health` probe.
///
/// The real response is:
/// ```json
/// {"status":"ok","service":"fluxspace-hub","auth":"enabled"}
/// ```
/// [reachable] means an HTTP response was received (even a non-200 one); a
/// transport failure is [HubHealth.transport]. [checkSucceeded] is the honest
/// gate: a 200 with `status: ok` is not enough if the auth mode cannot be
/// determined, because then the wizard cannot truthfully tell the human
/// whether a bearer token is required.
class HubHealth {
  HubHealth({
    required this.reachable,
    required this.statusCode,
    required this.status,
    required this.service,
    required this.auth,
    this.error,
  });

  factory HubHealth.fromResponse(int statusCode, Map<String, dynamic> json) {
    final Object? rawStatus = json['status'];
    final Object? rawService = json['service'];
    final Object? rawAuth = json['auth'];
    return HubHealth(
      reachable: true,
      statusCode: statusCode,
      status: rawStatus is String ? rawStatus : '',
      service: rawService is String ? rawService : '',
      auth: _parseAuth(rawAuth is String ? rawAuth : ''),
    );
  }

  factory HubHealth.transport(Object error) => HubHealth(
        reachable: false,
        statusCode: 0,
        status: '',
        service: '',
        auth: HubAuthMode.unknown,
        error: error.toString(),
      );

  final bool reachable;
  final int statusCode;
  final String status;
  final String service;
  final HubAuthMode auth;
  final String? error;

  static HubAuthMode _parseAuth(String raw) => switch (raw) {
        'enabled' => HubAuthMode.enabled,
        'disabled' => HubAuthMode.disabled,
        _ => HubAuthMode.unknown,
      };

  /// A human-readable reason the check did not succeed, or null on success.
  String? get failureMessage {
    if (!reachable) {
      final String detail = (error == null || error!.isEmpty) ? '' : ' ($error)';
      return 'Could not reach the Hub at this address. Check the URL and that '
          'the Hub process is running.$detail';
    }
    if (statusCode != 200) {
      return 'The Hub answered HTTP $statusCode instead of 200 for GET /health.';
    }
    if (status != 'ok') {
      return 'GET /health did not report status "ok" (got '
          '"${status.isEmpty ? 'missing' : status}"). This may not be the FluxSpace Hub.';
    }
    if (auth == HubAuthMode.unknown) {
      return 'GET /health did not report a recognised auth mode (expected '
          '"enabled" or "disabled"). Cannot tell whether a bearer token is '
          'required, so this is not treated as a successful check.';
    }
    return null;
  }

  /// Never true when a check failed: reachability, status and a recognised
  /// auth mode must all hold.
  bool get checkSucceeded => failureMessage == null;

  String get authLabel => switch (auth) {
        HubAuthMode.enabled => 'enabled',
        HubAuthMode.disabled => 'disabled',
        HubAuthMode.unknown => 'unknown',
      };
}

/// The outcome of the optional companion binding check
/// (`POST /hub/companions`, hub/api/companions.go).
class BindingCheck {
  const BindingCheck._({required this.ok, this.code, this.message});

  const BindingCheck.success() : this._(ok: true);

  const BindingCheck.failure({required String code, required String message})
      : this._(ok: false, code: code, message: message);

  final bool ok;
  final String? code;
  final String? message;
}

/// The network seam of the setup wizard. Injected into [SetupState] so tests
/// never open a socket; [DioHubProbe] is the only production implementation.
abstract class HubProbe {
  /// `GET /health`. Exempt from authentication (hub/api/auth.go publicExempt),
  /// so it works before a token exists and reports the auth mode.
  Future<HubHealth> health(String baseUrl, {String? ownerToken});

  /// `POST /hub/companions` with `{companion_id}`, idempotent per ADR-0009
  /// section 2.2. It is auth-gated when enabled, so it also exercises the
  /// bearer token end to end. On an unauthenticated Hub the endpoint answers
  /// 401 AUTH_REQUIRED because there is no owner to bind to.
  Future<BindingCheck> bindCompanion({
    required String baseUrl,
    required String ownerToken,
    required String companionId,
  });
}

/// Production probe. Every public method catches its own transport error and
/// returns a value, so the wizard shows an inline message instead of throwing.
class DioHubProbe implements HubProbe {
  DioHubProbe({Dio? dio})
      : _dio = dio ??
            Dio(BaseOptions(
              connectTimeout: const Duration(seconds: 3),
              receiveTimeout: const Duration(seconds: 5),
              sendTimeout: const Duration(seconds: 3),
            ));

  final Dio _dio;

  @override
  Future<HubHealth> health(String baseUrl, {String? ownerToken}) async {
    try {
      final Response<dynamic> response = await _dio.get<dynamic>(
        _uri(baseUrl, '/health'),
        options: Options(
          // Inspect every status ourselves instead of letting Dio throw: a 4xx
          // still means the process answered, which is information.
          validateStatus: (_) => true,
          headers: <String, dynamic>{
            if (ownerToken != null && ownerToken.isNotEmpty)
              'Authorization': 'Bearer $ownerToken',
          },
        ),
      );
      return HubHealth.fromResponse(
        response.statusCode ?? 0,
        _map(response.data),
      );
    } catch (error) {
      return HubHealth.transport(error);
    }
  }

  @override
  Future<BindingCheck> bindCompanion({
    required String baseUrl,
    required String ownerToken,
    required String companionId,
  }) async {
    try {
      final Response<dynamic> response = await _dio.post<dynamic>(
        _uri(baseUrl, '/hub/companions'),
        data: <String, dynamic>{'companion_id': companionId},
        options: Options(
          validateStatus: (_) => true,
          headers: <String, dynamic>{
            if (ownerToken.isNotEmpty) 'Authorization': 'Bearer $ownerToken',
          },
        ),
      );
      final int status = response.statusCode ?? 0;
      if (status >= 200 && status < 300) {
        return const BindingCheck.success();
      }
      final Map<String, dynamic> body = _map(response.data);
      final Object? envelope = body['error'];
      if (envelope is Map) {
        return BindingCheck.failure(
          code: '${envelope['code'] ?? 'HTTP_$status'}',
          message: '${envelope['message'] ?? 'the Hub refused the companion binding (HTTP $status)'}',
        );
      }
      return BindingCheck.failure(
        code: 'HTTP_$status',
        message: 'the Hub refused the companion binding (HTTP $status)',
      );
    } catch (error) {
      return BindingCheck.failure(code: 'TRANSPORT', message: error.toString());
    }
  }

  static String _uri(String baseUrl, String path) =>
      '${baseUrl.trim().replaceAll(RegExp(r'/+$'), '')}$path';

  static Map<String, dynamic> _map(Object? data) {
    if (data is Map<String, dynamic>) return data;
    if (data is Map) {
      return <String, dynamic>{
        for (final Object? key in data.keys) '$key': data[key],
      };
    }
    return <String, dynamic>{};
  }
}
