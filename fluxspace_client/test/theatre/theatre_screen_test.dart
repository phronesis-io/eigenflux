import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:fluxspace_client/theatre/placeholder_assets.dart';
import 'package:fluxspace_client/theatre/postmatch_replay.dart';
import 'package:fluxspace_client/theatre/seat_card.dart';
import 'package:fluxspace_client/theatre/stage_view.dart';
import 'package:fluxspace_client/theatre/theatre_models.dart';
import 'package:fluxspace_client/theatre/theatre_screen.dart';
import 'package:fluxspace_client/theatre/vote_spotlight.dart';

/// Builds a representative frame. [activeSpeakerId] is set explicitly so the
/// spotlight-follows test can drive it through a live ValueListenable.
TheatreFrame _frame({
  required int round,
  String? activeSpeakerId = 'seat_1',
  int seats = 4,
  TheatreReveal? reveal,
  String phase = 'discuss',
}) {
  return TheatreFrame(
    round: round,
    totalRounds: 5,
    phase: phase,
    title: 'The Locked Ward',
    summary: 'Round $round decision summary',
    viewMode: 'omniscient',
    lightGrade: 'cold',
    activeSpeakerId: activeSpeakerId,
    seats: <TheatreSeat>[
      for (int i = 0; i < seats; i++)
        TheatreSeat(
          id: 'seat_$i',
          displayName: 'Persona $i',
          personaId: 'persona-$i',
          state: TheatreSeatState.values[i % TheatreSeatState.values.length],
          isOfficial: i == 0 ? true : false,
        ),
    ],
    votes: const <TheatreVote>[
      TheatreVote(voterId: 'seat_0', targetId: 'seat_2'),
    ],
    reveal: reveal,
  );
}

/// Installs an error handler that collects RenderFlex overflow errors (so they
/// can be asserted explicitly) while forwarding every other error to the
/// framework's handler, which still fails the test on any real defect.
class _OverflowCapture {
  _OverflowCapture() {
    _previous = FlutterError.onError;
    FlutterError.onError = _handle;
  }

  late final FlutterExceptionHandler? _previous;
  final List<FlutterErrorDetails> overflow = <FlutterErrorDetails>[];

  void _handle(FlutterErrorDetails details) {
    if (details.exceptionAsString().toLowerCase().contains('overflow')) {
      overflow.add(details);
    } else {
      _previous?.call(details);
    }
  }

  void restore() => FlutterError.onError = _previous;
}

/// Uses the current (Flutter 3.47) test-view API; no deprecated
/// `window.physicalSizeTestValue` / `binding.window` access.
void _setSurfaceSize(WidgetTester tester, Size size) {
  tester.view.physicalSize = size;
  tester.view.devicePixelRatio = 1.0;
  addTearDown(tester.view.resetPhysicalSize);
  addTearDown(tester.view.resetDevicePixelRatio);
}

void main() {
  testWidgets('composed TheatreScreen smoke-renders with placeholder assets',
      (WidgetTester tester) async {
    await tester.pumpWidget(MaterialApp(
      home: TheatreScreen(
        frames: <TheatreFrame>[_frame(round: 1), _frame(round: 2)],
      ),
    ));
    await tester.pumpAndSettle();

    expect(find.byType(TheatreScreen), findsOneWidget);
    expect(find.byType(StageView), findsOneWidget);
    expect(find.byType(VoteSpotlight), findsOneWidget);
    expect(find.byType(PostmatchReplay), findsOneWidget);
    expect(find.text('The Locked Ward'), findsOneWidget);
    expect(find.text('placeholder art'), findsOneWidget);
    expect(tester.takeException(), isNull);
  });

  testWidgets('the stage renders exactly N seat cards', (WidgetTester tester) async {
    await tester.pumpWidget(MaterialApp(
      home: TheatreScreen(frames: <TheatreFrame>[_frame(round: 1, seats: 6)]),
    ));
    await tester.pumpAndSettle();

    expect(find.byType(SeatCard), findsNWidgets(6));
    // seat_0 has an official flag and a ballot, so both decorations exist.
    expect(find.text('Official'), findsOneWidget);
    expect(find.text('voted -> seat_2'), findsOneWidget);
  });

  testWidgets('the spotlight follows activeSpeakerId from a live ValueListenable',
      (WidgetTester tester) async {
    final ValueNotifier<TheatreFrame> notifier = ValueNotifier<TheatreFrame>(
      _frame(round: 1, activeSpeakerId: 'seat_1'),
    );
    addTearDown(notifier.dispose);

    await tester.pumpWidget(MaterialApp(home: TheatreScreen(frame: notifier)));
    await tester.pumpAndSettle();

    expect(find.byKey(const Key('active_speaker_seat_1')), findsOneWidget);
    expect(find.byKey(const Key('active_speaker_seat_2')), findsNothing);

    notifier.value = _frame(round: 2, activeSpeakerId: 'seat_2');
    await tester.pumpAndSettle();

    expect(find.byKey(const Key('active_speaker_seat_1')), findsNothing);
    expect(find.byKey(const Key('active_speaker_seat_2')), findsOneWidget);
  });

  testWidgets('the replay scrubber changes the displayed frame',
      (WidgetTester tester) async {
    // The composed screen is taller than the default 800x600 test viewport,
    // so give this functional test a comfortable height: the scrubber controls
    // stay on-screen and taps hit them directly (no scrolling needed).
    _setSurfaceSize(tester, const Size(1000, 1200));

    await tester.pumpWidget(MaterialApp(
      home: TheatreScreen(
        divergenceRound: 2,
        frames: <TheatreFrame>[
          _frame(round: 1),
          _frame(round: 2, activeSpeakerId: 'seat_3'),
          _frame(round: 3),
        ],
      ),
    ));
    await tester.pumpAndSettle();

    String roundText() =>
        tester.widget<Text>(find.byKey(const Key('current_round'))).data ?? '';
    expect(roundText(), contains('round 1'));

    await tester.tap(find.byKey(const Key('replay_next')));
    await tester.pumpAndSettle();
    expect(roundText(), contains('round 2'));
    // The selected frame also drives the stage's spotlight.
    expect(find.byKey(const Key('active_speaker_seat_3')), findsOneWidget);

    await tester.tap(find.byKey(const Key('replay_prev')));
    await tester.pumpAndSettle();
    expect(roundText(), contains('round 1'));

    // The divergence point is surfaced when supplied.
    expect(find.textContaining('divergence'), findsOneWidget);
  });

  testWidgets('PlaceholderAssets methods render without throwing',
      (WidgetTester tester) async {
    const PlaceholderAssets assets = PlaceholderAssets();
    await tester.pumpWidget(MaterialApp(
      home: Material(
        child: SingleChildScrollView(
          child: Column(
            children: <Widget>[
              _box(assets.backdrop(), 120, 120),
              _box(assets.portrait(null, 80), 80, 80),
              _box(assets.portrait('unknown-persona', 80), 80, 80),
              _box(assets.spotlight(0.5, true), 120, 80),
              _box(assets.voteFlourish(0.3), 120, 80),
              _box(assets.ambient('warm'), 120, 80),
              _box(assets.ambient(null), 120, 80),
              // Degenerate size must not throw either.
              _box(assets.portrait(null, 0), 0, 0),
              _box(assets.voteFlourish(0.0), 0, 0),
            ],
          ),
        ),
      ),
    ));
    await tester.pumpAndSettle();

    expect(tester.takeException(), isNull);
  });

  testWidgets('the overflow-capture harness detects a real RenderFlex overflow',
      (WidgetTester tester) async {
    // Meta-test: prove the "no overflow" assertions above are meaningful by
    // feeding the same capture a deliberately overflowing Row. Note the root
    // must give the Row *bounded* width: MaterialApp forces its home to fill
    // the whole surface, which would hide the overflow. Align gives the
    // SizedBox loose constraints, so the Row really is 200 wide with 600 of
    // children. If this stops finding the overflow, the two no-overflow tests
    // would be false passes rather than evidence.
    final _OverflowCapture capture = _OverflowCapture();
    addTearDown(capture.restore);
    await tester.pumpWidget(
      Directionality(
        textDirection: TextDirection.ltr,
        child: Align(
          alignment: Alignment.topLeft,
          child: SizedBox(
            width: 200,
            height: 50,
            child: Row(
              children: <Widget>[
                Container(width: 300, height: 20, color: Colors.red),
                Container(width: 300, height: 20, color: Colors.blue),
              ],
            ),
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();
    capture.restore();
    expect(capture.overflow, isNotEmpty);
    expect(tester.takeException(), isNull);
  });

  for (final Size size in <Size>[
    const Size(800, 600),
    const Size(1280, 720),
  ]) {
    testWidgets('no RenderFlex overflow at ${size.width}x${size.height}',
        (WidgetTester tester) async {
      _setSurfaceSize(tester, size);
      final _OverflowCapture capture = _OverflowCapture();
      addTearDown(capture.restore);

      // Stress the layout: 8 seats, a long title, a full reveal and a summary.
      await tester.pumpWidget(MaterialApp(
        home: TheatreScreen(
          title: 'A deliberately long match title used to stress the header',
          divergenceRound: 3,
          frames: <TheatreFrame>[
            _frame(
              round: 1,
              seats: 8,
              reveal: const TheatreReveal(
                winnerId: 'seat_2',
                tally: <String, int>{'seat_2': 3, 'seat_0': 2, 'seat_1': 1},
                note: 'Tally settled after the discuss phase',
              ),
            ),
            _frame(round: 2, seats: 8, activeSpeakerId: 'seat_7'),
            _frame(round: 3, seats: 8),
          ],
        ),
      ));
      await tester.pumpAndSettle();

      capture.restore();
      expect(
        capture.overflow,
        isEmpty,
        reason: 'RenderFlex overflow reported at '
            '${size.width}x${size.height}',
      );
      // Backstop: no other FlutterError either.
      expect(tester.takeException(), isNull);
    });
  }
}

Widget _box(Widget child, double width, double height) => SizedBox(
      width: width,
      height: height,
      child: child,
    );
