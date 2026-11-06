import 'package:flutter/material.dart';

import '../models/hub_models.dart';
import '../services/hub_client.dart';
import '../services/token_store.dart';
import '../session/session_controller.dart';

/// The match console: a text view of a real Hub session.
///
/// This is deliberately not the 2.5D theatre. S3's artboards are blocked on
/// procurement, and a scene built against placeholder art would have to be
/// rebuilt anyway; the loop that matters — invitation, seats, rounds, decisions,
/// settlement — is what this page exercises and what a client must get right
/// before any of it is worth illustrating.
class TheatrePage extends StatefulWidget {
  const TheatrePage({super.key, this.controller});

  /// Injected by tests; production builds one from the fields below.
  final SessionController? controller;

  @override
  State<TheatrePage> createState() => _TheatrePageState();
}

class _TheatrePageState extends State<TheatrePage> {
  // Not `late final`: reconnecting builds a new controller bound to the URL and
  // token currently in the fields, and disposing the old one is part of that.
  late SessionController _controller;
  late final bool _ownsController;

  final TextEditingController _baseUrl = TextEditingController(text: HubClient.defaultBaseUrl);
  final TextEditingController _token = TextEditingController();
  final TextEditingController _ownerId = TextEditingController(text: 'owner_alpha');
  final TextEditingController _companionId = TextEditingController(text: 'comp_alpha');
  final TextEditingController _invitationId = TextEditingController(text: 'inv_demo_1');
  final TextEditingController _partnerOwner = TextEditingController(text: 'owner_beta');
  final TextEditingController _partnerAgent = TextEditingController(text: 'agent_beta');
  final TextEditingController _partnerCompanion = TextEditingController(text: 'comp_beta');
  final TextEditingController _say = TextEditingController();
  final TextEditingController _watchSessionId = TextEditingController();
  final TextEditingController _spectatorToken = TextEditingController();

  @override
  void initState() {
    super.initState();
    _ownsController = widget.controller == null;
    _controller = widget.controller ??
        SessionController(api: HubClient(), tokens: SecureTokenStore());
  }

  @override
  void dispose() {
    if (_ownsController) _controller.dispose();
    for (final TextEditingController c in <TextEditingController>[
      _baseUrl,
      _token,
      _ownerId,
      _companionId,
      _invitationId,
      _partnerOwner,
      _partnerAgent,
      _partnerCompanion,
      _say,
      _watchSessionId,
      _spectatorToken,
    ]) {
      c.dispose();
    }
    super.dispose();
  }

  void _connect() {
    // The URL is applied before connecting: rebinding afterwards would leave
    // the controller talking to one Hub while the field shows another.
    final HubClient client = HubClient(
      baseUrl: _baseUrl.text.trim(),
      ownerToken: _token.text.trim(),
      ownerId: _ownerId.text.trim(),
    );
    if (_ownsController) {
      _controller.dispose();
      _controller = SessionController(api: client, tokens: SecureTokenStore());
    }
    _controller.connect(companionId: _companionId.text.trim());
  }

  void _startMatch() {
    final MatchSetup setup = MatchSetup(
      invitationId: _invitationId.text.trim(),
      me: HubParty(
        agentId: 'agent_alpha',
        companionId: _companionId.text.trim(),
        ownerId: _ownerId.text.trim(),
        displayName: 'Lin Shen',
      ),
      partner: HubParty(
        agentId: _partnerAgent.text.trim(),
        companionId: _partnerCompanion.text.trim(),
        ownerId: _partnerOwner.text.trim(),
        displayName: 'Su Wan',
      ),
    );
    _controller.startMatch(setup);
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(
        title: const Text('Theatre'),
        actions: <Widget>[
          Padding(
            padding: const EdgeInsets.only(right: 12),
            child: Center(child: _StageChip(stage: _controller.stage)),
          ),
        ],
      ),
      // The controller notifies from inside async flows, so the whole page
      // listens rather than individual fields: a partially rebuilt console is
      // how a player ends up acting on a round that already resolved.
      body: AnimatedBuilder(
        animation: _controller,
        builder: (BuildContext context, Widget? _) {
          final SessionView? view = _controller.view;
          return ListView(
            padding: const EdgeInsets.all(12),
            children: <Widget>[
              // Once a match is open it goes first: the setup cards were the
              // way in, and leaving them on top of a live match pushes the thing
              // the player is watching off the screen.
              if (view == null) ...<Widget>[
                _connectionCard(context),
                const SizedBox(height: 12),
                _watchCard(context),
                const SizedBox(height: 12),
                _openMatchCard(context),
              ] else ...<Widget>[
                _matchCard(context, view),
                const SizedBox(height: 12),
                ExpansionTile(
                  title: const Text('Connection & setup'),
                  tilePadding: EdgeInsets.zero,
                  childrenPadding: const EdgeInsets.only(bottom: 8),
                  children: <Widget>[_connectionCard(context), _watchCard(context)],
                ),
              ],
              const SizedBox(height: 12),
              _logCard(context),
            ],
          );
        },
      ),
    );
  }

  Widget _connectionCard(BuildContext context) {
    final bool authed = _token.text.trim().isNotEmpty;
    return Card(
      child: Padding(
        padding: const EdgeInsets.all(12),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: <Widget>[
            Text('Hub', style: Theme.of(context).textTheme.titleMedium),
            const SizedBox(height: 8),
            _field(_baseUrl, 'Hub URL'),
            _field(_token, 'Owner token (blank when the Hub is open)', obscure: true),
            Row(children: <Widget>[
              Expanded(child: _field(_ownerId, 'Owner id')),
              const SizedBox(width: 8),
              Expanded(child: _field(_companionId, 'Companion id')),
            ]),
            const SizedBox(height: 8),
            Row(
              children: <Widget>[
                FilledButton.icon(
                  onPressed: _controller.busy ? null : _connect,
                  icon: const Icon(Icons.link),
                  label: const Text('Connect'),
                ),
                const SizedBox(width: 12),
                Expanded(
                  child: Text(
                    authed
                        ? 'Requests carry Authorization: Bearer. The Hub derives the owner from the token, never from the body.'
                        : 'No token: the Hub must be started with HUB_ALLOW_UNAUTHENTICATED=1, or every call answers 401.',
                    style: Theme.of(context).textTheme.bodySmall,
                  ),
                ),
              ],
            ),
          ],
        ),
      ),
    );
  }

  Widget _watchCard(BuildContext context) {
    return Card(
      child: Padding(
        padding: const EdgeInsets.all(12),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: <Widget>[
            Text('Watch a match', style: Theme.of(context).textTheme.titleMedium),
            const SizedBox(height: 8),
            _field(_watchSessionId, 'Session id'),
            _field(_spectatorToken, 'Spectator token (HUB_SPECTATOR_TOKEN)', obscure: true),
            const SizedBox(height: 4),
            FilledButton.tonalIcon(
              onPressed: _controller.busy ? null : _watch,
              icon: const Icon(Icons.visibility_outlined),
              label: const Text('Watch as spectator'),
            ),
            const SizedBox(height: 8),
            Text(
              'This is the owner\'s view: the omniscient stream, which shows every seat\'s '
              'private decisions. It needs a spectator token, not an owner token — spectating '
              'is not acting, and the Hub keeps writes owner-only.',
              style: Theme.of(context).textTheme.bodySmall,
            ),
          ],
        ),
      ),
    );
  }

  void _watch() {
    final String sessionId = _watchSessionId.text.trim();
    if (sessionId.isEmpty) return;
    _controller.watch(sessionId, spectatorToken: _spectatorToken.text.trim());
  }

  Widget _openMatchCard(BuildContext context) {
    return Card(
      child: Padding(
        padding: const EdgeInsets.all(12),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: <Widget>[
            Text('Open a match', style: Theme.of(context).textTheme.titleMedium),
            const SizedBox(height: 8),
            _field(_invitationId, 'Invitation id (also seeds the skeleton)'),
            Row(children: <Widget>[
              Expanded(child: _field(_partnerOwner, 'Partner owner id')),
              const SizedBox(width: 8),
              Expanded(child: _field(_partnerAgent, 'Partner agent id')),
            ]),
            _field(_partnerCompanion, 'Partner companion id'),
            const SizedBox(height: 8),
            FilledButton.icon(
              onPressed: _controller.busy ? null : _startMatch,
              icon: const Icon(Icons.play_arrow),
              label: const Text('Negotiate and open'),
            ),
            const SizedBox(height: 8),
            Text(
              'The partner side is confirmed locally. A real reply arrives over EigenFlux PM and is '
              'picked up by POST /hub/invitations/{id}/poll, which needs a gateway; without one the '
              'Hub answers 503 on send.',
              style: Theme.of(context).textTheme.bodySmall,
            ),
          ],
        ),
      ),
    );
  }

  Widget _matchCard(BuildContext context, SessionView view) {
    final ThemeData theme = Theme.of(context);
    final HubRound? round = view.currentRound;
    final bool live = view.isLive;
    final int joined = view.seats.where((HubSeat s) => s.joined).length;

    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: <Widget>[
        Card(
          child: Padding(
            padding: const EdgeInsets.all(12),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: <Widget>[
                Text(view.skeleton?.title ?? 'Match ${view.sessionId}',
                    style: theme.textTheme.titleLarge),
                const SizedBox(height: 4),
                Wrap(
                  spacing: 8,
                  runSpacing: 4,
                  children: <Widget>[
                    Chip(label: Text('${view.status} · ${view.phase}')),
                    Chip(label: Text('round ${view.round}/${view.totalRounds}')),
                    if (_controller.watching) const Chip(label: Text('omniscient')),
                    Chip(label: Text('$joined/${view.seats.length} seated')),
                    if (view.skeleton?.dedupDegraded ?? false)
                      const Chip(label: Text('dedup degraded')),
                  ],
                ),
                if (!live)
                  Padding(
                    padding: const EdgeInsets.only(top: 8),
                    child: Text(
                      view.isFinished
                          ? 'Finished: ${view.outcome.isEmpty ? 'no settlement recorded' : view.outcome}'
                          : 'The match goes live when every seat has joined. Each seat needs its own '
                              'client, because a join asserts an owner id and the Hub compares it to the token.',
                      style: theme.textTheme.bodySmall,
                    ),
                  ),
                if (view.metrics.isNotEmpty) ...<Widget>[
                  const SizedBox(height: 12),
                  for (final HubMetric metric in view.metrics.values) _metricRow(theme, metric),
                ],
              ],
            ),
          ),
        ),
        const SizedBox(height: 12),
        Card(
          child: Padding(
            padding: const EdgeInsets.all(12),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: <Widget>[
                Text('Seats', style: theme.textTheme.titleMedium),
                for (final HubSeat seat in view.seats)
                  ListTile(
                    dense: true,
                    leading: Icon(seat.official ? Icons.badge_outlined : Icons.person_outline),
                    title: Text('${seat.seatId} · ${seat.displayName}'),
                    subtitle: Text('${seat.ownerId} · ${seat.companionId}'),
                    trailing: seat.joined
                        ? const Icon(Icons.check_circle_outline)
                        : const Icon(Icons.hourglass_empty),
                  ),
                if (!live && joined < view.seats.length)
                  OutlinedButton.icon(
                    onPressed: _controller.busy ? null : () => _sitRemaining(view),
                    icon: const Icon(Icons.event_seat),
                    label: Text('Sit the remaining ${view.seats.length - joined} seat(s)'),
                  ),
              ],
            ),
          ),
        ),
        if (round != null) ...<Widget>[
          const SizedBox(height: 12),
          Card(
            child: Padding(
              padding: const EdgeInsets.all(12),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: <Widget>[
                  Text('Round ${round.index}: ${round.title}', style: theme.textTheme.titleMedium),
                  const SizedBox(height: 4),
                  Text(round.objective),
                  if (round.reveal.isNotEmpty) ...<Widget>[
                    const SizedBox(height: 8),
                    for (final HubClue clue in round.reveal)
                      Text('• ${clue.label}: ${clue.text}', style: theme.textTheme.bodySmall),
                  ],
                  const SizedBox(height: 12),
                  Wrap(
                    spacing: 8,
                    runSpacing: 8,
                    children: <Widget>[
                      for (final HubAction action in round.actions)
                        ActionChip(
                          label: Text('${action.label} (${action.risk})'),
                          tooltip: 'family ${action.family} · info ${action.infoGain} · harm ${action.teamHarm}',
                          onPressed: _controller.busy || !_controller.isSeated || view.phase != 'act'
                              ? null
                              : () => _controller.act(action.id),
                        ),
                    ],
                  ),
                  const SizedBox(height: 8),
                  Text(
                    view.phase == 'act'
                        ? 'Decisions are accepted in the act phase. The round resolves when every seated seat has decided.'
                        : 'Advance to ${_nextPhase(view.phase)}; actions unlock in act.',
                    style: theme.textTheme.bodySmall,
                  ),
                  const SizedBox(height: 8),
                  // Wrap, not Row: three buttons plus their labels do not fit a
                  // narrow window, and a RenderFlex overflow is a visible defect
                  // (the debug stripe) rather than a graceful wrap.
                  Wrap(
                    spacing: 8,
                    runSpacing: 8,
                    children: <Widget>[
                      FilledButton.tonal(
                        onPressed: _controller.busy || !live || _nextPhase(view.phase).isEmpty
                            ? null
                            : () => _controller.advance(_nextPhase(view.phase)),
                        child: Text('Advance to ${_nextPhase(view.phase).isEmpty ? '—' : _nextPhase(view.phase)}'),
                      ),
                      OutlinedButton(
                        onPressed: _controller.busy ? null : _controller.refresh,
                        child: const Text('Refresh'),
                      ),
                      OutlinedButton.icon(
                        // Resumes from the last event this client processed, so
                        // the gap is replayed rather than skipped.
                        onPressed: _controller.busy || _controller.view == null
                            ? null
                            : _controller.reconnect,
                        icon: const Icon(Icons.sync),
                        label: Text('Reconnect from ${_controller.lastEventId}'),
                      ),
                    ],
                  ),
                ],
              ),
            ),
          ),
        ],
        const SizedBox(height: 12),
        Card(
          child: Padding(
            padding: const EdgeInsets.all(12),
            child: Row(
              children: <Widget>[
                Expanded(
                  child: TextField(
                    controller: _say,
                    decoration: const InputDecoration(
                      labelText: 'Say something in the discuss phase',
                      isDense: true,
                    ),
                    onSubmitted: (String value) {
                      _controller.say(value);
                      _say.clear();
                    },
                  ),
                ),
                const SizedBox(width: 8),
                IconButton.filled(
                  tooltip: 'Send',
                  onPressed: _controller.isSeated
                      ? () {
                          _controller.say(_say.text);
                          _say.clear();
                        }
                      : null,
                  icon: const Icon(Icons.send),
                ),
              ],
            ),
          ),
        ),
      ],
    );
  }

  /// Seats the rest of the match as the claimed owners.
  ///
  /// Only an unauthenticated Hub accepts this: with tokens on, each join is
  /// checked against its owner's token, so one client cannot sit another
  /// owner's seat — which is the point of ADR-0009. The button exists so a
  /// single machine can still watch a whole match, and it says so.
  void _sitRemaining(SessionView view) {
    final String sessionId = view.sessionId;
    for (final HubSeat seat in view.seats) {
      if (seat.joined) continue;
      _controller.joinSeat(sessionId, seat.seatId, seat.ownerId);
    }
  }

  String _nextPhase(String phase) {
    switch (phase) {
      case 'observe':
        return 'discuss';
      case 'discuss':
        return 'act';
      default:
        return '';
    }
  }

  Widget _metricRow(ThemeData theme, HubMetric metric) {
    final bool goodHigh = metric.good == 'high';
    final bool healthy = goodHigh ? metric.fraction >= 0.5 : metric.fraction <= 0.5;
    final Color color = healthy ? theme.colorScheme.primary : theme.colorScheme.error;
    return Padding(
      padding: const EdgeInsets.only(bottom: 6),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: <Widget>[
          Row(
            children: <Widget>[
              Expanded(child: Text(metric.label, style: theme.textTheme.bodySmall)),
              Text('${metric.value.toStringAsFixed(0)}/${metric.max.toStringAsFixed(0)}',
                  style: theme.textTheme.bodySmall),
            ],
          ),
          const SizedBox(height: 2),
          ClipRRect(
            borderRadius: BorderRadius.circular(4),
            child: LinearProgressIndicator(
              value: metric.fraction,
              minHeight: 6,
              color: color,
              backgroundColor: theme.colorScheme.surfaceContainerHighest,
            ),
          ),
        ],
      ),
    );
  }

  Widget _logCard(BuildContext context) {
    final ThemeData theme = Theme.of(context);
    final List<LogLine> lines = _controller.log;
    return Card(
      child: Padding(
        padding: const EdgeInsets.all(12),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: <Widget>[
            Row(
              children: <Widget>[
                Text('Match log', style: theme.textTheme.titleMedium),
                const Spacer(),
                Text('${lines.length}', style: theme.textTheme.bodySmall),
              ],
            ),
            if (_controller.error != null)
              Padding(
                padding: const EdgeInsets.only(top: 8),
                child: Text(
                  '${_controller.error!.code}: ${_controller.error!.message}',
                  style: theme.textTheme.bodySmall?.copyWith(color: theme.colorScheme.error),
                ),
              ),
            const SizedBox(height: 8),
            if (lines.isEmpty)
              Text('Nothing yet. Connect, then open a match.', style: theme.textTheme.bodySmall),
            for (final LogLine line in lines.reversed.take(60))
              Padding(
                padding: const EdgeInsets.symmetric(vertical: 2),
                child: Text(
                  '${line.at.toIso8601String().substring(11, 19)}  ${line.text}',
                  style: theme.textTheme.bodySmall?.copyWith(
                    color: line.kind == LogKind.failure ? theme.colorScheme.error : null,
                  ),
                ),
              ),
          ],
        ),
      ),
    );
  }

  Widget _field(TextEditingController controller, String label, {bool obscure = false}) {
    return Padding(
      padding: const EdgeInsets.only(bottom: 8),
      child: TextField(
        controller: controller,
        obscureText: obscure,
        decoration: InputDecoration(labelText: label, isDense: true, border: const OutlineInputBorder()),
      ),
    );
  }
}

class _StageChip extends StatelessWidget {
  const _StageChip({required this.stage});

  final SessionStage stage;

  @override
  Widget build(BuildContext context) {
    final ThemeData theme = Theme.of(context);
    final bool bad = stage == SessionStage.failed;
    return Chip(
      visualDensity: VisualDensity.compact,
      backgroundColor: bad ? theme.colorScheme.errorContainer : null,
      label: Text(stage.name),
    );
  }
}
