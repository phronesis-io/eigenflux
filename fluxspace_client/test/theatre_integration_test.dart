import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:fluxspace_client/models/hub_models.dart';
import 'package:fluxspace_client/pages/theatre_page.dart';
import 'package:fluxspace_client/services/token_store.dart';
import 'package:fluxspace_client/session/session_controller.dart';
import 'package:fluxspace_client/theatre/theatre_screen.dart';

import 'support/fake_hub.dart';

/// The three slices added in this batch (automatic reconnection, the S3 render
/// skeleton and the S4/S5 panels) are only useful if the app actually reaches
/// them. These tests pin the wiring, not the widgets' own internals.
///
/// The console is a lazy `ListView`: on the default 800x600 test surface the
/// entry-point card below the setup cards is never built. Enlarge the surface
/// instead of scrolling, so the assertions are about wiring rather than layout.
void _largeSurface(WidgetTester tester) {
  tester.view.physicalSize = const Size(1200, 2400);
  tester.view.devicePixelRatio = 1.0;
  addTearDown(tester.view.resetPhysicalSize);
  addTearDown(tester.view.resetDevicePixelRatio);
}

void main() {
  Future<(SessionController, FakeHubApi)> fixture() async {
    final FakeHubApi api = FakeHubApi();
    final SessionController controller =
        SessionController(api: api, tokens: MemoryTokenStore());
    return (controller, api);
  }

  testWidgets('the console exposes the live connection state', (WidgetTester tester) async {
    final (SessionController controller, FakeHubApi api) = await fixture();
    addTearDown(() async {
      controller.dispose();
      await api.dispose();
    });

    await tester.pumpWidget(MaterialApp(home: TheatrePage(controller: controller)));

    expect(find.text('Stream: idle'), findsOneWidget);
    expect(find.byIcon(Icons.sync), findsOneWidget);
  });

  testWidgets('the stage and records entry points are present, stage is gated on a view',
      (WidgetTester tester) async {
    _largeSurface(tester);
    final (SessionController controller, FakeHubApi api) = await fixture();
    addTearDown(() async {
      controller.dispose();
      await api.dispose();
    });

    await tester.pumpWidget(MaterialApp(home: TheatrePage(controller: controller)));

    final Finder stageButton = find.widgetWithText(OutlinedButton, 'Visual stage (placeholder)');
    final Finder panelsButton = find.widgetWithText(OutlinedButton, 'Governance & archives');
    expect(stageButton, findsOneWidget);
    expect(panelsButton, findsOneWidget);
    // No session view yet: the stage has nothing honest to render.
    expect(tester.widget<OutlinedButton>(stageButton).onPressed, isNull);
    // Records can be browsed without a live session.
    expect(tester.widget<OutlinedButton>(panelsButton).onPressed, isNotNull);
  });

  testWidgets('a seated match opens the placeholder stage with the Hub seats',
      (WidgetTester tester) async {
    _largeSurface(tester);
    final (SessionController controller, FakeHubApi api) = await fixture();
    addTearDown(() async {
      controller.dispose();
      await api.dispose();
    });

    await tester.pumpWidget(MaterialApp(home: TheatrePage(controller: controller)));
    await controller.connect(companionId: 'comp_alpha');
    await controller.startMatch(const MatchSetup(
      invitationId: 'inv_demo_1',
      me: HubParty(
        agentId: 'agent_alpha',
        companionId: 'comp_alpha',
        ownerId: 'owner_alpha',
        displayName: 'Lin Shen',
      ),
      partner: HubParty(
        agentId: 'agent_beta',
        companionId: 'comp_beta',
        ownerId: 'owner_beta',
        displayName: 'Su Wan',
      ),
    ));
    await tester.pumpAndSettle();

    final Finder stageButton = find.widgetWithText(OutlinedButton, 'Visual stage (placeholder)');
    expect(tester.widget<OutlinedButton>(stageButton).onPressed, isNotNull);

    // Invoke the wired callback directly: the button sits below the fold on a
    // test-sized viewport and scrolling is not what is under test here.
    tester.widget<OutlinedButton>(stageButton).onPressed!();
    await tester.pumpAndSettle();

    expect(find.byType(TheatreScreen), findsOneWidget);
    expect(find.text('Lin Shen'), findsWidgets);
    expect(find.text('Su Wan'), findsWidgets);
  });
}
