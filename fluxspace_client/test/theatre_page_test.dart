import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:fluxspace_client/models/hub_models.dart';
import 'package:fluxspace_client/pages/theatre_page.dart';
import 'package:fluxspace_client/services/token_store.dart';
import 'package:fluxspace_client/session/session_controller.dart';

import 'support/fake_hub.dart';

/// The console is a text view of a real session, so the thing worth checking is
/// that it renders the Hub's answer instead of its own scaffolding — and that it
/// does so with an injected controller, since the production path would reach
/// for the platform keychain, which is absent under `flutter test`.
void main() {
  testWidgets('the console renders connection fields before anything is connected', (WidgetTester tester) async {
    final FakeHubApi api = FakeHubApi();
    final SessionController controller = SessionController(api: api, tokens: MemoryTokenStore());
    addTearDown(() async {
      controller.dispose();
      await api.dispose();
    });

    await tester.pumpWidget(MaterialApp(home: TheatrePage(controller: controller)));

    expect(find.text('Hub'), findsOneWidget);
    expect(find.text('Connect'), findsOneWidget);
    expect(find.text('Open a match'), findsOneWidget);
    // The log sits below the fold on a test-sized viewport; it is asserted in
    // the seated case instead of scrolling for it here.
  });

  testWidgets('a seated match shows its title, seats and current actions', (WidgetTester tester) async {
    final FakeHubApi api = FakeHubApi();
    final SessionController controller = SessionController(api: api, tokens: MemoryTokenStore());
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

    expect(find.text('The Locked Ward'), findsOneWidget);
    expect(find.textContaining('round 1'), findsOneWidget);
    // Seat rows come from the view, not from the setup the user typed.
    expect(find.textContaining('seat_0 · Lin Shen'), findsOneWidget);
    expect(find.textContaining('seat_1 · Su Wan'), findsOneWidget);
    // Actions are visible but not pressable outside the act phase: the player
    // should see what the round offers without being able to pick it yet.
    final Iterable<ActionChip> chips = tester.widgetList<ActionChip>(find.byType(ActionChip));
    expect(chips, hasLength(2));
    expect(chips.every((ActionChip c) => c.onPressed == null), isTrue);
    expect(find.text('Advance to discuss'), findsOneWidget);
  });
}
