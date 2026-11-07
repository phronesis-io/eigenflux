import 'package:flutter/material.dart';

class CompanionsPage extends StatelessWidget {
  const CompanionsPage({super.key});

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(title: const Text('Companions')),
      body: const Center(
        child: Text('Up to four companions, their memories and relationships.'),
      ),
    );
  }
}
