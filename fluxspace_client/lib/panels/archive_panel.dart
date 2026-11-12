/// The S4 archive panel.
///
/// Visible: the archived-session list, the six-view projection selector, the
/// replay stream, the same-spec divergence point, the fail-closed consent
/// ledger, and `integrity.missing_fields` rendered as "not recorded".
///
/// Only read endpoints are called. A 503 `ARCHIVE_STORE_UNCONFIGURED` is an
/// explicit [DegradedBanner], never an empty list. Everything the Hub did not
/// record stays "not recorded".
library;

import 'package:flutter/material.dart' hide Badge;
import 'package:fluxspace_client/theatre/theatre_models.dart';
import 'package:fluxspace_client/theatre/theatre_screen.dart';
import 'package:fluxspace_client/theatre/theatre_config.dart';

import 'archive_frames.dart';
import 'panels_api.dart';
import 'panels_models.dart';
import 'panels_ui.dart';

class ArchivePanel extends StatefulWidget {
  /// [api] performs the read-only archive calls. [archiveId] optionally
  /// pre-selects one archive; otherwise the first returned archive is used.
  /// [compareWith] supplies the second archive id the `same_spec_comparison`
  /// view needs (its `compare_with` query parameter); when empty the panel
  /// picks another archive from the list, if one exists.
  const ArchivePanel({
    super.key,
    required this.api,
    this.archiveId = '',
    this.compareWith = '',
  });

  final PanelsApi api;
  final String archiveId;
  final String compareWith;

  @override
  State<ArchivePanel> createState() => _ArchivePanelState();
}

class _ArchivePanelState extends State<ArchivePanel> {
  Section<List<ArchiveSummary>>? _list;
  bool _loadingList = true;

  String _selectedId = '';
  String _mode = ArchiveModes.full;
  ArchiveBundle? _bundle;
  bool _loadingBundle = false;
  String? _firstSeatId;

  /// The replay timeline adapted from the selected view's FULL raw JSON.
  /// Null until a bundle has been loaded; an empty frame list is a genuine
  /// "not recorded" state that gates the Play replay button.
  ArchiveReplay? _replay;

  @override
  void initState() {
    super.initState();
    _loadList();
  }

  Future<void> _loadList() async {
    if (!mounted) return;
    setState(() => _loadingList = true);
    final Section<List<ArchiveSummary>> list = await widget.api.loadArchiveList();
    if (!mounted) return;
    final List<ArchiveSummary> items = list.value ?? const <ArchiveSummary>[];
    String initial = widget.archiveId;
    if (initial.isEmpty && items.isNotEmpty) initial = items.first.id;
    setState(() {
      _list = list;
      _loadingList = false;
      _selectedId = initial;
    });
    if (initial.isNotEmpty) await _loadBundle(initial, _mode);
  }

  Future<void> _loadBundle(String id, String mode) async {
    if (!mounted) return;
    setState(() => _loadingBundle = true);
    final Map<String, dynamic>? viewQuery = _viewQuery(mode);
    final ArchiveBundle bundle = await widget.api.loadArchiveBundle(
      archiveId: id,
      mode: mode,
      viewQuery: viewQuery,
    );
    // The typed ArchiveView behind the observability cards is deliberately
    // lossy (it drops events[].phase, seat ids, decision details, ...). Fetch
    // the full raw `View` once so the replay adapter can build an honest
    // timeline. This is the same read-only GET the cards already use.
    Map<String, dynamic> viewJson = const <String, dynamic>{};
    try {
      viewJson = await widget.api.fetchViewJson(id, mode, query: viewQuery);
    } catch (_) {
      // A failed or empty view yields no frames; the button stays gated and
      // shows "not recorded". Nothing is fabricated.
      viewJson = const <String, dynamic>{};
    }
    if (!mounted) return;
    setState(() {
      _bundle = bundle;
      _replay = adaptArchiveView(viewJson);
      _loadingBundle = false;
      _firstSeatId = bundle.detail.value?.config.firstSeatId;
    });
  }

  Map<String, dynamic>? _viewQuery(String mode) {
    switch (mode) {
      case ArchiveModes.accelerated:
        // The compressed view only accepts speed 2 or 4.
        return const <String, dynamic>{'speed': 2};
      case ArchiveModes.singleCharacter:
        final String? seat = _firstSeatId;
        return (seat != null && seat.isNotEmpty)
            ? <String, dynamic>{'seat_id': seat}
            : null;
      case ArchiveModes.sameSpecComparison:
        final String other = _otherArchiveId();
        return other.isNotEmpty
            ? <String, dynamic>{'compare_with': other}
            : null;
      default:
        return null;
    }
  }

  String _otherArchiveId() {
    if (widget.compareWith.isNotEmpty && widget.compareWith != _selectedId) {
      return widget.compareWith;
    }
    final List<ArchiveSummary> items = _list?.value ?? const <ArchiveSummary>[];
    for (final ArchiveSummary summary in items) {
      if (summary.id.isNotEmpty && summary.id != _selectedId) return summary.id;
    }
    return '';
  }

  void _onArchiveSelected(String id) {
    if (id == _selectedId && _bundle != null) return;
    setState(() => _selectedId = id);
    _loadBundle(id, _mode);
  }

  void _onModeSelected(String mode) {
    if (mode == _mode && _bundle != null) return;
    setState(() => _mode = mode);
    if (_selectedId.isNotEmpty) _loadBundle(_selectedId, mode);
  }

  PanelsException? _degraded() {
    if (_list != null && _list!.isDegraded) return _list!.error;
    final ArchiveBundle? bundle = _bundle;
    if (bundle != null && bundle.isDegraded) {
      for (final Section<dynamic> section in <Section<dynamic>>[
        bundle.detail,
        bundle.integrity,
        bundle.view,
        bundle.reprises,
      ]) {
        if (section.isDegraded) return section.error;
      }
    }
    return null;
  }

  @override
  Widget build(BuildContext context) {
    if (_loadingList) {
      return const Center(child: CircularProgressIndicator());
    }
    final PanelsException? degraded = _degraded();
    return SingleChildScrollView(
      padding: const EdgeInsets.all(panelsGutter),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: <Widget>[
          Wrap(
            spacing: 8,
            crossAxisAlignment: WrapCrossAlignment.center,
            children: <Widget>[
              Text('Archive: ${_selectedId.isEmpty ? panelsNotRecorded : _selectedId}',
                  style: Theme.of(context).textTheme.titleSmall),
              IconButton(
                icon: const Icon(Icons.refresh),
                tooltip: 'Reload archives',
                onPressed: () => _loadList(),
              ),
            ],
          ),
          if (degraded != null)
            DegradedBanner(code: degraded.code, message: degraded.message),
          _sessionListCard(context),
          _viewSelectorCard(),
          _replayLaunchCard(),
          if (_selectedId.isEmpty)
            const EmptyHint('No archive selected (empty session list).'),
          if (_selectedId.isNotEmpty && _loadingBundle)
            const Padding(
              padding: EdgeInsets.all(panelsGutter),
              child: Center(child: CircularProgressIndicator()),
            ),
          if (_selectedId.isNotEmpty && !_loadingBundle && _bundle != null)
            ..._bundleCards(context, _bundle!),
        ],
      ),
    );
  }

  // --- session list --------------------------------------------------------

  Widget _sessionListCard(BuildContext context) {
    final Section<List<ArchiveSummary>>? list = _list;
    return PanelCard(
      title: 'Archived sessions',
      trailing: (list != null && list.isOk)
          ? Badge('${list.value!.length}', color: Colors.blueGrey)
          : null,
      children: <Widget>[
        if (list == null) const EmptyHint('Loading…'),
        if (list != null && !list.isOk) ...sectionErrorRows(context, list),
        if (list != null && list.isOk && list.value!.isEmpty)
          const EmptyHint('No archives recorded (empty list).'),
        if (list != null && list.isOk && list.value!.isNotEmpty)
          Wrap(
            spacing: 6,
            runSpacing: 6,
            children: <Widget>[
              for (final ArchiveSummary summary in list.value!)
                ChoiceChip(
                  label: Text(shortHash(summary.id, 16)),
                  selected: summary.id == _selectedId,
                  onSelected: (_) => _onArchiveSelected(summary.id),
                ),
            ],
          ),
      ],
    );
  }

  Widget _viewSelectorCard() => PanelCard(
        title: 'Six-view projection',
        children: <Widget>[
          Wrap(
            spacing: 6,
            runSpacing: 6,
            children: <Widget>[
              for (final String mode in ArchiveModes.all)
                ChoiceChip(
                  label: Text(ArchiveModes.label(mode)),
                  selected: mode == _mode,
                  onSelected: (_) => _onModeSelected(mode),
                ),
            ],
          ),
          const Caption(
            'Each mode is a read-only projection over the same immutable '
            'event stream (hub/archive/views.go). Unused fields are omitted '
            'by the Hub, so a missing projection field stays "not recorded".',
          ),
        ],
      );

  // --- replay playback -----------------------------------------------------

  /// The "Play replay" action. The button is gated on a selected archive AND a
  /// non-empty adapted frame list; otherwise a clear "not recorded" state is
  /// shown. Wrap (not an unbounded Row) keeps it overflow-safe at 800x600.
  Widget _replayLaunchCard() {
    final List<TheatreFrame> frames =
        _replay?.frames ?? const <TheatreFrame>[];
    final bool hasSelection = _selectedId.isNotEmpty;
    final bool canPlay = hasSelection && frames.isNotEmpty && !_loadingBundle;
    return PanelCard(
      title: 'Replay playback',
      children: <Widget>[
        Wrap(
          spacing: 8,
          runSpacing: 6,
          crossAxisAlignment: WrapCrossAlignment.center,
          children: <Widget>[
            FilledButton.icon(
              key: const Key('play_replay_button'),
              onPressed: canPlay ? _playReplay : null,
              icon: const Icon(Icons.play_arrow),
              label: const Text('Play replay'),
            ),
            if (frames.isNotEmpty)
              Badge('${frames.length} frame(s)', color: Colors.blueGrey),
            if (_replay?.divergenceRound != null)
              Badge('divergence round ${_replay!.divergenceRound}',
                  color: Colors.orange),
          ],
        ),
        if (!hasSelection)
          const EmptyHint('Not recorded — no archive selected.'),
        if (hasSelection && !_loadingBundle && frames.isEmpty)
          const EmptyHint('Not recorded — this view recorded no replay frames.'),
        const Caption(
          'Plays the selected six-view projection through TheatreScreen. Only '
          'GET /hub/archives/{id}/views/{mode} is read; no consent grant, '
          'reprise creation or publish is triggered. A field the Hub did not '
          'record (speaker, seat state, winner, tally) stays "not recorded".',
        ),
      ],
    );
  }

  /// Pushes the recorded timeline into the existing TheatreScreen replay.
  Future<void> _playReplay() async {
    final ArchiveReplay? replay = _replay;
    if (replay == null || !replay.hasFrames || _selectedId.isEmpty) return;
    // The heading is the archive id: prefer the genuinely recorded
    // View.archive_id, falling back to the selected id (never invented).
    final String title =
        replay.archiveId.isNotEmpty ? replay.archiveId : _selectedId;
    await Navigator.of(context).push<void>(
      MaterialPageRoute<void>(
        builder: (BuildContext context) => TheatreScreen(
          frames: replay.frames,
          divergenceRound: replay.divergenceRound,
          title: title,
          assets: defaultTheatreAssets,
        ),
      ),
    );
  }

  // --- bundle cards --------------------------------------------------------

  List<Widget> _bundleCards(BuildContext context, ArchiveBundle bundle) =>
      <Widget>[
        _detailCard(context, bundle),
        _missingFieldsCard(bundle),
        _consentCard(bundle),
        _replayCard(context, bundle),
        _divergenceCard(bundle),
        _reprisesCard(context, bundle),
      ];

  Widget _detailCard(BuildContext context, ArchiveBundle bundle) {
    final Section<ArchiveDetail> section = bundle.detail;
    return PanelCard(
      title: 'Archive detail',
      trailing: bundle.integrity.isOk
          ? Badge('integrity ${bundle.integrity.value!.verified.describe()}',
              color: bundle.integrity.value!.verified.value == true
                  ? Colors.green
                  : Colors.grey)
          : null,
      children: <Widget>[
        if (!section.isOk) ...sectionErrorRows(context, section),
        if (section.isOk) ..._detailRows(section.value!),
      ],
    );
  }

  List<Widget> _detailRows(ArchiveDetail detail) {
    final ArchiveSummary? summary = detail.summary;
    return <Widget>[
      Kv('Archive id',
          summary == null || summary.id.isEmpty ? panelsNotRecorded : summary.id),
      Kv('Session id',
          summary == null || summary.sessionId.isEmpty ? panelsNotRecorded : summary.sessionId),
      Kv('Outcome',
          summary == null || summary.outcome.isEmpty ? panelsNotRecorded : summary.outcome),
      Kv('Rounds', summary == null ? panelsNotRecorded : summary.rounds.describe()),
      Kv('Events', summary == null ? panelsNotRecorded : summary.eventCount.describe()),
      Kv('Verified', summary == null ? panelsNotRecorded : summary.verified.describe()),
      Kv('Complete', detail.integrity.complete.describe()),
      Kv('Created', tsText(summary == null ? const Field<int>.unknown() : summary.createdAt)),
      Kv('Published', detail.published.describe()),
      Kv('Replay policy',
          summary == null ? panelsNotRecorded : summary.replayPolicy.describe()),
      Kv('Content hash',
          detail.integrity.contentHash.isEmpty ? panelsNotRecorded : shortHash(detail.integrity.contentHash, 20)),
      Kv('Skeleton hash',
          detail.integrity.skeletonHash.isEmpty ? panelsNotRecorded : shortHash(detail.integrity.skeletonHash, 20)),
      Kv('Config: total rounds', detail.config.totalRounds.describe()),
      Kv('Config: seats', detail.config.seatCount.describe()),
      Kv('Replay consent', detail.config.replayConsent.describe()),
    ];
  }

  Widget _missingFieldsCard(ArchiveBundle bundle) {
    final Field<List<String>> field = bundle.detail.isOk
        ? bundle.detail.value!.integrity.missingFields
        : const Field<List<String>>.unknown();
    return PanelCard(
      title: 'Missing / unknown fields (integrity)',
      children: <Widget>[
        Kv('Complete',
            bundle.detail.isOk ? bundle.detail.value!.integrity.complete.describe() : panelsNotRecorded),
        ..._missingRows(field),
      ],
    );
  }

  List<Widget> _missingRows(Field<List<String>> field) {
    if (field.isUnknown) {
      return const <Widget>[
        Kv('missing_fields', panelsNotRecorded),
        Caption('The integrity block did not record missing_fields.'),
      ];
    }
    final List<String> missing = field.value!;
    if (missing.isEmpty) {
      return const <Widget>[
        Kv('missing_fields', 'none'),
        Caption('No missing fields recorded (archive complete).'),
      ];
    }
    return <Widget>[
      Kv('missing_fields', '${missing.length} not recorded'),
      Wrap(
        spacing: 6,
        runSpacing: 6,
        children: <Widget>[
          for (final String name in missing.take(30))
            Chip(
              label: Text('$name: $panelsNotRecorded',
                  style: const TextStyle(fontSize: 11)),
              visualDensity: VisualDensity.compact,
              materialTapTargetSize: MaterialTapTargetSize.shrinkWrap,
            ),
        ],
      ),
      const Caption(
        'Each field the Hub could not capture (persona_id, model_version, '
        'persona_snapshot) is shown as "not recorded" and is never invented '
        '(ADR-0005, missing_fields invariant).',
      ),
    ];
  }

  Widget _consentCard(ArchiveBundle bundle) {
    return PanelCard(
      title: 'Consent ledger (fail-closed)',
      children: <Widget>[
        if (!bundle.detail.isOk)
          const Kv('Consent decision', panelsNotRecorded)
        else
          ..._consentRows(bundle.detail.value!.consent),
      ],
    );
  }

  List<Widget> _consentRows(PolicyDecision consent) => <Widget>[
        Kv('Policy', consent.policy.describe()),
        Kv('Participants', listText(consent.participants)),
        Kv('Granted', listText(consent.granted)),
        Kv('Missing', listText(consent.missing)),
        Kv('Public', consent.isPublic.describe()),
        Kv('Reason', consent.reason.isEmpty ? panelsNotRecorded : consent.reason),
        const Caption(
          'Fail-closed: an empty policy is treated as "all" and an unknown '
          'policy is an error. Individual per-owner Grant rows are returned '
          'only by the write POST .../consent and have NO read endpoint '
          '(see doc), so this panel shows the evaluated decision, not '
          'fabricated grant rows.',
        ),
      ];

  Widget _replayCard(BuildContext context, ArchiveBundle bundle) {
    final Section<ArchiveView> section = bundle.view;
    return PanelCard(
      title: 'Replay · ${ArchiveModes.label(_mode)}',
      trailing: section.isOk
          ? Badge(section.value!.mode.isEmpty ? panelsNotRecorded : section.value!.mode,
              color: Colors.blueGrey)
          : const Badge('unavailable', color: Colors.grey),
      children: <Widget>[
        if (!section.isOk) ...sectionErrorRows(context, section),
        if (section.isOk) ..._replayRows(context, section.value!),
      ],
    );
  }

  List<Widget> _replayRows(BuildContext context, ArchiveView view) {
    final List<Widget> rows = <Widget>[
      Kv('Mode', view.mode.isEmpty ? panelsNotRecorded : view.mode),
      Kv('Event count', view.eventCount.describe()),
      if (view.speed.isRecorded) Kv('Speed', '${view.speed.value}x'),
      if (view.seatId.isRecorded) Kv('Seat', view.seatId.value!),
      if (view.durationMs.isRecorded) Kv('Duration (ms)', view.durationMs.describe()),
      const SizedBox(height: 6),
    ];
    switch (view.mode) {
      case ArchiveModes.omniscientComparison:
        rows.addAll(_roundRows(context, view));
      case ArchiveModes.keyMoments:
        rows.addAll(_momentRows(context, view));
      case ArchiveModes.sameSpecComparison:
        rows.addAll(_comparisonRows(view.comparison));
      default:
        rows.addAll(_eventRows(context, view));
    }
    return rows;
  }

  List<Widget> _eventRows(BuildContext context, ArchiveView view) {
    if (view.events.isUnknown) {
      return const <Widget>[Kv('Timeline events', panelsNotRecorded)];
    }
    final List<ReplayEvent> events = view.events.value!;
    if (events.isEmpty) {
      return const <Widget>[
        Kv('Timeline events', 'none recorded'),
        Caption('Empty replay timeline for this projection.'),
      ];
    }
    return <Widget>[
      Kv('Timeline events', '${events.length}'),
      for (final ReplayEvent event in events.take(20))
        Padding(
          padding: const EdgeInsets.symmetric(vertical: 2),
          child: Wrap(
            spacing: 6,
            runSpacing: 3,
            crossAxisAlignment: WrapCrossAlignment.center,
            children: <Widget>[
              Text('#${event.seq.describe()}',
                  style: Theme.of(context).textTheme.bodySmall),
              Text(event.type.isEmpty ? panelsNotRecorded : event.type,
                  style: Theme.of(context).textTheme.bodySmall),
              Text('round ${event.round.describe()}',
                  style: Theme.of(context).textTheme.bodySmall),
              Text('+${event.offsetMs.describe()}ms',
                  style: Theme.of(context).textTheme.bodySmall),
              Text(event.visibility.isEmpty ? panelsNotRecorded : event.visibility,
                  style: Theme.of(context).textTheme.bodySmall),
            ],
          ),
        ),
    ];
  }

  List<Widget> _roundRows(BuildContext context, ArchiveView view) {
    if (view.rounds.isUnknown) {
      return const <Widget>[Kv('Rounds', panelsNotRecorded)];
    }
    final List<ReplayRound> rounds = view.rounds.value!;
    if (rounds.isEmpty) {
      return const <Widget>[Kv('Rounds', 'none recorded')];
    }
    return <Widget>[
      Kv('Rounds', '${rounds.length}'),
      for (final ReplayRound round in rounds.take(12))
        Padding(
          padding: const EdgeInsets.symmetric(vertical: 2),
          child: Wrap(
            spacing: 6,
            runSpacing: 3,
            crossAxisAlignment: WrapCrossAlignment.center,
            children: <Widget>[
              Text('round ${round.round.describe()}',
                  style: Theme.of(context).textTheme.bodySmall),
              Kv('agreement', round.agreement.describe()),
              Kv('synergy', round.synergy.describe()),
              Kv('decisions', round.decisionCount.describe()),
            ],
          ),
        ),
    ];
  }

  List<Widget> _momentRows(BuildContext context, ArchiveView view) {
    if (view.moments.isUnknown) {
      return const <Widget>[Kv('Key moments', panelsNotRecorded)];
    }
    final List<ReplayMoment> moments = view.moments.value!;
    if (moments.isEmpty) {
      return const <Widget>[Kv('Key moments', 'none recorded')];
    }
    return <Widget>[
      Kv('Key moments', '${moments.length}'),
      for (final ReplayMoment moment in moments.take(12))
        Padding(
          padding: const EdgeInsets.symmetric(vertical: 2),
          child: Wrap(
            spacing: 6,
            runSpacing: 3,
            crossAxisAlignment: WrapCrossAlignment.center,
            children: <Widget>[
              Badge(moment.kind.isEmpty ? panelsNotRecorded : moment.kind,
                  color: Colors.purple),
              Text('round ${moment.round.describe()}',
                  style: Theme.of(context).textTheme.bodySmall),
              Text(moment.reason.isEmpty ? panelsNotRecorded : moment.reason,
                  style: Theme.of(context).textTheme.bodySmall),
            ],
          ),
        ),
    ];
  }

  List<Widget> _comparisonRows(RepriseComparison? comparison) {
    if (comparison == null) {
      return const <Widget>[
        Kv('Comparison', panelsNotRecorded),
        Caption(
          'Not recorded: same_spec_comparison returned no comparison. It '
          'requires a valid compare_with archive id (a second archive).',
        ),
      ];
    }
    return <Widget>[
      Kv('Original id',
          comparison.originalId.isEmpty ? panelsNotRecorded : comparison.originalId),
      Kv('Reprise id',
          comparison.repriseId.isEmpty ? panelsNotRecorded : comparison.repriseId),
      Kv('Identical config', comparison.identicalConfig.describe()),
      Kv('Divergence point', divergenceText(comparison)),
      Kv('Divergence seat', comparison.divergenceSeat.describe()),
      Kv('Outcome changed', comparison.outcomeChanged.describe()),
      Kv('Rounds with differences',
          comparison.rounds.isUnknown ? panelsNotRecorded : '${comparison.rounds.value!.length}'),
    ];
  }

  Widget _divergenceCard(ArchiveBundle bundle) {
    final RepriseComparison? comparison = _divergence(bundle);
    return PanelCard(
      title: 'Same-spec replay · divergence point',
      trailing: Badge(
        comparison == null
            ? panelsNotRecorded
            : (comparison.hasDivergence ? 'diverged' : 'no divergence'),
        color: comparison == null
            ? Colors.grey
            : (comparison.hasDivergence ? Colors.orange : Colors.green),
      ),
      children: <Widget>[
        if (comparison == null)
          const Kv('Divergence point', panelsNotRecorded)
        else ...<Widget>[
          Kv('Divergence point', divergenceText(comparison)),
          Kv('Divergence seat', comparison.divergenceSeat.describe()),
          Kv('Identical config', comparison.identicalConfig.describe()),
          Kv('Outcome changed', comparison.outcomeChanged.describe()),
          Kv('Original outcome',
              comparison.originalOutcome.isEmpty ? panelsNotRecorded : comparison.originalOutcome),
          Kv('Reprise outcome',
              comparison.repriseOutcome.isEmpty ? panelsNotRecorded : comparison.repriseOutcome),
        ],
        const Caption(
          'Divergence comes from the same_spec_comparison view or a completed '
          'reprise. A recorded 0 means "never diverged"; an absent value '
          'stays "not recorded". There is no companion-scoped divergence '
          'endpoint.',
        ),
      ],
    );
  }

  RepriseComparison? _divergence(ArchiveBundle bundle) {
    final RepriseComparison? fromView = bundle.view.value?.comparison;
    if (fromView != null) return fromView;
    final List<Reprise> reprises = bundle.reprises.value ?? const <Reprise>[];
    for (final Reprise reprise in reprises) {
      if (reprise.comparison != null) return reprise.comparison;
    }
    return null;
  }

  Widget _reprisesCard(BuildContext context, ArchiveBundle bundle) {
    final Section<List<Reprise>> section = bundle.reprises;
    return PanelCard(
      title: 'Same-spec reprises',
      trailing: section.isOk
          ? Badge('${section.value!.length}', color: Colors.blueGrey)
          : null,
      children: <Widget>[
        if (!section.isOk) ...sectionErrorRows(context, section),
        if (section.isOk && section.value!.isEmpty)
          const EmptyHint('No reprises recorded (empty list).'),
        if (section.isOk && section.value!.isNotEmpty)
          ...section.value!.take(10).map(
                (Reprise reprise) => Padding(
                  padding: const EdgeInsets.symmetric(vertical: 2),
                  child: Wrap(
                    spacing: 6,
                    runSpacing: 3,
                    crossAxisAlignment: WrapCrossAlignment.center,
                    children: <Widget>[
                      Text(shortHash(reprise.id, 16),
                          style: Theme.of(context).textTheme.bodySmall),
                      Badge(
                        reprise.status.isEmpty ? panelsNotRecorded : reprise.status,
                        color: reprise.isCompleted ? Colors.green : Colors.orange,
                      ),
                      Kv('divergence',
                          reprise.comparison == null ? panelsNotRecorded : divergenceText(reprise.comparison!)),
                    ],
                  ),
                ),
              ),
      ],
    );
  }
}

