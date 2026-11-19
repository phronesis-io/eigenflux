/// Adapter tests: Hub `View` JSON -> `List<TheatreFrame>`.
///
/// Network-free: the only API calls go through the injected [ScriptedFetcher].
/// The "GET only" group proves the replay flow never requests a write/consume
/// route. Field names in every fixture are transcribed from the Go `json:"..."`
/// tags in `hub/archive/views.go`, `hub/archive/reprise.go` and
/// `hub/models/session.go`.
library;

import 'package:flutter_test/flutter_test.dart';
import 'package:fluxspace_client/panels/archive_frames.dart';
import 'package:fluxspace_client/panels/panels_api.dart';
import 'package:fluxspace_client/panels/panels_models.dart';

import 'support/panels_fakes.dart';

// --- realistic Hub-shaped fixtures ------------------------------------------

/// A `full` `View` with every `TimelineEvent` wire field the Hub emits. The
/// four `phase` values are the real ones (`hub/models/session.go`: observe /
/// discuss / act / resolve), not invented labels.
Map<String, dynamic> hubFullView() => <String, dynamic>{
      'mode': 'full',
      'archive_id': 'replay_ab12',
      'session_id': 'sess_1',
      'speed': 0,
      'duration_ms': 9000,
      'event_count': 3,
      'events': <dynamic>[
        <String, dynamic>{
          'seq': 1,
          'type': 'match_start',
          'round': 0,
          'phase': 'observe',
          'seat_id': '',
          'visibility': 'public',
          'ts': 1000,
          'offset_ms': 0,
          'payload': <String, dynamic>{},
        },
        <String, dynamic>{
          'seq': 2,
          'type': 'discussion',
          'round': 1,
          'phase': 'discuss',
          'seat_id': 'seat_0',
          'visibility': 'public',
          'ts': 4000,
          'offset_ms': 3000,
          'payload': <String, dynamic>{'text': 'Lin speaks'},
        },
        <String, dynamic>{
          'seq': 3,
          'type': 'decision',
          'round': 1,
          'phase': 'act',
          'seat_id': 'seat_1',
          'visibility': 'public',
          'ts': 7000,
          'offset_ms': 6000,
          'payload': <String, dynamic>{
            'seat_id': 'seat_1',
            'action_id': 'a1',
            'summaries': <String, dynamic>{},
            'official': false,
          },
        },
      ],
    };

/// An `omniscient_comparison` `View` with a `RoundComparison`.
Map<String, dynamic> hubOmniscientView() => <String, dynamic>{
      'mode': 'omniscient_comparison',
      'archive_id': 'replay_ab12',
      'session_id': 'sess_1',
      'event_count': 42,
      'rounds': <dynamic>[
        <String, dynamic>{
          'round': 2,
          'agreement': 0.75,
          'synergy': 0.5,
          'delta': <String, dynamic>{'trust': 1.5},
          'decisions': <dynamic>[
            <String, dynamic>{
              'seat_id': 'seat_0',
              'action_id': 'cooperate',
              'official': false,
              'summaries': <String, dynamic>{},
              'state_snapshot': <String, dynamic>{},
            },
            <String, dynamic>{
              'seat_id': 'seat_1',
              'action_id': 'defect',
              'official': true,
              'state_snapshot': <String, dynamic>{},
            },
          ],
        },
      ],
    };

/// A `key_moments` `View` with a `Moment`.
Map<String, dynamic> hubKeyMomentsView() => <String, dynamic>{
      'mode': 'key_moments',
      'archive_id': 'replay_ab12',
      'session_id': 'sess_1',
      'event_count': 42,
      'moments': <dynamic>[
        <String, dynamic>{
          'event_id': 9,
          'round': 3,
          'kind': 'branch',
          'magnitude': 12.0,
          'reason': 'branch b1 fired, shifting 2 metric(s) by 12.0 total',
          'ref': 'b1',
        },
        <String, dynamic>{
          'event_id': 40,
          'round': 6,
          'kind': 'outcome',
          'magnitude': 0,
          'reason': 'match ended with outcome "accord" at round 6',
        },
      ],
    };

/// A `same_spec_comparison` `View` carrying a `RepriseComparison`.
Map<String, dynamic> hubSameSpecView() => <String, dynamic>{
      'mode': 'same_spec_comparison',
      'archive_id': 'replay_ab12',
      'session_id': 'sess_1',
      'comparison': repriseComparisonJson,
    };

/// Every read this slice is allowed to issue (transcribed from PanelsApi's
/// path constants and hub/api/*.go GET registrations).
bool isPanelsReadOnlyGetPath(String path) {
  if (!path.startsWith('/hub/')) return false;
  return path == PanelsApi.pathArchives ||
      path == PanelsApi.pathAudit ||
      path == PanelsApi.pathAuditVerify ||
      RegExp(r'^/hub/companions/[^/]+/(permissions|budget)$').hasMatch(path) ||
      RegExp(r'^/hub/archives/[^/]+$').hasMatch(path) ||
      RegExp(r'^/hub/archives/[^/]+/verify$').hasMatch(path) ||
      RegExp(r'^/hub/archives/[^/]+/views/[^/]+$').hasMatch(path) ||
      RegExp(r'^/hub/archives/[^/]+/model-consistency$').hasMatch(path) ||
      RegExp(r'^/hub/archives/[^/]+/reprises$').hasMatch(path) ||
      RegExp(r'^/hub/reprises/[^/]+$').hasMatch(path);
}

/// True for the write/consume routes the panels must NEVER call.
bool isPanelsWritePath(String path) => path.contains('/consent') ||
    path.contains('/publish') ||
    path.endsWith('/reprise') ||
    path.endsWith('/complete') ||
    path.contains('/approvals') ||
    path.endsWith('/authorize') ||
    path.contains('/budget/usage') ||
    path.contains('/sessions/');

void main() {
  group('adapter maps a realistic full View', () {
    test('round / phase / view_mode are mapped; archive id and mode kept', () {
      final ArchiveReplay replay = adaptArchiveView(hubFullView());
      expect(replay.frames.length, 3);
      expect(replay.archiveId, 'replay_ab12');
      expect(replay.viewMode, 'full');
      expect(replay.divergenceRound, isNull);

      expect(replay.frames[0].round, 0);
      expect(replay.frames[0].phase, 'observe');
      expect(replay.frames[0].viewMode, 'full');
      expect(replay.frames[1].round, 1);
      expect(replay.frames[1].phase, 'discuss');
      expect(replay.frames[2].phase, 'act');
    });

    test('never fabricates a speaker, a seat state or a winner', () {
      final ArchiveReplay replay = adaptArchiveView(hubFullView());
      for (final frame in replay.frames) {
        // The Hub records events[].seat_id (an actor), NOT a current speaker.
        expect(frame.activeSpeakerId, isNull);
        // No ballots / reveal / winner / tally are recorded in any View.
        expect(frame.votes, isEmpty);
        expect(frame.reveal, isNull);
        // No seat roster or seat state for a timeline event.
        expect(frame.seats, isEmpty);
        // total_rounds / title / light_grade are not in any View.
        expect(frame.totalRounds, isNull);
        expect(frame.title, isNull);
        expect(frame.lightGrade, isNull);
      }
    });

    test('archiveViewToFrames convenience returns the same timeline', () {
      expect(archiveViewToFrames(hubFullView()).length,
          adaptArchiveView(hubFullView()).frames.length);
    });
  });

  group('omniscient / key_moments / same_spec projections', () {
    test('omniscient maps decisions to seats (id + official) but no state', () {
      final ArchiveReplay replay = adaptArchiveView(hubOmniscientView());
      expect(replay.frames.length, 1);
      final frame = replay.frames.single;
      expect(frame.round, 2);
      expect(frame.phase, isNull); // RoundComparison has no phase field
      expect(frame.seats.length, 2);
      expect(frame.seats[0].id, 'seat_0');
      expect(frame.seats[0].isOfficial, false);
      expect(frame.seats[1].id, 'seat_1');
      expect(frame.seats[1].isOfficial, true);
      // A seat state / name / persona is NOT invented.
      for (final seat in frame.seats) {
        expect(seat.state, isNull);
        expect(seat.displayName, isNull);
        expect(seat.personaId, isNull);
      }
      // action_id / agreement / synergy / delta have no TheatreFrame slot.
      expect(frame.votes, isEmpty);
      expect(frame.reveal, isNull);
    });

    test('key_moments maps Moment.reason verbatim into summary', () {
      final ArchiveReplay replay = adaptArchiveView(hubKeyMomentsView());
      expect(replay.frames.length, 2);
      expect(replay.frames[0].round, 3);
      expect(replay.frames[0].summary,
          'branch b1 fired, shifting 2 metric(s) by 12.0 total');
      expect(replay.frames[1].round, 6);
      expect(replay.frames[1].summary,
          'match ended with outcome "accord" at round 6');
      expect(replay.frames[0].phase, isNull);
      expect(replay.frames[0].seats, isEmpty);
      // kind / magnitude / ref / event_id are recorded but unmapped (no slot).
      expect(replay.frames[0].reveal, isNull);
    });

    test('same_spec maps comparison.rounds to frames and exposes divergence', () {
      final ArchiveReplay replay = adaptArchiveView(hubSameSpecView());
      expect(replay.divergenceRound, 3);
      // repriseComparisonJson has one divergence round with one seat diff.
      expect(replay.frames.length, 1);
      final frame = replay.frames.single;
      expect(frame.round, 3);
      expect(frame.viewMode, 'same_spec_comparison');
      expect(frame.seats.length, 1);
      expect(frame.seats.single.id, 'seat_1');
      // No winner / state is fabricated from outcome_changed or actions.
      expect(frame.reveal, isNull);
      expect(frame.seats.single.state, isNull);
    });
  });

  group('six view modes', () {
    test('kArchiveViewModes lists exactly the six GDD modes', () {
      expect(kArchiveViewModes.length, 6);
      expect(kArchiveViewModes.toSet().length, 6);
      expect(kArchiveViewModes, ArchiveModes.all);
      expect(kArchiveViewModes, <String>[
        'full',
        'accelerated',
        'single_character',
        'omniscient_comparison',
        'key_moments',
        'same_spec_comparison',
      ]);
    });

    test('each of the six modes yields a non-empty adapted timeline', () {
      for (final String mode in kArchiveViewModes) {
        final Map<String, dynamic> view =
            viewJsonForPath(PanelsApi.viewPath('replay_ab12', mode));
        final ArchiveReplay replay = adaptArchiveView(view);
        expect(replay.hasFrames, isTrue, reason: 'mode $mode produced no frames');
        expect(replay.viewMode, mode);
      }
    });

    test('an unknown / missing mode still maps genuinely recorded collections',
        () {
      final ArchiveReplay replay = adaptArchiveView(<String, dynamic>{
        'mode': 'some_future_mode',
        'archive_id': 'x',
        'events': hubFullView()['events'],
      });
      expect(replay.frames.length, 3);
      expect(replay.viewMode, 'some_future_mode');
      final ArchiveReplay noMode = adaptArchiveView(<String, dynamic>{
        'archive_id': 'x',
        'events': hubFullView()['events'],
      });
      expect(noMode.frames.length, 3);
      expect(noMode.viewMode, isNull); // not recorded
    });
  });

  group('divergence round extraction', () {
    test('a positive divergence_point is surfaced', () {
      expect(adaptArchiveView(<String, dynamic>{
        'comparison': <String, dynamic>{'divergence_point': 4},
      }).divergenceRound, 4);
    });

    test('a recorded 0 means "never diverged" -> no marker', () {
      expect(adaptArchiveView(<String, dynamic>{
        'comparison': <String, dynamic>{'divergence_point': 0},
      }).divergenceRound, isNull);
    });

    test('a numeric-string divergence_point is tolerated', () {
      expect(adaptArchiveView(<String, dynamic>{
        'comparison': <String, dynamic>{'divergence_point': '5'},
      }).divergenceRound, 5);
    });

    test('absent / wrong-typed / negative divergence_point stays not recorded',
        () {
      expect(adaptArchiveView(<String, dynamic>{'comparison': null})
          .divergenceRound, isNull);
      expect(adaptArchiveView(<String, dynamic>{
        'comparison': <String, dynamic>{},
      }).divergenceRound, isNull);
      expect(adaptArchiveView(<String, dynamic>{
        'comparison': <String, dynamic>{'divergence_point': 'oops'},
      }).divergenceRound, isNull);
      expect(adaptArchiveView(<String, dynamic>{
        'comparison': <String, dynamic>{'divergence_point': -2},
      }).divergenceRound, isNull);
      expect(adaptArchiveView(<String, dynamic>{'mode': 'full'})
          .divergenceRound, isNull);
    });
  });

  group('empty and null payloads', () {
    test('null / non-map / scalar / list payloads yield an empty timeline', () {
      for (final Object? payload in <Object?>[
        null,
        <String, dynamic>{},
        'a string',
        42,
        <dynamic>[],
        <dynamic>[1, 2, 3],
        true,
      ]) {
        final ArchiveReplay replay = adaptArchiveView(payload);
        expect(replay.frames, isEmpty, reason: 'payload $payload');
        expect(replay.hasFrames, isFalse);
        expect(replay.divergenceRound, isNull);
      }
    });

    test('a View with no projection collections yields no frames', () {
      final ArchiveReplay replay = adaptArchiveView(<String, dynamic>{
        'mode': 'full',
        'archive_id': 'replay_ab12',
        'session_id': 'sess_1',
        'event_count': 0,
      });
      expect(replay.frames, isEmpty);
      expect(replay.archiveId, 'replay_ab12');
      expect(replay.viewMode, 'full');
    });

    test('null and empty events (Go nil slice / empty slice) yield no frames',
        () {
      expect(adaptArchiveView(<String, dynamic>{'events': null}).frames, isEmpty);
      expect(adaptArchiveView(<String, dynamic>{'events': <dynamic>[]}).frames,
          isEmpty);
    });

    test('an empty comparison.rounds yields no frames but keeps divergence',
        () {
      final ArchiveReplay replay = adaptArchiveView(<String, dynamic>{
        'mode': 'same_spec_comparison',
        'comparison': <String, dynamic>{
          'divergence_point': 3,
          'rounds': <dynamic>[],
        },
      });
      expect(replay.frames, isEmpty);
      expect(replay.divergenceRound, 3);
    });
  });

  group('missing / unknown / wrong-typed fields', () {
    test('wrong-typed scalars become null (not recorded), never a default',
        () {
      final ArchiveReplay replay = adaptArchiveView(<String, dynamic>{
        'mode': 'full',
        'events': <dynamic>[
          <String, dynamic>{
            'round': 'not-a-number',
            'phase': 123,
            'seat_id': <dynamic>[1, 2],
          },
        ],
      });
      expect(replay.frames.length, 1);
      expect(replay.frames.single.round, isNull);
      expect(replay.frames.single.phase, isNull);
      expect(replay.frames.single.activeSpeakerId, isNull);
      expect(replay.frames.single.viewMode, 'full');
    });

    test('a wrong-typed official stays not recorded, not false', () {
      final ArchiveReplay replay = adaptArchiveView(<String, dynamic>{
        'mode': 'omniscient_comparison',
        'rounds': <dynamic>[
          <String, dynamic>{
            'round': 1,
            'decisions': <dynamic>[
              <String, dynamic>{'seat_id': 'seat_0', 'official': 'yes'},
              <String, dynamic>{'seat_id': 99, 'official': true},
            ],
          },
        ],
      });
      final seats = replay.frames.single.seats;
      expect(seats.length, 2);
      expect(seats[0].isOfficial, isNull); // wrong type -> not recorded
      expect(seats[0].state, isNull);
      expect(seats[1].id, ''); // non-string seat id -> empty = not recorded
    });

    test('non-map entries in a collection are skipped, not fabricated', () {
      final ArchiveReplay replay = adaptArchiveView(<String, dynamic>{
        'mode': 'full',
        'events': <dynamic>[
          42,
          'x',
          null,
          <String, dynamic>{'round': 2, 'phase': 'act'},
        ],
      });
      expect(replay.frames.length, 1);
      expect(replay.frames.single.round, 2);
    });

    test('a non-list events value yields no frames', () {
      expect(
          adaptArchiveView(<String, dynamic>{'events': 'oops'}).frames, isEmpty);
    });

    test('a wrong-typed map key set is still decoded defensively', () {
      final Map<dynamic, dynamic> weird = <dynamic, dynamic>{
        'mode': 'full',
        'archive_id': 'replay_ab12',
        'events': <dynamic>[
          <dynamic, dynamic>{'round': 1, 'phase': 'act'},
        ],
        7: 'ignored non-string key',
      };
      final ArchiveReplay replay = adaptArchiveView(weird);
      expect(replay.frames.length, 1);
      expect(replay.frames.single.round, 1);
      expect(replay.archiveId, 'replay_ab12');
    });
  });

  group('PanelsApi read-only GET proof', () {
    test('fetchViewJson returns the FULL raw View on the documented GET path',
        () async {
      final ScriptedFetcher fetcher = ScriptedFetcher();
      final PanelsApi api = fetcher.api();

      final Map<String, dynamic> raw = await api.fetchViewJson(
        'replay_ab12',
        ArchiveModes.sameSpecComparison,
        query: <String, dynamic>{'compare_with': 'replay_cd34'},
      );

      expect(fetcher.paths.single,
          PanelsApi.viewPath('replay_ab12', ArchiveModes.sameSpecComparison));
      expect(fetcher.paths.single,
          '/hub/archives/replay_ab12/views/same_spec_comparison');
      expect(fetcher.queries.single,
          <String, dynamic>{'compare_with': 'replay_cd34'});
      // It is the raw wire document, NOT the lossy typed ArchiveView: the
      // comparison and its divergence_point are still present.
      expect(raw['mode'], ArchiveModes.sameSpecComparison);
      expect(raw['comparison'], isA<Map>());
      expect((raw['comparison'] as Map)['divergence_point'], 3);
    });

    test('fetchViewJson keeps events[].phase that the typed model drops',
        () async {
      final Map<String, dynamic> raw = await ScriptedFetcher()
          .api()
          .fetchViewJson('replay_ab12', ArchiveModes.full);
      final List<dynamic> events = raw['events'] as List<dynamic>;
      expect(events.first['phase'], 'setup');
      expect(events.first['payload'], isA<Map>());
      expect(adaptArchiveView(raw).frames.length, events.length);
    });

    test('the replay read flow requests ONLY read-only GET paths', () async {
      final ScriptedFetcher fetcher = ScriptedFetcher();
      final PanelsApi api = fetcher.api();
      await api.fetchArchives();
      await api.loadArchiveBundle(
        archiveId: 'replay_ab12',
        mode: ArchiveModes.full,
      );
      final Map<String, dynamic> raw =
          await api.fetchViewJson('replay_ab12', ArchiveModes.full);
      adaptArchiveView(raw); // pure function, performs no network call

      expect(fetcher.paths, isNotEmpty);
      for (final String path in fetcher.paths) {
        expect(isPanelsReadOnlyGetPath(path), isTrue,
            reason: '$path is not a known read-only GET route');
        expect(isPanelsWritePath(path), isFalse,
            reason: '$path is a write/consume route that must never be called');
      }
      // The chosen view is fetched through exactly the documented GET path.
      expect(
        fetcher.paths.where((String p) => p.contains('/views/')).toSet(),
        <String>{PanelsApi.viewPath('replay_ab12', ArchiveModes.full)},
      );
    });
  });
}
