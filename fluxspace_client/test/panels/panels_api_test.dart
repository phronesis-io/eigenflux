/// PanelsApi tests: exact paths, defensive parsing, degraded state, empty
/// lists, and the Dio error-envelope -> PanelsException mapping.
///
/// All tests are network-free: they inject a [PanelsFetcher], or (for the one
/// envelope test) a Dio backed by an in-memory HttpClientAdapter.
library;

import 'dart:convert';
import 'dart:typed_data';

import 'package:dio/dio.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:fluxspace_client/panels/panels_api.dart';
import 'package:fluxspace_client/panels/panels_models.dart';

import 'support/panels_fakes.dart';

/// A minimal Dio adapter that answers from a script, no socket involved.
class _Responder implements HttpClientAdapter {
  _Responder(this.body, this.status);

  final String body;
  final int status;

  @override
  Future<ResponseBody> fetch(
    RequestOptions options,
    Stream<Uint8List>? requestStream,
    Future<void>? cancelFuture,
  ) async =>
      ResponseBody.fromString(
        body,
        status,
        headers: <String, List<String>>{
          Headers.contentTypeHeader: <String>['application/json'],
        },
      );

  @override
  void close({bool force = false}) {}
}

void main() {
  group('endpoint paths (exact, transcribed from hub/api/*.go)', () {
    test('every read uses the registered path', () async {
      final ScriptedFetcher fetcher = ScriptedFetcher();
      final PanelsApi api = fetcher.api();
      await api.fetchPermissions('comp_alpha');
      await api.fetchBudget('comp_alpha');
      await api.fetchAudit('comp_alpha');
      await api.fetchAuditVerification();
      await api.fetchModelConsistency('replay_ab12');
      await api.fetchArchives();
      await api.fetchArchive('replay_ab12');
      await api.fetchVerifyArchive('replay_ab12');
      await api.fetchView('replay_ab12', ArchiveModes.full);
      await api.fetchReprises('replay_ab12');
      await api.fetchReprise('reprise_1');

      expect(fetcher.paths, <String>[
        '/hub/companions/comp_alpha/permissions',
        '/hub/companions/comp_alpha/budget',
        '/hub/audit',
        '/hub/audit/verify',
        '/hub/archives/replay_ab12/model-consistency',
        '/hub/archives',
        '/hub/archives/replay_ab12',
        '/hub/archives/replay_ab12/verify',
        '/hub/archives/replay_ab12/views/full',
        '/hub/archives/replay_ab12/reprises',
        '/hub/reprises/reprise_1',
      ]);
    });

    test('optional query parameters are passed through', () async {
      final ScriptedFetcher fetcher = ScriptedFetcher();
      final PanelsApi api = fetcher.api();
      await api.fetchAudit('comp_alpha');
      await api.fetchView('replay_ab12', ArchiveModes.accelerated,
          query: <String, dynamic>{'speed': 2});

      expect(fetcher.queries[0], <String, dynamic>{'companion_id': 'comp_alpha'});
      expect(fetcher.queries[1], <String, dynamic>{'speed': 2});
    });

    test('an empty companion id omits the audit query parameter', () async {
      final ScriptedFetcher fetcher = ScriptedFetcher();
      await fetcher.api().fetchAudit('');
      expect(fetcher.queries.single, isNull);
    });
  });

  group('typed parsing through the API', () {
    test('budget / permissions / audit / model parse to recorded values', () async {
      final PanelsApi api = ScriptedFetcher().api();
      expect((await api.fetchBudget('comp_alpha')).monthlyTokens.value, 200000);
      expect((await api.fetchPermissions('comp_alpha')).tier.value,
          PermissionTiers.restrictedWrite);
      expect((await api.fetchAudit('comp_alpha')).denied.length, 1);
      expect((await api.fetchModelConsistency('replay_ab12')).code.value,
          ModelCodes.consistent);
    });

    test('a response with missing keys leaves fields unknown', () async {
      final PanelsApi api = PanelsApi(
        fetcher: (String path, {Map<String, dynamic>? query}) async =>
            <String, dynamic>{'companion_id': 'comp_alpha'},
      );
      final BudgetState budget = await api.fetchBudget('comp_alpha');
      expect(budget.monthlyTokens.describe(), panelsNotRecorded);
      expect(budget.downgrade.describe(), panelsNotRecorded);
    });

    test('empty lists are returned as real empty lists', () async {
      final PanelsApi api = PanelsApi(
        fetcher: (String path, {Map<String, dynamic>? query}) async =>
            <String, dynamic>{'archives': <dynamic>[]},
      );
      expect(await api.fetchArchives(), isEmpty);
      final Section<List<ArchiveSummary>> section = await api.loadArchiveList();
      expect(section.isOk, isTrue);
      expect(section.value, isEmpty);
    });
  });

  group('GOVERNANCE_DEGRADED (503) is an explicit state, not an empty list', () {
    test('loadGovernance surfaces a degraded flag and failed sections', () async {
      final ScriptedFetcher fetcher = ScriptedFetcher(
        degradedContains: <String>{'/hub/companions/', PanelsApi.pathAudit},
      );
      final GovernanceData data = await fetcher.api().loadGovernance(
            companionId: 'comp_alpha',
            archiveId: 'replay_ab12',
          );

      expect(data.isDegraded, isTrue);
      expect(data.budget.isOk, isFalse);
      expect(data.budget.isDegraded, isTrue);
      expect(data.budget.error!.code, 'GOVERNANCE_DEGRADED');
      expect(data.budget.error!.isGovernanceDegraded, isTrue);
      expect(data.budget.error!.isDegraded, isTrue);
      expect(data.audit.isOk, isFalse); // NOT an empty AuditState
      expect(data.verification.isDegraded, isTrue);

      // Model-consistency has no governance-state dependency in the Hub, so
      // it still succeeds and is rendered even while the surface is degraded.
      expect(data.modelConsistency, isNotNull);
      expect(data.modelConsistency!.isOk, isTrue);
      expect(data.modelConsistency!.value!.code.value, ModelCodes.consistent);
    });

    test('a granular read propagates the 503 as PanelsException', () async {
      final ScriptedFetcher fetcher = ScriptedFetcher(
        degradedContains: <String>{'/hub/companions/'},
      );
      await expectLater(
        fetcher.api().fetchBudget('comp_alpha'),
        throwsA(isA<PanelsException>().having(
          (PanelsException e) => e.code,
          'code',
          'GOVERNANCE_DEGRADED',
        )),
      );
    });

    test('the Hub 503 error envelope maps to the domain code', () async {
      final Dio dio = Dio()
        ..httpClientAdapter = _Responder(
          jsonEncode(<String, dynamic>{
            'error': <String, dynamic>{
              'code': 'GOVERNANCE_DEGRADED',
              'message': 'governance stopped after a decision could not be recorded',
            },
          }),
          503,
        );
      final PanelsApi api = PanelsApi.fromDio(dio);
      await expectLater(
        api.fetchBudget('comp_alpha'),
        throwsA(isA<PanelsException>()
            .having((PanelsException e) => e.status, 'status', 503)
            .having((PanelsException e) => e.code, 'code', 'GOVERNANCE_DEGRADED')
            .having((PanelsException e) => e.isDegraded, 'isDegraded', isTrue)),
      );
    });

    test('a 200 body still parses through the Dio adapter', () async {
      final Dio dio = Dio()
        ..httpClientAdapter = _Responder(jsonEncode(budgetJson), 200);
      final BudgetState budget =
          await PanelsApi.fromDio(dio).fetchBudget('comp_alpha');
      expect(budget.monthlyTokens.value, 200000);
      expect(budget.downgrade.isUnknown, isTrue);
    });
  });

  group('audit verification', () {
    test('a 409 AUDIT_CHAIN_BROKEN becomes a "broken" result', () async {
      final PanelsApi api = PanelsApi(
        fetcher: (String path, {Map<String, dynamic>? query}) async {
          if (path == PanelsApi.pathAuditVerify) {
            throw const PanelsException(
                status: 409, code: 'AUDIT_CHAIN_BROKEN', message: 'entry 3 hash mismatch');
          }
          return <String, dynamic>{};
        },
      );
      final AuditVerification verification = await api.fetchAuditVerification();
      expect(verification.broken, isTrue);
      expect(verification.verified.value, false);
    });

    test('a verified chain parses verified=true', () async {
      final AuditVerification verification =
          await ScriptedFetcher().api().fetchAuditVerification();
      expect(verification.verified.value, true);
      expect(verification.entries.value, 2);
    });
  });

  group('archive aggregate loads', () {
    test('loadArchiveBundle returns ok sections with detail + view + reprises',
        () async {
      final ArchiveBundle bundle = await ScriptedFetcher().api().loadArchiveBundle(
            archiveId: 'replay_ab12',
            mode: ArchiveModes.full,
          );
      expect(bundle.isDegraded, isFalse);
      expect(bundle.detail.isOk, isTrue);
      expect(bundle.detail.value!.integrity.missingFields.value!.length, 2);
      expect(bundle.view.isOk, isTrue);
      expect(bundle.view.value!.events.value!.length, 2);
      expect(bundle.reprises.value!.first.comparison!.divergencePoint.value, 3);
    });

    test('same_spec_comparison view carries the divergence point', () async {
      final ArchiveBundle bundle = await ScriptedFetcher().api().loadArchiveBundle(
            archiveId: 'replay_ab12',
            mode: ArchiveModes.sameSpecComparison,
            viewQuery: <String, dynamic>{'compare_with': 'replay_cd34'},
          );
      expect(bundle.view.value!.comparison, isNotNull);
      expect(divergenceText(bundle.view.value!.comparison!), '3');
    });

    test('a degraded archive store is surfaced by loadArchiveList', () async {
      final PanelsApi api = PanelsApi(
        fetcher: (String path, {Map<String, dynamic>? query}) async =>
            throw const PanelsException(
              status: 503,
              code: 'ARCHIVE_STORE_UNCONFIGURED',
              message: 'archive store is not configured',
            ),
      );
      final Section<List<ArchiveSummary>> section = await api.loadArchiveList();
      expect(section.isOk, isFalse);
      expect(section.isDegraded, isTrue);
      expect(section.error!.code, 'ARCHIVE_STORE_UNCONFIGURED');
    });
  });
}
