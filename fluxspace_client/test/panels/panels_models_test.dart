/// Model-parsing tests: missing/unknown fields stay UNKNOWN -> "not recorded".
library;

import 'package:flutter_test/flutter_test.dart';
import 'package:fluxspace_client/panels/panels_api.dart';
import 'package:fluxspace_client/panels/panels_models.dart';

import 'support/panels_fakes.dart';

void main() {
  group('Field tri-state', () {
    test('a recorded value is displayed, an unknown value is "not recorded"', () {
      expect(const Field<int>.unknown().describe(), panelsNotRecorded);
      expect(const Field<int>.unknown().isUnknown, isTrue);
      expect(const Field<int>.recorded(0).describe(), '0');
      expect(const Field<bool>.recorded(false).describe(), 'false');
      expect(const Field<String>.recorded('rule_engine').describe(), 'rule_engine');
    });

    test('map() renders a recorded value and never fabricates an unknown one', () {
      expect(const Field<int>.recorded(7).map((int v) => 'v=$v'), 'v=7');
      expect(const Field<int>.unknown().map((int v) => 'v=$v'), panelsNotRecorded);
    });
  });

  group('BudgetState', () {
    test('parses limit / spent / remaining from the real GET /budget shape', () {
      final BudgetState budget = BudgetState.fromJson(budgetJson);
      expect(budget.companionId, 'comp_alpha');
      expect(budget.month, '2026-11');
      expect(budget.monthlyTokens.value, 200000);
      expect(budget.spentTokens.value, 1200);
      expect(budget.remainingTokens.value, 198800);
      expect(budget.configured.value, true);
    });

    test('GET /budget has NO downgrade field -> stays "not recorded"', () {
      // The invariant: never fabricate downgrade:"rule_engine" on a read.
      expect(budgetJson.containsKey('downgrade'), isFalse);
      final BudgetState budget = BudgetState.fromJson(budgetJson);
      expect(budget.downgrade.isUnknown, isTrue);
      expect(budget.downgrade.describe(), panelsNotRecorded);
    });

    test('missing numeric/bool keys stay unknown, not 0 / false', () {
      final BudgetState budget = BudgetState.fromJson(<String, dynamic>{});
      expect(budget.monthlyTokens.isUnknown, isTrue);
      expect(budget.spentTokens.describe(), panelsNotRecorded);
      expect(budget.remainingTokens.describe(), panelsNotRecorded);
      expect(budget.configured.isUnknown, isTrue); // NOT false
    });

    test('a wrong-typed value is defensive, not fabricated', () {
      final BudgetState budget = BudgetState.fromJson(<String, dynamic>{
        'monthly_tokens': 'not-a-number',
        'spent_tokens': <dynamic>[1],
      });
      expect(budget.monthlyTokens.isUnknown, isTrue);
      expect(budget.spentTokens.isUnknown, isTrue);
    });
  });

  group('PermissionState', () {
    test('parses tier and revoked', () {
      final PermissionState permission = PermissionState.fromJson(permissionsJson);
      expect(permission.tier.value, PermissionTiers.restrictedWrite);
      expect(permission.revoked.value, false);
      expect(permission.isHighImpact, isFalse);
    });

    test('an unconfigured companion (empty body) has an unknown tier', () {
      final PermissionState permission = PermissionState.fromJson(<String, dynamic>{});
      expect(permission.tier.isUnknown, isTrue);
      expect(permission.tier.describe(), panelsNotRecorded);
    });
  });

  group('AuditState / AuditEntry', () {
    test('denied operations come ONLY from recorded denials', () {
      final AuditState audit = AuditState.fromJson(auditJson);
      expect(audit.count.value, 2);
      expect(audit.list.length, 2);
      expect(audit.denied.length, 1);
      expect(audit.denied.first.operation, 'export_contacts');
      expect(audit.denied.first.isDenied, isTrue);
    });

    test('an absent outcome is unknown, NOT a denial or an allowance', () {
      final AuditEntry entry = AuditEntry.fromJson(<String, dynamic>{
        'seq': 5,
        'operation': 'mystery_op',
      });
      expect(entry.outcome.isUnknown, isTrue);
      expect(entry.isDenied, isFalse);
      expect(entry.isAllowed, isFalse);
      expect(entry.outcome.describe(), panelsNotRecorded);
      expect(entry.prevHash.describe(), panelsNotRecorded);
    });

    test('seq / prev_hash / hash are preserved for the chain badge', () {
      final Map<String, dynamic> entry = auditJson['entries'][1] as Map<String, dynamic>;
      final AuditEntry parsed = AuditEntry.fromJson(entry);
      expect(parsed.seq.value, 2);
      expect(parsed.prevHash.value, 'sha256:1111111111111111');
      expect(parsed.hash.value, 'sha256:2222222222222222');
    });

    test('an empty entries list is a real empty list, not a degraded state', () {
      final AuditState audit =
          AuditState.fromJson(<String, dynamic>{'entries': <dynamic>[], 'count': 0});
      expect(audit.entries.isRecorded, isTrue);
      expect(audit.list, isEmpty);
      expect(audit.denied, isEmpty);
    });

    test('a missing entries key stays unknown', () {
      final AuditState audit = AuditState.fromJson(<String, dynamic>{'count': 0});
      expect(audit.entries.isUnknown, isTrue);
      expect(audit.list, isEmpty);
    });

    test('a nil Go slice (null) parses as an empty recorded list', () {
      final AuditState audit =
          AuditState.fromJson(<String, dynamic>{'entries': null, 'count': 0});
      expect(audit.entries.isRecorded, isTrue);
      expect(audit.list, isEmpty);
    });
  });

  group('AuditVerification', () {
    test('verified response', () {
      final AuditVerification verification =
          AuditVerification.fromJson(auditVerifyJson);
      expect(verification.verified.value, true);
      expect(verification.entries.value, 2);
      expect(verification.broken, isFalse);
    });

    test('a broken chain is a legitimate "false" answer, not an exception', () {
      final AuditVerification verification =
          AuditVerification.broken('audit entry 3 hash mismatch');
      expect(verification.broken, isTrue);
      expect(verification.verified.value, false);
      expect(verification.note, contains('hash mismatch'));
    });
  });

  group('ModelConsistencyState', () {
    test('parses code / versions / seats', () {
      final ModelConsistencyState model =
          ModelConsistencyState.fromJson(modelJson);
      expect(model.consistent.value, true);
      expect(model.code.value, ModelCodes.consistent);
      expect(model.versions.value, <String>['flux-1.4']);
      expect(model.seats.value!.length, 2);
    });

    test('an absent model_version stays "not recorded"', () {
      final ModelConsistencyState model =
          ModelConsistencyState.fromJson(modelJson);
      final ModelUsage seat = model.seats.value![1];
      expect(seat.modelVersion.isUnknown, isTrue);
      expect(seat.modelVersion.describe(), panelsNotRecorded);
    });
  });

  group('ArchiveDetail', () {
    test('missing_fields are preserved (rendered as "not recorded")', () {
      final ArchiveDetail detail = ArchiveDetail.fromJson(detailJson);
      expect(detail.summary!.id, 'replay_ab12');
      expect(detail.integrity.complete.value, false);
      expect(detail.integrity.missingFields.value, <String>[
        'config.seats[0].model_version',
        'config.seats[0].persona_id',
      ]);
      expect(detail.config.firstSeatId, 'seat_0');
    });

    test('consent decision (fail-closed) parses participants/granted/missing', () {
      final ArchiveDetail detail = ArchiveDetail.fromJson(detailJson);
      expect(detail.consent.policy.value, 'all');
      expect(detail.consent.granted.value, <String>['comp_alpha']);
      expect(detail.consent.missing.value, <String>['comp_beta']);
      expect(detail.consent.isPublic.value, false);
    });

    test('a null missing_fields (complete archive) is an empty recorded list', () {
      final Map<String, dynamic> json = <String, dynamic>{
        ...detailJson,
        'integrity': <String, dynamic>{...detailJson['integrity'] as Map<String, dynamic>, 'missing_fields': null},
      };
      final ArchiveDetail detail = ArchiveDetail.fromJson(json);
      expect(detail.integrity.missingFields.isRecorded, isTrue);
      expect(detail.integrity.missingFields.value, isEmpty);
    });
  });

  group('ArchiveView (six modes)', () {
    test('full mode parses the timeline', () {
      final ArchiveView view = ArchiveView.fromJson(viewJsonForPath(
          PanelsApi.viewPath('replay_ab12', ArchiveModes.full)));
      expect(view.mode, ArchiveModes.full);
      expect(view.eventCount.value, 2);
      expect(view.events.value!.length, 2);
      expect(view.comparison, isNull);
    });

    test('same_spec_comparison carries the divergence point', () {
      final ArchiveView view = ArchiveView.fromJson(viewJsonForPath(PanelsApi
          .viewPath('replay_ab12', ArchiveModes.sameSpecComparison)));
      expect(view.comparison, isNotNull);
      expect(view.comparison!.divergencePoint.value, 3);
      expect(view.comparison!.hasDivergence, isTrue);
      expect(divergenceText(view.comparison!), '3');
    });

    test('unused projection fields are omitted and stay unknown', () {
      final ArchiveView view = ArchiveView.fromJson(<String, dynamic>{
        'mode': ArchiveModes.full,
        'archive_id': 'x',
        'session_id': 'y',
      });
      expect(view.events.isUnknown, isTrue);
      expect(view.rounds.isUnknown, isTrue);
      expect(view.moments.isUnknown, isTrue);
    });

    test('all six modes are declared', () {
      expect(ArchiveModes.all.length, 6);
      expect(ArchiveModes.all.toSet().length, 6);
    });
  });

  group('Reprise / divergence', () {
    test('a completed reprise carries a comparison', () {
      final List<Reprise> reprises = <Reprise>[
        for (final Object? r in reprisesJson['reprises'] as List<dynamic>)
          Reprise.fromJson(r as Map<String, dynamic>),
      ];
      expect(reprises.length, 1);
      expect(reprises.first.isCompleted, isTrue);
      expect(reprises.first.comparison!.divergencePoint.value, 3);
    });

    test('divergence point 0 is "never diverged", not "not recorded"', () {
      final RepriseComparison comparison = RepriseComparison.fromJson(
          <String, dynamic>{...repriseComparisonJson, 'divergence_point': 0});
      expect(comparison.divergencePoint.isRecorded, isTrue);
      expect(comparison.hasDivergence, isFalse);
      expect(divergenceText(comparison), '0 (never diverged)');
    });

    test('a missing divergence point stays "not recorded"', () {
      final RepriseComparison comparison =
          RepriseComparison.fromJson(<String, dynamic>{});
      expect(comparison.divergencePoint.isUnknown, isTrue);
      expect(divergenceText(comparison), panelsNotRecorded);
    });
  });
}
