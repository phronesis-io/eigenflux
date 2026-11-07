/// The read-only HTTP surface behind the Flux Theatre observability panels.
///
/// This slice calls ONLY `GET` endpoints that already exist in the Hub. It
/// never calls a write/consume route (`authorize`, `budget/usage`, `consent`,
/// `publish`, approvals, reprise completion, ...) because doing so would change
/// state — consume a one-shot approval, book token spend, append an audit
/// entry — just to observe it. The endpoints that exist only as writes are
/// documented in `production/epics/flux-theatre/OBSERVABILITY_PANELS.md` under
/// "what has NO endpoint yet".
///
/// The caller supplies a [Dio] already configured with `baseUrl` and auth
/// headers (this class never sets a base URL or a token itself). For tests —
/// and for any caller that wants no network at all — a [PanelsFetcher] can be
/// injected instead; it is used verbatim and a thrown [PanelsException] is
/// propagated unchanged. `http_mock_adapter` is NOT a dependency of this
/// project, so the fetcher is the network-free seam.
library;

import 'dart:convert';

import 'package:dio/dio.dart';

import 'panels_models.dart';

/// The injectable network seam. Returns the decoded JSON object for [path].
///
/// The real implementation is built from a [Dio] in [PanelsApi.fromDio]. A
/// test implementation can return canned maps per path and throw a
/// [PanelsException] to simulate e.g. a 503 `GOVERNANCE_DEGRADED` response.
typedef PanelsFetcher = Future<Map<String, dynamic>> Function(
  String path, {
  Map<String, dynamic>? query,
});

/// Read-only access to the S4 archive and S5 governance endpoints.
class PanelsApi {
  /// Provide a configured [dio] (baseUrl + headers) for production, or a
  /// [fetcher] for tests/offline use. If [fetcher] is given it wins; otherwise
  /// a fetcher is built around [dio] (or a fresh [Dio] when both are absent).
  PanelsApi({Dio? dio, PanelsFetcher? fetcher})
      : _dio = dio,
        _fetcher = fetcher ?? _fromDio(dio ?? Dio());

  /// Convenience constructor when only a [Dio] is available.
  PanelsApi.fromDio(Dio dio) : this(dio: dio);

  final Dio? _dio;
  final PanelsFetcher _fetcher;

  /// The injected-or-derived fetcher, exposed so a host can log paths.
  PanelsFetcher get fetcher => _fetcher;

  /// The [Dio] backing this API, if one was supplied.
  Dio? get dio => _dio;

  // --- endpoint path constants (mirror hub/api/*.go) ----------------------

  static const String pathArchives = '/hub/archives';
  static const String pathAudit = '/hub/audit';
  static const String pathAuditVerify = '/hub/audit/verify';

  static String permissionsPath(String companionId) =>
      '/hub/companions/$companionId/permissions';

  static String budgetPath(String companionId) =>
      '/hub/companions/$companionId/budget';

  static String archivePath(String id) => '/hub/archives/$id';

  static String verifyArchivePath(String id) => '/hub/archives/$id/verify';

  static String viewPath(String id, String mode) =>
      '/hub/archives/$id/views/$mode';

  static String modelConsistencyPath(String id) =>
      '/hub/archives/$id/model-consistency';

  static String reprisesPath(String id) => '/hub/archives/$id/reprises';

  static String reprisePath(String id) => '/hub/reprises/$id';

  // --- granular typed reads (throw PanelsException on failure) ------------

  /// `GET /hub/companions/{id}/permissions`.
  Future<PermissionState> fetchPermissions(String companionId) async =>
      PermissionState.fromJson(await _fetch(permissionsPath(companionId)));

  /// `GET /hub/companions/{id}/budget`.
  Future<BudgetState> fetchBudget(String companionId) async =>
      BudgetState.fromJson(await _fetch(budgetPath(companionId)));

  /// `GET /hub/audit?companion_id=...`.
  Future<AuditState> fetchAudit(String companionId) async => AuditState.fromJson(
        await _fetch(
          pathAudit,
          query: companionId.isEmpty
              ? null
              : <String, dynamic>{'companion_id': companionId},
        ),
      );

  /// `GET /hub/audit/verify`. A 409 `AUDIT_CHAIN_BROKEN` is turned into a
  /// legitimate "chain broken" result rather than an exception.
  Future<AuditVerification> fetchAuditVerification() async {
    try {
      return AuditVerification.fromJson(await _fetch(pathAuditVerify));
    } on PanelsException catch (error) {
      if (error.status == 409 || error.code == 'AUDIT_CHAIN_BROKEN') {
        return AuditVerification.broken(error.message);
      }
      rethrow;
    }
  }

  /// `GET /hub/archives/{id}/model-consistency`.
  Future<ModelConsistencyState> fetchModelConsistency(String archiveId) async =>
      ModelConsistencyState.fromJson(await _fetch(modelConsistencyPath(archiveId)));

  /// `GET /hub/archives`.
  Future<List<ArchiveSummary>> fetchArchives() async {
    final Field<List<ArchiveSummary>> parsed = _listFromJson<ArchiveSummary>(
      await _fetch(pathArchives),
      'archives',
      ArchiveSummary.fromJson,
    );
    return parsed.value ?? const <ArchiveSummary>[];
  }

  /// `GET /hub/archives/{id}`.
  Future<ArchiveDetail> fetchArchive(String id) async =>
      ArchiveDetail.fromJson(await _fetch(archivePath(id)));

  /// `GET /hub/archives/{id}/verify`.
  Future<ArchiveIntegrityState> fetchVerifyArchive(String id) async =>
      ArchiveIntegrityState.fromJson(await _fetch(verifyArchivePath(id)));

  /// `GET /hub/archives/{id}/views/{mode}`.
  Future<ArchiveView> fetchView(
    String id,
    String mode, {
    Map<String, dynamic>? query,
  }) async =>
      ArchiveView.fromJson(await _fetch(viewPath(id, mode), query: query));

  /// `GET /hub/archives/{id}/reprises`.
  Future<List<Reprise>> fetchReprises(String archiveId) async {
    final Field<List<Reprise>> parsed = _listFromJson<Reprise>(
      await _fetch(reprisesPath(archiveId)),
      'reprises',
      Reprise.fromJson,
    );
    return parsed.value ?? const <Reprise>[];
  }

  /// `GET /hub/reprises/{id}`.
  Future<Reprise> fetchReprise(String id) async =>
      Reprise.fromJson(await _fetch(reprisePath(id)));

  // --- aggregate loads (never throw; capture per-section errors) -----------

  /// Loads every governance read for [companionId]. [archiveId] is optional and
  /// gates the archive-scoped model-consistency check; when null, that section
  /// is left null (rendered as "not recorded — no archive selected").
  Future<GovernanceData> loadGovernance({
    required String companionId,
    String? archiveId,
  }) async {
    final Section<BudgetState> budget =
        await _section<BudgetState>(() => fetchBudget(companionId));
    final Section<PermissionState> permissions =
        await _section<PermissionState>(() => fetchPermissions(companionId));
    final Section<AuditState> audit =
        await _section<AuditState>(() => fetchAudit(companionId));
    final Section<AuditVerification> verification =
        await _section<AuditVerification>(fetchAuditVerification);
    final Section<ModelConsistencyState>? modelConsistency =
        (archiveId != null && archiveId.isNotEmpty)
            ? await _section<ModelConsistencyState>(
                () => fetchModelConsistency(archiveId))
            : null;
    return GovernanceData(
      budget: budget,
      permissions: permissions,
      audit: audit,
      verification: verification,
      modelConsistency: modelConsistency,
    );
  }

  /// `GET /hub/archives` wrapped as a [Section] so a 503
  /// ARCHIVE_STORE_UNCONFIGURED is visible rather than an empty list.
  Future<Section<List<ArchiveSummary>>> loadArchiveList() =>
      _section<List<ArchiveSummary>>(fetchArchives);

  /// Loads detail, integrity, the selected view and reprises for one archive.
  Future<ArchiveBundle> loadArchiveBundle({
    required String archiveId,
    required String mode,
    Map<String, dynamic>? viewQuery,
  }) async {
    final Section<ArchiveDetail> detail =
        await _section<ArchiveDetail>(() => fetchArchive(archiveId));
    final Section<ArchiveIntegrityState> integrity =
        await _section<ArchiveIntegrityState>(() => fetchVerifyArchive(archiveId));
    final Section<ArchiveView> view = await _section<ArchiveView>(
      () => fetchView(archiveId, mode, query: viewQuery),
    );
    final Section<List<Reprise>> reprises =
        await _section<List<Reprise>>(() => fetchReprises(archiveId));
    return ArchiveBundle(
      detail: detail,
      integrity: integrity,
      view: view,
      reprises: reprises,
    );
  }

  // --- plumbing ------------------------------------------------------------

  Future<Map<String, dynamic>> _fetch(
    String path, {
    Map<String, dynamic>? query,
  }) async {
    try {
      return await _fetcher(path, query: query);
    } on PanelsException {
      rethrow;
    } catch (error) {
      throw PanelsException(status: 0, code: 'TRANSPORT', message: '$error');
    }
  }

  Future<Section<T>> _section<T>(Future<T> Function() body) async {
    try {
      return Section<T>.value(await body());
    } on PanelsException catch (error) {
      return Section<T>.failed(error);
    } catch (error) {
      return Section<T>.failed(
        PanelsException(status: 0, code: 'UNEXPECTED', message: '$error'),
      );
    }
  }

  /// Reuses the model's defensive list parser without exposing it publicly.
  Field<List<T>> _listFromJson<T>(
    Map<String, dynamic> json,
    String key,
    T Function(Map<String, dynamic>) from,
  ) {
    final Object? v = json[key];
    if (v == null) return Field<List<T>>.recorded(<T>[]);
    if (v is List) {
      return Field<List<T>>.recorded(<T>[
        for (final Object? e in v)
          if (e is Map<String, dynamic>) from(e),
      ]);
    }
    return Field<List<T>>.unknown();
  }
}

/// Builds a [PanelsFetcher] around a [Dio] instance.
///
/// Dio is configured with `validateStatus: (_) => true` so a 503 arrives as a
/// normal response and its `{"error":{"code":..}}` envelope can be turned into
/// a [PanelsException] carrying the stable domain code.
PanelsFetcher _fromDio(Dio dio) {
  return (String path, {Map<String, dynamic>? query}) async {
    try {
      final Response<dynamic> response = await dio.get<dynamic>(
        path,
        queryParameters: query,
        options: Options(validateStatus: (int? status) => true),
      );
      final int status = response.statusCode ?? 0;
      final Object? raw = response.data;
      final Map<String, dynamic> body = _asMap(raw);
      if (status >= 200 && status < 300) {
        return body;
      }
      String code = 'HTTP_$status';
      String message = raw is String ? raw : '$raw';
      final Object? error = body['error'];
      if (error is Map) {
        code = '${error['code'] ?? code}';
        message = '${error['message'] ?? message}';
      }
      throw PanelsException(status: status, code: code, message: message);
    } on PanelsException {
      rethrow;
    } catch (error) {
      throw PanelsException(status: 0, code: 'TRANSPORT', message: '$error');
    }
  };
}

Map<String, dynamic> _asMap(Object? raw) {
  if (raw is Map<String, dynamic>) return raw;
  if (raw is Map) return Map<String, dynamic>.from(raw);
  if (raw is String) {
    try {
      final Object? decoded = jsonDecode(raw);
      if (decoded is Map) return Map<String, dynamic>.from(decoded);
    } catch (_) {
      // Not JSON: leave the body empty; the error path still reports status.
    }
  }
  return <String, dynamic>{};
}
