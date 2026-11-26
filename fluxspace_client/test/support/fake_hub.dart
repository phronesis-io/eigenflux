import 'dart:async';

import 'package:fluxspace_client/models/hub_models.dart';
import 'package:fluxspace_client/services/hub_api.dart';

/// Wire-shaped fixtures. Parsing real JSON instead of constructing model
/// objects keeps the `fromJson` decoders under test: a fixture that skips them
/// would let a renamed field pass every test and fail on the first real call.
Map<String, dynamic> sessionJson({
  String sessionId = 'sess_1',
  String invitationId = 'inv_demo_1',
  String status = 'live',
  int round = 1,
  int totalRounds = 3,
  String phase = 'observe',
  Set<String> joined = const <String>{'seat_0'},
  List<String> actionIds = const <String>['a1', 'a2'],
  String outcome = '',
  int lastEventId = 7,
  bool dedupDegraded = false,
}) {
  return <String, dynamic>{
    'session_id': sessionId,
    'invitation_id': invitationId,
    'status': status,
    'round': round,
    'total_rounds': totalRounds,
    'phase': phase,
    'seats': <Map<String, dynamic>>[
      <String, dynamic>{
        'seat_id': 'seat_0',
        'owner_id': 'owner_alpha',
        'agent_id': 'agent_alpha',
        'companion_id': 'comp_alpha',
        'display_name': 'Lin Shen',
        'official': false,
        'joined': joined.contains('seat_0'),
        'personality': <String, dynamic>{'risk': 60, 'logic': 70, 'empathy': 40},
      },
      <String, dynamic>{
        'seat_id': 'seat_1',
        'owner_id': 'owner_beta',
        'agent_id': 'agent_beta',
        'companion_id': 'comp_beta',
        'display_name': 'Su Wan',
        'official': false,
        'joined': joined.contains('seat_1'),
        'personality': <String, dynamic>{'risk': 30, 'logic': 50, 'empathy': 80},
      },
    ],
    'metrics': <String, dynamic>{
      'trust': <String, dynamic>{'key': 'trust', 'label': 'Trust', 'value': 50, 'max': 100, 'good': 'high'},
      'danger': <String, dynamic>{'key': 'danger', 'label': 'Danger', 'value': 20, 'max': 100, 'good': 'low'},
    },
    'outcome': outcome,
    'skeleton': <String, dynamic>{
      'type': 'deduction',
      'theme_id': 'theme_1',
      'title': 'The Locked Ward',
      'theme_text': 'A ward with one door and a liar in it.',
      'rules': <String>['three rounds', 'one decision per seat'],
      'rounds': <Map<String, dynamic>>[
        <String, dynamic>{
          'index': 1,
          'title': 'Round 1',
          'objective': 'Find out who moved the chart.',
          'tension': 2,
          'reveal': <Map<String, dynamic>>[
            <String, dynamic>{'key': 'clue_a', 'label': 'Chart', 'text': 'Ink is still wet.'},
          ],
          'actions': <Map<String, dynamic>>[
            for (final String id in actionIds)
              <String, dynamic>{
                'id': id,
                'label': 'Action $id',
                'family': 'investigate',
                'risk': 'low',
                'info_gain': 0.4,
                'team_harm': 0.1,
                'contest': 0,
              },
          ],
        },
      ],
      'fingerprint': 'fp_1',
      'estimated_minutes': 20,
      'dedup_degraded': dedupDegraded,
    },
    'decisions': <String, dynamic>{},
    'last_event_id': lastEventId,
    'social_contract': <String, dynamic>{
      'sharing': 'open',
      'private_channels': 'allowed',
      'replay_consent': 'all',
      'visibility': 'private',
    },
  };
}

Map<String, dynamic> negotiationJson({bool confirmed = false, String status = 'draft'}) {
  return <String, dynamic>{
    'id': 'inv_demo_1',
    'status': status,
    'seats': 2,
    'initiator': <String, dynamic>{
      'agent_id': 'agent_alpha',
      'companion_id': 'comp_alpha',
      'owner_id': 'owner_alpha',
      'display_name': 'Lin Shen',
      'confirmed': true,
      'selected_proposal': 1,
    },
    'responder': <String, dynamic>{
      'agent_id': 'agent_beta',
      'companion_id': 'comp_beta',
      'owner_id': 'owner_beta',
      'display_name': 'Su Wan',
      'confirmed': confirmed,
      'selected_proposal': confirmed ? 1 : 0,
    },
    'invitation': <String, dynamic>{
      'kind': 'fluxspace.invitation.v1',
      'invitation_id': 'inv_demo_1',
      'estimated_minutes': 20,
      'proposals': <Map<String, dynamic>>[
        for (int i = 1; i <= 3; i++)
          <String, dynamic>{
            'index': i,
            'type': 'deduction',
            'seats': 2,
            'minutes': 20,
            'summary': 'Proposal $i',
            'fingerprint': 'fp_$i',
          },
      ],
      'social_contract_draft': <String, dynamic>{
        'sharing': 'open',
        'private_channels': 'allowed',
        'replay_consent': 'all',
        'visibility': 'private',
      },
    },
  };
}

/// A scripted [HubApi].
///
/// Records calls as `name:argument` strings so a test asserts on what the client
/// asked the Hub for, which is the interesting half of these flows: the
/// rendering of the answer is a second problem.
class FakeHubApi implements HubApi {
  FakeHubApi({this.ownerIdValue = 'owner_alpha'});

  final String ownerIdValue;

  bool healthy = true;
  bool confirmOnAccept = true;
  HubException? createError;
  HubException? bindError;
  HubException? joinError;
  HubException? acceptError;
  HubException? discussionError;
  HubException? actionError;

  int round = 1;
  String phase = 'observe';
  String status = 'lobby';
  Set<String> joined = <String>{'seat_0'};

  final List<String> calls = <String>[];
  final StreamController<HubEvent> eventsController = StreamController<HubEvent>.broadcast();

  @override
  String get ownerId => ownerIdValue;

  @override
  Future<bool> health() async {
    calls.add('health');
    return healthy;
  }

  @override
  Future<void> bindCompanion(String companionId) async {
    calls.add('bind:$companionId');
    if (bindError != null) throw bindError!;
  }

  @override
  Future<NegotiationView> createInvitation(MatchDraft draft) async {
    calls.add('createInvitation:${draft.invitationId}:${draft.seats}');
    if (createError != null) throw createError!;
    return NegotiationView.fromJson(negotiationJson());
  }

  @override
  Future<NegotiationView> acceptInvitation(String invitationId, {required String agentId, int? selectedProposal}) async {
    calls.add('accept:$invitationId:$agentId');
    if (acceptError != null) throw acceptError!;
    return NegotiationView.fromJson(
      negotiationJson(confirmed: confirmOnAccept, status: confirmOnAccept ? 'confirmed' : 'negotiating'),
    );
  }

  @override
  Future<SessionView> createSessionFromInvitation(String invitationId, {int seed = 0}) async {
    calls.add('createSession:$invitationId');
    return SessionView.fromJson(sessionJson(status: status, joined: joined, phase: phase, round: round));
  }

  @override
  Future<JoinResult> join(String sessionId, {required String seatId, required String ownerId}) async {
    calls.add('join:$sessionId:$seatId:$ownerId');
    if (joinError != null) throw joinError!;
    joined = <String>{...joined, seatId};
    return JoinResult.fromJson(<String, dynamic>{
      'session': sessionJson(sessionId: sessionId, joined: joined, phase: phase, round: round),
      'stream_token': 'token_$seatId',
    });
  }

  @override
  Future<SessionView> state(String sessionId) async {
    calls.add('state:$sessionId');
    return SessionView.fromJson(sessionJson(sessionId: sessionId, status: status, joined: joined, phase: phase, round: round));
  }

  @override
  Future<SessionView> snapshot(String sessionId) async {
    calls.add('snapshot:$sessionId');
    return SessionView.fromJson(sessionJson(sessionId: sessionId, status: status, joined: joined, phase: phase, round: round));
  }

  @override
  Future<SessionView> spectatorState(String sessionId, {required String spectatorToken}) async {
    calls.add('spectatorState:$sessionId:$spectatorToken');
    return SessionView.fromJson(
      sessionJson(sessionId: sessionId, status: status, joined: joined, phase: phase, round: round),
    );
  }

  @override
  Stream<HubEvent> events(
    String sessionId, {
    required String seatId,
    required String seatToken,
    int lastEventId = 0,
  }) {
    calls.add('events:$sessionId:$seatId:$seatToken:$lastEventId');
    return eventsController.stream;
  }

  @override
  Stream<HubEvent> spectate(String sessionId, {required String spectatorToken, int lastEventId = 0}) {
    calls.add('spectate:$sessionId:$spectatorToken:$lastEventId');
    return eventsController.stream;
  }

  @override
  Future<void> postDiscussion(String sessionId, {required String seatId, required String text, String channel = 'public'}) async {
    calls.add('discussion:$seatId:$text');
    if (discussionError != null) throw discussionError!;
  }

  @override
  Future<SessionView> submitAction(
    String sessionId, {
    required String seatId,
    required String actionId,
    required String reasoning,
    double confidence = 1,
  }) async {
    calls.add('action:$seatId:$actionId');
    if (actionError != null) throw actionError!;
    return SessionView.fromJson(sessionJson(sessionId: sessionId, status: status, joined: joined, phase: phase, round: round));
  }

  @override
  Future<SessionView> advance(String sessionId, String phase) async {
    calls.add('advance:$phase');
    this.phase = phase;
    return SessionView.fromJson(sessionJson(sessionId: sessionId, status: status, joined: joined, phase: phase, round: round));
  }

  Future<void> emit(Map<String, dynamic> payload) async {
    eventsController.add(HubEvent.fromJson(payload));
  }

  Future<void> dispose() => eventsController.close();
}
