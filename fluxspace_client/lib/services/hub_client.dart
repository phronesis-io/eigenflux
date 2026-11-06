import 'package:dio/dio.dart';

/// Thin client for the FluxSpace Hub. The Hub is a game-engine service; all
/// EigenFlux identity/social traffic goes through a separate adapter.
class HubClient {
  HubClient({String? baseUrl, Dio? dio})
      : baseUrl = baseUrl ?? defaultBaseUrl,
        _dio = dio ??
            Dio(BaseOptions(
              baseUrl: baseUrl ?? defaultBaseUrl,
              connectTimeout: const Duration(seconds: 3),
              receiveTimeout: const Duration(seconds: 3),
            ));

  /// 8095 avoids EigenFlux gateway 8080, ws 8088, console 8090 and the
  /// existing offline replay service 8092.
  static const String defaultBaseUrl = 'http://127.0.0.1:8095';

  final String baseUrl;
  final Dio _dio;

  /// Story 001 AC5/6: the client must be able to reach GET /health.
  Future<bool> health() async {
    final Response<dynamic> response = await _dio.get<dynamic>('/health');
    return response.statusCode == 200;
  }
}
