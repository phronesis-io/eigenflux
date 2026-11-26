import 'package:flutter_test/flutter_test.dart';
import 'package:fluxspace_client/theatre/theatre_models.dart';

void main() {
  test('TheatreFrame.fromMap never invents missing fields', () {
    final TheatreFrame frame = TheatreFrame.fromMap(<String, dynamic>{});

    expect(frame.round, isNull);
    expect(frame.totalRounds, isNull);
    expect(frame.phase, isNull);
    expect(frame.title, isNull);
    expect(frame.summary, isNull);
    expect(frame.activeSpeakerId, isNull);
    expect(frame.viewMode, isNull);
    expect(frame.lightGrade, isNull);
    expect(frame.seats, isEmpty);
    expect(frame.votes, isEmpty);
    expect(frame.reveal, isNull);
  });

  test('TheatreFrame.fromMap maps full data and tolerates wrong types', () {
    final TheatreFrame frame =
        TheatreFrame.fromMap(<String, dynamic>{
      'round': '3', // numeric string is coerced, not rejected
      'total_rounds': 5,
      'phase': 'act',
      'title': 'The Locked Ward',
      'summary': 'a decision summary',
      'active_speaker_id': 'seat_2',
      'view_mode': 'full',
      'light_grade': 'warm',
      'seats': <Map<String, dynamic>>[
        <String, dynamic>{
          'id': 'seat_2',
          'display_name': 'Su Wan',
          'persona_id': 'su-wan',
          'state': 'speaking',
          'official': true,
        },
        <String, dynamic>{'id': 'seat_9', 'state': 'not-a-state'},
      ],
      'votes': <Map<String, dynamic>>[
        <String, dynamic>{
          'voter_id': 'seat_2',
          'target_id': 'seat_0',
          'label': 'chose the safe route',
        },
      ],
      'reveal': <String, dynamic>{
        'winner_id': 'seat_0',
        'note': 'tally settled',
        'tally': <String, dynamic>{'seat_0': 3, 'seat_2': '2'},
      },
    });

    expect(frame.round, 3);
    expect(frame.totalRounds, 5);
    expect(frame.phase, 'act');
    expect(frame.activeSpeakerId, 'seat_2');
    expect(frame.lightGrade, 'warm');

    expect(frame.seats, hasLength(2));
    expect(frame.seats[0].state, TheatreSeatState.speaking);
    expect(frame.seats[0].isOfficial, isTrue);
    // An unrecognised state and an absent name are "not recorded", not guessed.
    expect(frame.seats[1].state, isNull);
    expect(frame.seats[1].displayName, isNull);

    expect(frame.voteFor('seat_2')?.targetId, 'seat_0');
    expect(frame.voteFor('seat_9'), isNull);
    expect(frame.seatById('seat_2')?.displayName, 'Su Wan');

    expect(frame.reveal?.winnerId, 'seat_0');
    expect(frame.reveal?.tally['seat_2'], 2); // numeric string coerced
    expect(frame.reveal?.note, 'tally settled');
  });

  test('theatreFramesFromMaps is tolerant of junk entries', () {
    expect(theatreFramesFromMaps(null), isEmpty);
    expect(theatreFramesFromMaps('not a list'), isEmpty);

    final List<TheatreFrame> frames = theatreFramesFromMaps(<Object?>[
      <String, dynamic>{'round': 1},
      'skip me',
      <String, dynamic>{'round': 2},
    ]);
    expect(frames, hasLength(2));
    expect(frames[0].round, 1);
    expect(frames[1].round, 2);
  });

  test('TheatreSeatState labels are the four chip values', () {
    expect(TheatreSeatState.values.map((TheatreSeatState s) => s.label),
        <String>['thinking', 'speaking', 'voted', 'eliminated']);
  });
}
