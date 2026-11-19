import 'package:flutter/material.dart';

import 'pages/companions_page.dart';
import 'pages/discover_page.dart';
import 'pages/home_page.dart';
import 'pages/theatre_page.dart';

/// Route table for the four-tab bottom navigation (story 001 AC4).
class AppRoutes {
  const AppRoutes._();

  static const String home = '/';
  static const String theatre = '/theatre';
  static const String companions = '/companions';
  static const String discover = '/discover';

  static const List<String> order = <String>[
    home,
    theatre,
    companions,
    discover,
  ];

  static const Map<String, String> labels = <String, String>{
    home: 'Home',
    theatre: 'Theatre',
    companions: 'Companions',
    discover: 'Discover',
  };

  static Widget pageFor(String route) {
    switch (route) {
      case theatre:
        return const TheatrePage();
      case companions:
        return const CompanionsPage();
      case discover:
        return const DiscoverPage();
      case home:
      default:
        return const HomePage();
    }
  }
}

class AppShell extends StatefulWidget {
  const AppShell({super.key, this.initialIndex = 0});

  final int initialIndex;

  @override
  State<AppShell> createState() => _AppShellState();
}

class _AppShellState extends State<AppShell> {
  late int _index = widget.initialIndex;

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      body: IndexedStack(
        index: _index,
        children: <Widget>[
          for (final String route in AppRoutes.order) AppRoutes.pageFor(route),
        ],
      ),
      bottomNavigationBar: BottomNavigationBar(
        type: BottomNavigationBarType.fixed,
        currentIndex: _index,
        onTap: (int i) => setState(() => _index = i),
        items: <BottomNavigationBarItem>[
          for (final String route in AppRoutes.order)
            BottomNavigationBarItem(
              icon: Icon(_iconFor(route)),
              label: AppRoutes.labels[route],
            ),
        ],
      ),
    );
  }

  IconData _iconFor(String route) {
    switch (route) {
      case AppRoutes.theatre:
        return Icons.theater_comedy_outlined;
      case AppRoutes.companions:
        return Icons.people_outline;
      case AppRoutes.discover:
        return Icons.explore_outlined;
      default:
        return Icons.home_outlined;
    }
  }
}
