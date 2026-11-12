import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:fluxspace_client/theatre/ai_art_assets.dart';
import 'package:fluxspace_client/theatre/theatre_config.dart';

void main() {
  testWidgets('AiArtAssets renders every method without throwing',
      (WidgetTester tester) async {
    const AiArtAssets assets = AiArtAssets();
    await tester.pumpWidget(MaterialApp(
      home: Material(
        child: SingleChildScrollView(
          child: Column(
            children: <Widget>[
              SizedBox(width: 200, height: 120, child: assets.backdrop()),
              SizedBox(width: 96, height: 96, child: assets.portrait('lin_shen', 96)),
              SizedBox(width: 96, height: 96, child: assets.portrait('su-wan', 96)),
              SizedBox(width: 96, height: 96, child: assets.portrait(null, 96)),
              SizedBox(width: 96, height: 96, child: assets.portrait('mystery', 96)),
              SizedBox(width: 120, height: 120, child: assets.spotlight(0.7, false)),
              SizedBox(width: 120, height: 120, child: assets.spotlight(0.7, true)),
              SizedBox(width: 120, height: 120, child: assets.voteFlourish(0.5)),
              SizedBox(width: 120, height: 120, child: assets.ambient('warm')),
              SizedBox(width: 120, height: 120, child: assets.ambient(null)),
            ],
          ),
        ),
      ),
    ));
    await tester.pumpAndSettle();
    expect(tester.takeException(), isNull);
  });

  testWidgets('known persona id resolves to an Image asset', (WidgetTester tester) async {
    const AiArtAssets assets = AiArtAssets();
    await tester.pumpWidget(MaterialApp(
      home: SizedBox(width: 64, height: 64, child: assets.portrait('lin_shen', 64)),
    ));
    await tester.pumpAndSettle();
    expect(find.byType(Image), findsOneWidget);
  });

  testWidgets('unknown persona id resolves to a stable portrait without throwing',
      (WidgetTester tester) async {
    const AiArtAssets assets = AiArtAssets();
    await tester.pumpWidget(MaterialApp(
      home: SizedBox(width: 64, height: 64, child: assets.portrait('mystery', 64)),
    ));
    await tester.pumpAndSettle();
    // Real matches emit opaque companion ids (not the four character keys), so
    // unknown ids are hashed to a stable portrait instead of a missing asset.
    expect(tester.takeException(), isNull);
    expect(find.byType(Image), findsOneWidget);
  });

  testWidgets('null persona id shows the neutral fallback', (WidgetTester tester) async {
    const AiArtAssets assets = AiArtAssets();
    await tester.pumpWidget(MaterialApp(
      home: SizedBox(width: 64, height: 64, child: assets.portrait(null, 64)),
    ));
    await tester.pumpAndSettle();
    expect(tester.takeException(), isNull);
    expect(find.byType(Image), findsNothing);
  });

  test('defaultTheatreAssets honours the interim flag', () {
    expect(defaultTheatreAssets is AiArtAssets, kUseAiArtInterim);
  });
}
