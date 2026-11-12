/// Widget tests for the "Play replay" action in [ArchivePanel].
///
/// They cover button gating (selected archive + non-empty frames), the empty /
/// "not recorded" state, tapping opens [TheatreScreen] with the right frame
/// count / divergence round / title, no overflow at 800x600, and the proof that
/// the whole flow requests only read-only GET paths. All data comes from the
/// injected [ScriptedFetcher]; there is no network.
library;

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:fluxspace_client/panels/archive_panel.dart';
import 'package:fluxspace_client/panels/panels_api.dart';
import 'package:fluxspace_client/panels/panels_models.dart';
import 'package:fluxspace_client/theatre/theatre_screen.dart';

import 'support/panels_fakes.dart';

const Key _playKey = Key('play_replay_button');

Widget _app(PanelsApi api, {String archiveId = '', String compareWith = ''}) =>
    MaterialApp(
      home: Scaffold(
        body: ArchivePanel(
          api: api,
          archiveId: archiveId,
          compareWith: compareWith,
        ),
      ),
    );

FilledButton _button(WidgetTester tester) =>
    tester.widget<FilledButton>(find.byKey(_playKey));

void main() {
  group('Play replay button gating', () {
    testWidgets('disabled when no archive is selected', (WidgetTester tester) async {
      final ScriptedFetcher fetcher = ScriptedFetcher(
        respond: (String path, Map<String, dynamic>? query) {
          if (path == PanelsApi.pathArchives) {
            return <String, dynamic>{'archives': <dynamic>[]};
          }
          return <String, dynamic>{};
        },
      );
      await tester.pumpWidget(_app(fetcher.api()));
      await tester.pumpAndSettle();

      expect(_button(tester).onPressed, isNull);
      expect(find.textContaining('no archive selected'), findsWidgets);
      expect(find.byType(TheatreScreen), findsNothing);
      expect(tester.takeException(), isNull);
    });

    testWidgets('disabled when the selected view recorded no frames',
        (WidgetTester tester) async {
      final ScriptedFetcher fetcher = ScriptedFetcher(
        respond: (String path, Map<String, dynamic>? query) {
          if (path == PanelsApi.pathArchives) return archivesJson;
          if (path.contains('/views/')) {
            return <String, dynamic>{
              'mode': ArchiveModes.full,
              'archive_id': 'replay_ab12',
              'session_id': 'sess_1',
              'event_count': 0,
              'events': <dynamic>[],
            };
          }
          if (path.endsWith('/verify')) return archiveVerifyJson;
          if (path.endsWith('/reprises')) return reprisesJson;
          return detailJson;
        },
      );
      await tester.pumpWidget(_app(fetcher.api(), archiveId: 'replay_ab12'));
      await tester.pumpAndSettle();

      expect(_button(tester).onPressed, isNull);
      expect(find.textContaining('no replay frames'), findsWidgets);
      expect(find.byType(TheatreScreen), findsNothing);
      expect(tester.takeException(), isNull);
    });

    testWidgets('enabled once a selected archive has a non-empty frame list',
        (WidgetTester tester) async {
      await tester.pumpWidget(
          _app(ScriptedFetcher().api(), archiveId: 'replay_ab12'));
      await tester.pumpAndSettle();

      expect(_button(tester).onPressed, isNotNull);
      // The canned full view has 2 timeline events -> 2 frames.
      expect(find.text('2 frame(s)'), findsOneWidget);
    });
  });

  group('tapping opens the existing TheatreScreen replay', () {
    testWidgets('full view: right frame count and archive-id title',
        (WidgetTester tester) async {
      await tester.pumpWidget(
          _app(ScriptedFetcher().api(), archiveId: 'replay_ab12'));
      await tester.pumpAndSettle();

      await tester.tap(find.byKey(_playKey));
      await tester.pumpAndSettle();

      expect(find.byType(TheatreScreen), findsOneWidget);
      final TheatreScreen screen =
          tester.widget<TheatreScreen>(find.byType(TheatreScreen));
      expect(screen.frames, isNotNull);
      expect(screen.frames!.length, 2);
      expect(screen.title, 'replay_ab12');
      // No same-spec comparison in a full view -> no divergence marker.
      expect(screen.divergenceRound, isNull);
      // The stage renders recorded round/phase data and no fabricated speaker.
      expect(find.textContaining('no seats recorded'), findsWidgets);
      expect(tester.takeException(), isNull);
    });

    testWidgets('same_spec view: divergence round is passed through',
        (WidgetTester tester) async {
      await tester.pumpWidget(_app(
        ScriptedFetcher().api(),
        archiveId: 'replay_ab12',
        compareWith: 'replay_cd34',
      ));
      await tester.pumpAndSettle();

      await tester.tap(
          find.text(ArchiveModes.label(ArchiveModes.sameSpecComparison)));
      await tester.pumpAndSettle();
      expect(find.textContaining('divergence round 3'), findsOneWidget);

      await tester.tap(find.byKey(_playKey));
      await tester.pumpAndSettle();

      expect(find.byType(TheatreScreen), findsOneWidget);
      final TheatreScreen screen =
          tester.widget<TheatreScreen>(find.byType(TheatreScreen));
      expect(screen.divergenceRound, 3);
      expect(screen.frames, isNotNull);
      expect(screen.frames, isNotEmpty);
      expect(tester.takeException(), isNull);
    });
  });

  group('read-only GET proof (full panel flow)', () {
    testWidgets('only the documented GET read paths are ever requested',
        (WidgetTester tester) async {
      final ScriptedFetcher fetcher = ScriptedFetcher();
      await tester.pumpWidget(_app(fetcher.api(), archiveId: 'replay_ab12'));
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(_playKey));
      await tester.pumpAndSettle();

      // The complete set of requested paths: every one is a registered GET.
      expect(fetcher.paths.toSet(), <String>{
        '/hub/archives',
        '/hub/archives/replay_ab12',
        '/hub/archives/replay_ab12/verify',
        '/hub/archives/replay_ab12/views/full',
        '/hub/archives/replay_ab12/reprises',
      });

      // And none is a write/consume route (consent, publish, reprise creation,
      // approval consumption, budget usage, session archiving, completion).
      const List<String> writeMarkers = <String>[
        '/consent',
        '/publish',
        '/approvals',
        '/authorize',
        '/budget/usage',
        '/sessions/',
      ];
      for (final String path in fetcher.paths) {
        for (final String marker in writeMarkers) {
          expect(path.contains(marker), isFalse,
              reason: '$path used a write route ($marker)');
        }
        expect(path.endsWith('/reprise'), isFalse);
        expect(path.endsWith('/complete'), isFalse);
      }
      expect(tester.takeException(), isNull);
    });
  });

  group('overflow safety', () {
    testWidgets('no RenderFlex overflow at 800x600 with the replay card',
        (WidgetTester tester) async {
      tester.view.physicalSize = const Size(800, 600);
      tester.view.devicePixelRatio = 1.0;
      addTearDown(() => tester.view.reset());

      await tester.pumpWidget(
          _app(ScriptedFetcher().api(), archiveId: 'replay_ab12'));
      await tester.pumpAndSettle();
      expect(tester.takeException(), isNull);

      await tester.tap(find.byKey(_playKey));
      await tester.pumpAndSettle();
      // Wrap/Flexible layout survives a narrow viewport in both the panel and
      // the pushed TheatreScreen.
      expect(find.byType(TheatreScreen), findsOneWidget);
      expect(tester.takeException(), isNull);
    });
  });
}
