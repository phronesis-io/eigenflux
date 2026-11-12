/// Widget smoke renders for both panels and the composed screen, plus a
/// no-overflow check at 800x600. All data comes from the injected fetcher.
library;

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:fluxspace_client/panels/archive_panel.dart';
import 'package:fluxspace_client/panels/governance_panel.dart';
import 'package:fluxspace_client/panels/panels_api.dart';
import 'package:fluxspace_client/panels/panels_models.dart';
import 'package:fluxspace_client/panels/panels_screen.dart';

import 'support/panels_fakes.dart';

void main() {
  testWidgets('GovernancePanel renders budget, tier, audit chain and consistency',
      (WidgetTester tester) async {
    final ScriptedFetcher fetcher = ScriptedFetcher();
    await tester.pumpWidget(MaterialApp(
      home: Scaffold(
        body: GovernancePanel(
          api: fetcher.api(),
          companionId: 'comp_alpha',
          archiveId: 'replay_ab12',
        ),
      ),
    ));
    await tester.pumpAndSettle();

    expect(find.text('Monthly token budget'), findsOneWidget);
    expect(find.text('200000'), findsWidgets);
    expect(find.text('Permission tier (default-deny)'), findsOneWidget);
    expect(find.text(PermissionTiers.restrictedWrite), findsWidgets);
    expect(find.text('chain verified'), findsOneWidget);
    expect(find.text('export_contacts'), findsWidgets); // denied operation
    expect(find.text('Model-version consistency'), findsOneWidget);
    // downgrade is unknown on a read -> "not recorded", never fabricated.
    expect(find.text(panelsNotRecorded), findsWidgets);
    expect(tester.takeException(), isNull);
  });

  testWidgets('GovernancePanel shows GOVERNANCE_DEGRADED as an explicit state',
      (WidgetTester tester) async {
    final ScriptedFetcher fetcher = ScriptedFetcher(
      degradedContains: <String>{'/hub/companions/', PanelsApi.pathAudit},
    );
    await tester.pumpWidget(MaterialApp(
      home: Scaffold(
        body: GovernancePanel(
          api: fetcher.api(),
          companionId: 'comp_alpha',
        ),
      ),
    ));
    await tester.pumpAndSettle();

    expect(find.textContaining('GOVERNANCE_DEGRADED'), findsWidgets);
    expect(find.textContaining('NOT an empty list'), findsOneWidget);
    // A degraded audit must NOT be rendered as an empty list.
    expect(find.textContaining('No audit entries recorded'), findsNothing);
    expect(tester.takeException(), isNull);
  });

  testWidgets('ArchivePanel renders sessions, six views, missing_fields, consent, divergence',
      (WidgetTester tester) async {
    final ScriptedFetcher fetcher = ScriptedFetcher();
    await tester.pumpWidget(MaterialApp(
      home: Scaffold(
        body: ArchivePanel(
          api: fetcher.api(),
          archiveId: 'replay_ab12',
          compareWith: 'replay_cd34',
        ),
      ),
    ));
    await tester.pumpAndSettle();

    expect(find.text('Archived sessions'), findsOneWidget);
    expect(find.text('Six-view projection'), findsOneWidget);
    expect(find.text(ArchiveModes.label(ArchiveModes.full)), findsOneWidget);
    // Six view chips exist.
    for (final String mode in ArchiveModes.all) {
      expect(find.text(ArchiveModes.label(mode)), findsOneWidget);
    }
    expect(find.text('Consent ledger (fail-closed)'), findsOneWidget);
    expect(find.text('Same-spec replay · divergence point'), findsOneWidget);
    // missing_fields rendered as "not recorded".
    expect(find.textContaining('model_version: not recorded'), findsWidgets);
    expect(tester.takeException(), isNull);
  });

  testWidgets('ArchivePanel switches the six-view projection without error',
      (WidgetTester tester) async {
    final ScriptedFetcher fetcher = ScriptedFetcher();
    await tester.pumpWidget(MaterialApp(
      home: Scaffold(
        body: ArchivePanel(api: fetcher.api(), archiveId: 'replay_ab12'),
      ),
    ));
    await tester.pumpAndSettle();

    await tester.tap(find.text(ArchiveModes.label(ArchiveModes.sameSpecComparison)));
    await tester.pumpAndSettle();

    expect(find.textContaining('Divergence point'), findsWidgets);
    expect(tester.takeException(), isNull);
  });

  testWidgets('PanelsScreen composes both tabs', (WidgetTester tester) async {
    final ScriptedFetcher fetcher = ScriptedFetcher();
    await tester.pumpWidget(MaterialApp(
      home: PanelsScreen(
        api: fetcher.api(),
        companionId: 'comp_alpha',
        archiveId: 'replay_ab12',
      ),
    ));
    await tester.pumpAndSettle();

    expect(find.text('Governance'), findsOneWidget);
    expect(find.text('Archive'), findsOneWidget);
    // IndexedStack builds both panels; governance content is visible on tab 0.
    expect(find.text('Monthly token budget'), findsOneWidget);
    expect(tester.takeException(), isNull);
  });

  testWidgets('no RenderFlex overflow at 800x600', (WidgetTester tester) async {
    tester.view.physicalSize = const Size(800, 600);
    tester.view.devicePixelRatio = 1.0;
    addTearDown(() => tester.view.reset());

    final ScriptedFetcher fetcher = ScriptedFetcher();
    await tester.pumpWidget(MaterialApp(
      home: PanelsScreen(
        api: fetcher.api(),
        companionId: 'comp_alpha',
        archiveId: 'replay_ab12',
        compareWith: 'replay_cd34',
      ),
    ));
    await tester.pumpAndSettle();

    // A layout overflow would have been captured as a FlutterError here.
    expect(tester.takeException(), isNull);

    // Switch to the archive tab and re-check the same constraint.
    await tester.tap(find.text('Archive'));
    await tester.pumpAndSettle();
    expect(tester.takeException(), isNull);
    expect(find.text('Six-view projection'), findsOneWidget);
  });
}
