/// The single drop-in widget the lead wires into the app.
///
/// It composes [GovernancePanel] (S5) and [ArchivePanel] (S4) behind a
/// two-tab [TabBar]. Everything is read-only: no endpoint that mutates
/// governance or archive state is ever called.
///
/// Typical integration (baseUrl and auth headers are set on the [Dio] by the
/// caller — this slice never sets a base URL or a token itself):
///
/// ```dart
/// final PanelsApi api = PanelsApi(
///   dio: Dio(BaseOptions(
///     baseUrl: 'http://127.0.0.1:8095',
///     headers: <String, dynamic>{'Authorization': 'Bearer <owner token>'},
///   )),
/// );
///
/// const PanelsScreen(
///   api: api,                    // or: dio: myConfiguredDio
///   companionId: 'comp_alpha',
///   archiveId: 'replay_ab12',   // optional; gates model-consistency + bundle
///   compareWith: 'replay_cd34', // optional; 2nd archive for same-spec diff
/// )
/// ```
///
/// Constructor (exact signature):
/// ```dart
/// const PanelsScreen({
///   Key? key,
///   PanelsApi? api,       // provide this OR dio
///   Dio? dio,             // optional; a PanelsApi is built around it
///   String companionId = '',
///   String archiveId = '',
///   String compareWith = '',
/// })
/// ```
/// At least one of [api] / [dio] must be supplied (asserted in debug).
library;

import 'package:dio/dio.dart';
import 'package:flutter/material.dart';

import 'archive_panel.dart';
import 'governance_panel.dart';
import 'panels_api.dart';

class PanelsScreen extends StatefulWidget {
  const PanelsScreen({
    super.key,
    this.api,
    this.dio,
    this.companionId = '',
    this.archiveId = '',
    this.compareWith = '',
  }) : assert(api != null || dio != null,
            'PanelsScreen requires either a PanelsApi or a Dio.');

  /// The read-only API. Provide this directly (recommended; makes tests
  /// network-free via an injected fetcher), or omit it and pass a [dio].
  final PanelsApi? api;

  /// Optional configured Dio (baseUrl + auth headers). Used only when [api]
  /// is null, to build a [PanelsApi] over it.
  final Dio? dio;

  /// Companion whose budget / permissions / audit are shown. Empty renders as
  /// "not recorded" and still probes the endpoints (the integrator should
  /// supply the bound companion id).
  final String companionId;

  /// Optional pre-selected archive id. Gates the archive-scoped model-version
  /// consistency card and the archive panel's initial selection.
  final String archiveId;

  /// Second archive id for the `same_spec_comparison` view's `compare_with`
  /// query parameter (needed to compute a divergence point).
  final String compareWith;

  @override
  State<PanelsScreen> createState() => _PanelsScreenState();
}

class _PanelsScreenState extends State<PanelsScreen>
    with SingleTickerProviderStateMixin {
  late final PanelsApi _api;
  late final TabController _tabs;
  int _index = 0;

  @override
  void initState() {
    super.initState();
    _api = widget.api ?? PanelsApi(dio: widget.dio ?? Dio());
    _tabs = TabController(length: 2, vsync: this);
    _tabs.addListener(_onTabChanged);
  }

  void _onTabChanged() {
    if (!_tabs.indexIsChanging && _index != _tabs.index) {
      setState(() => _index = _tabs.index);
    }
  }

  @override
  void dispose() {
    // Dispose the controller we own; the panels own their own state.
    _tabs.removeListener(_onTabChanged);
    _tabs.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(
        title: const Text('Flux Theatre · Observability'),
        bottom: TabBar(
          controller: _tabs,
          tabs: const <Widget>[
            Tab(icon: Icon(Icons.gavel), text: 'Governance'),
            Tab(icon: Icon(Icons.archive), text: 'Archive'),
          ],
        ),
      ),
      body: IndexedStack(
        index: _index,
        sizing: StackFit.expand,
        children: <Widget>[
          GovernancePanel(
            api: _api,
            companionId: widget.companionId,
            archiveId: widget.archiveId,
          ),
          ArchivePanel(
            api: _api,
            archiveId: widget.archiveId,
            compareWith: widget.compareWith,
          ),
        ],
      ),
    );
  }
}
