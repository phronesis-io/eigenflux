import 'package:flutter/material.dart';

import '../setup/setup_result_bus.dart';
import '../setup/setup_state.dart';
import '../setup/setup_wizard.dart';

class HomePage extends StatelessWidget {
  const HomePage({super.key});

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(
        title: const Text('FluxSpace'),
        actions: <Widget>[
          IconButton(
            tooltip: 'Set up Hub',
            icon: const Icon(Icons.settings_ethernet_outlined),
            onPressed: () => _openSetup(context),
          ),
        ],
      ),
      body: Center(
        child: SingleChildScrollView(
          padding: const EdgeInsets.all(24),
          child: Column(
            mainAxisSize: MainAxisSize.min,
            children: <Widget>[
              const Text('Recaps and followed-character threads appear here.'),
              const SizedBox(height: 16),
              // The primary, clearly visible entry point: a first-run user
              // should not have to know Hub URLs or bearer tokens.
              FilledButton.icon(
                onPressed: () => _openSetup(context),
                icon: const Icon(Icons.settings_ethernet_outlined),
                label: const Text('Set up Hub'),
              ),
            ],
          ),
        ),
      ),
    );
  }

  Future<void> _openSetup(BuildContext context) async {
    // The wizard returns the saved configuration; publish it on the shared bus
    // so TheatrePage (a sibling under the IndexedStack) can pick it up.
    final SetupResult? result = await SetupWizard.open(context);
    if (result == null || !context.mounted) return;
    SetupResultBus.instance.publish(result);
    ScaffoldMessenger.of(context).showSnackBar(
      SnackBar(
        content: Text('Hub configured: ${result.hubUrl} (auth: ${result.authLabel})'),
      ),
    );
  }
}
