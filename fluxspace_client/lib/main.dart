import 'package:flutter/material.dart';

import 'routes.dart';

void main() => runApp(const FluxSpaceApp());

/// Root widget.
///
/// No `ProviderScope` yet: the only stateful flow is one match, and it lives in
/// a [SessionController] owned by the theatre page. Riverpod is in the
/// dependency set for the cross-page state that arrives with companion
/// management; wrapping the app in a scope before then would add a layer with
/// nothing in it.
class FluxSpaceApp extends StatelessWidget {
  const FluxSpaceApp({super.key});

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: 'FluxSpace',
      debugShowCheckedModeBanner: false,
      theme: ThemeData(
        colorScheme: ColorScheme.fromSeed(seedColor: const Color(0xFF7C5CF0)),
        useMaterial3: true,
      ),
      home: const AppShell(),
    );
  }
}
