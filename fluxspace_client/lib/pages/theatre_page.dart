import 'package:flutter/material.dart';

class TheatrePage extends StatelessWidget {
  const TheatrePage({super.key});

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(title: const Text('Theatre')),
      body: const Center(
        child: Text('Omniscient 2.5D spectator view arrives in slice 3.'),
      ),
    );
  }
}
