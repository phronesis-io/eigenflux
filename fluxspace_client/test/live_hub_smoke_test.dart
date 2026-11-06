@TestOn('vm')
library;

import 'dart:async';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:fluxspace_client/models/hub_models.dart';
import 'package:fluxspace_client/services/hub_client.dart';

/// End-to-end against a real Hub process.
///
/// Skipped by default so `flutter test` stays hermetic. To run it:
///
/// ```
/// cd hub && bash scripts/build.sh
/// HUB_ALLOW_UNAUTHENTICATED=1 HUB_ADDR=:8399 ./build/fluxspace-hub &
/// cd ../fluxspace_client && FLUXSPACE_LIVE_HUB=http://127.0.0.1:8399 \
///   flutter test test/live_hub_smoke_test.dart
/// ```
///
/// It runs in unauthenticated mode on purpose: with tokens on, joining seat_1
/// requires owner_beta's token, so one client cannot sit both seats — which is
/// the property ADR-0009 added. The unit tests cover the authenticated paths
/// against a fake Hub; this one covers the parts only a real Hub can answer:
/// generated skeleton, phase transitions, settlement, and the SSE framing.
void main() {
  final String? base = Platform.environment['FLUXSPACE_LIVE_HUB'];

  test('one match runs from invitation to settlement', () async {
    final HubClient client = HubClient(baseUrl: base);

    expect(await client.health(), isTrue, reason: 'the Hub must answer GET /health');

    // The Hub keeps invitations for the life of the process and refuses a
    // duplicate id, so each run names its own.
    final String invitationId = 'inv_smoke_${DateTime.now().microsecondsSinceEpoch}';
    final NegotiationView offered = await client.createInvitation(MatchDraft(
      invitationId: invitationId,
      initiator: const HubParty(
        agentId: 'agent_alpha',
        companionId: 'comp_alpha',
        ownerId: 'owner_alpha',
        displayName: 'Lin Shen',
      ),
      responder: const HubParty(
        agentId: 'agent_beta',
        companionId: 'comp_beta',
        ownerId: 'owner_beta',
        displayName: 'Su Wan',
      ),
      seats: 2,
    ));

    // Three proposals is the generator's contract, not a preference: an
    // invitation with fewer cannot be negotiated.
    expect(offered.proposals, hasLength(3));
    expect(offered.isConfirmed, isFalse);

    final NegotiationView confirmed = await client.acceptInvitation(
      offered.id,
      agentId: 'agent_beta',
      selectedProposal: 1,
    );
    expect(confirmed.isConfirmed, isTrue, reason: 'both parties must confirm before a match exists');

    SessionView view = await client.createSessionFromInvitation(confirmed.id);
    expect(view.seats, hasLength(2));
    expect(view.isLive, isFalse, reason: 'a lobby is not live until every seat joins');

    final JoinResult first = await client.join(view.sessionId, seatId: 'seat_0', ownerId: 'owner_alpha');
    expect(first.streamToken, isNotEmpty);

    // Subscribe before the match starts so no event can be missed, and to prove
    // the seat token opens the private stream.
    final List<HubEvent> events = <HubEvent>[];
    final Completer<void> streamDone = Completer<void>();
    final StreamSubscription<HubEvent> subscription =
        client.events(view.sessionId, seatId: 'seat_0', seatToken: first.streamToken).listen(
      events.add,
      onError: streamDone.completeError,
      onDone: streamDone.complete,
    );
    addTearDown(subscription.cancel);

    final JoinResult second = await client.join(view.sessionId, seatId: 'seat_1', ownerId: 'owner_beta');
    expect(second.streamToken, isNot(first.streamToken), reason: 'tokens are per seat, not per session');

    final Map<String, String> seats = <String, String>{
      'seat_0': first.streamToken,
      'seat_1': second.streamToken,
    };

    view = await client.state(view.sessionId);
    expect(view.isLive, isTrue, reason: 'the second join makes the lobby live');
    expect(view.skeleton, isNotNull);

    // Play the match out. The Hub owns the transition rules, so the loop reads
    // the phase rather than assuming an order — the assertions would otherwise
    // fail the first time a rule changed.
    int guard = 0;
    while (!view.isFinished && guard++ < 60) {
      switch (view.phase) {
        case 'observe':
          view = await client.advance(view.sessionId, 'discuss');
        case 'discuss':
          view = await client.advance(view.sessionId, 'act');
        case 'act':
          final HubRound round = view.currentRound!;
          expect(round.actions, isNotEmpty);
          for (final HubSeat seat in view.seats.where((HubSeat s) => s.joined)) {
            if (view.decisions.containsKey(seat.seatId)) continue;
            expect(seats.containsKey(seat.seatId), isTrue);
            view = await client.submitAction(
              view.sessionId,
              seatId: seat.seatId,
              actionId: round.actions.first.id,
              reasoning: 'smoke: first legal action',
            );
          }
        default:
          // A phase that is neither the three above nor finished means the Hub
          // and this client disagree; fail loudly instead of spinning.
          fail('unexpected phase ${view.phase} in round ${view.round}');
      }
    }

    expect(guard, lessThan(60), reason: 'the match did not terminate');
    expect(view.isFinished, isTrue);
    expect(view.outcome, isNotEmpty, reason: 'a finished match carries its settlement');
    // Not `== totalRounds`: a win or lose condition can settle the match before
    // the last round, and the first run of this test proved it (3 of 5).
    expect(view.round, lessThanOrEqualTo(view.totalRounds));

    await streamDone.future.timeout(const Duration(seconds: 10));
    final Set<String> types = events.map((HubEvent e) => e.type).toSet();
    expect(types, contains('round_begin'));
    expect(types, contains('decision'));
    expect(types, contains('match_end'));
    // Event ids are monotonic; a client that resumes with Last-Event-ID relies
    // on it.
    for (int i = 1; i < events.length; i++) {
      expect(events[i].id, greaterThan(events[i - 1].id));
    }
  }, skip: base == null ? 'set FLUXSPACE_LIVE_HUB=http://127.0.0.1:8399 to run' : false);
}
