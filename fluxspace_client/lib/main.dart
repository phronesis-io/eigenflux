import 'package:flutter/material.dart';

import 'routes.dart';

void main() => runApp(const FluxSpaceApp());

/// Root widget. Riverpod's ProviderScope is added when stateful features land
/// in slice 2; the scaffold itself stays dependency-free and testable.
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
