/// Widget smoke renders and a no-overflow check at 800x600. Every test injects
/// a [SetupState] built on [FakeHubProbe] + [MemoryTokenStore], so no platform
/// keychain and no socket is touched.
library;

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:fluxspace_client/pages/home_page.dart';
import 'package:fluxspace_client/services/token_store.dart';
import 'package:fluxspace_client/setup/hub_probe.dart';
import 'package:fluxspace_client/setup/setup_state.dart';
import 'package:fluxspace_client/setup/setup_wizard.dart';

import 'setup_fakes.dart';

void main() {
  testWidgets('the wizard renders its first step, indicator and navigation',
      (WidgetTester tester) async {
    final SetupState state = makeState(
      probe: FakeHubProbe(health: healthEnabled()),
      store: MemoryTokenStore(),
    );
    addTearDown(state.dispose);

    await tester.pumpWidget(MaterialApp(home: SetupWizard(state: state)));
    await tester.pumpAndSettle();

    expect(find.text('Set up Hub'), findsOneWidget); // AppBar
    expect(find.text('Where is the Hub?'), findsOneWidget);
    expect(find.textContaining('Hub URL'), findsWidgets); // field + step chip
    expect(find.text('Next'), findsOneWidget);
    expect(find.text('Back'), findsNothing); // first step
    expect(tester.takeException(), isNull);
  });

  testWidgets('an empty URL shows a clear inline error and does not advance',
      (WidgetTester tester) async {
    final SetupState state = makeState(
      probe: FakeHubProbe(),
      store: MemoryTokenStore(),
      url: '',
    );
    addTearDown(state.dispose);

    await tester.pumpWidget(MaterialApp(home: SetupWizard(state: state)));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Next'));
    await tester.pumpAndSettle();

    expect(find.textContaining('Enter the Hub URL'), findsOneWidget);
    expect(state.step, SetupStep.hubUrl);
    expect(tester.takeException(), isNull);
  });

  testWidgets('the two auth modes are shown with explicit guidance',
      (WidgetTester tester) async {
    final FakeHubProbe probe = FakeHubProbe(health: healthEnabled());
    final SetupState state = makeState(probe: probe, store: MemoryTokenStore());
    addTearDown(state.dispose);

    await tester.pumpWidget(MaterialApp(home: SetupWizard(state: state)));
    await state.next(); // connection step
    await tester.pumpAndSettle();
    await state.checkConnection();
    await tester.pumpAndSettle();

    expect(find.textContaining('HUB_OWNER_TOKENS'), findsOneWidget);
    expect(find.textContaining('auth: enabled'), findsOneWidget);

    // Re-script for the open Hub and re-run the check.
    probe.scriptHealth(healthDisabled());
    await state.checkConnection();
    await tester.pumpAndSettle();
    expect(find.textContaining('HUB_ALLOW_UNAUTHENTICATED=1'), findsOneWidget);
    expect(find.textContaining('auth: disabled'), findsOneWidget);
    expect(tester.takeException(), isNull);
  });

  testWidgets('HomePage exposes a visible Set up Hub action that opens the wizard',
      (WidgetTester tester) async {
    await tester.pumpWidget(const MaterialApp(home: HomePage()));
    await tester.pumpAndSettle();

    expect(find.text('Set up Hub'), findsOneWidget);
    await tester.tap(find.text('Set up Hub'));
    await tester.pumpAndSettle();

    // The wizard's first-step title only exists on the pushed route.
    expect(find.text('Where is the Hub?'), findsOneWidget);
    expect(tester.takeException(), isNull);
  });

  testWidgets('no RenderFlex overflow at 800x600 on every step',
      (WidgetTester tester) async {
    tester.view.physicalSize = const Size(800, 600);
    tester.view.devicePixelRatio = 1.0;
    addTearDown(() => tester.view.reset());

    final FakeHubProbe probe = FakeHubProbe(
      health: healthEnabled(),
      binding: const BindingCheck.success(),
    );
    final SetupState state = makeState(probe: probe, store: MemoryTokenStore());
    addTearDown(state.dispose);

    await tester.pumpWidget(MaterialApp(home: SetupWizard(state: state)));
    await tester.pumpAndSettle();
    expect(tester.takeException(), isNull); // hubUrl

    await state.next(); // connection
    await tester.pumpAndSettle();
    expect(tester.takeException(), isNull);

    await state.checkConnection();
    await tester.pumpAndSettle();
    expect(tester.takeException(), isNull);

    await state.next(); // ownerToken
    await tester.pumpAndSettle();
    expect(tester.takeException(), isNull);

    state.setOwnerToken('tok-alpha');
    await state.next(); // identity
    await tester.pumpAndSettle();
    expect(tester.takeException(), isNull);

    state.setOwnerId('owner_alpha');
    state.setCompanionId('comp_alpha');
    await state.next(); // binding
    await tester.pumpAndSettle();
    expect(tester.takeException(), isNull);

    await state.checkBinding();
    await tester.pumpAndSettle();
    expect(tester.takeException(), isNull);

    await state.next(); // save
    await tester.pumpAndSettle();
    expect(tester.takeException(), isNull);

    await state.next(); // save -> done
    await tester.pumpAndSettle();
    expect(tester.takeException(), isNull);
    expect(find.text('Setup complete'), findsOneWidget);
    expect(find.text('Done'), findsOneWidget);
  });
}
