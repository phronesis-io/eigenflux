import 'package:flutter/material.dart';

import '../services/token_store.dart';
import 'hub_probe.dart';
import 'setup_state.dart';

/// A guided, step-by-step first-run setup for the Hub connection.
///
/// The human never has to know the `/health` route, the bearer-token env var or
/// the companion binding endpoint: the wizard probes the Hub, reads the auth
/// mode from the real `/health` JSON and shows one screen per decision.
///
/// Public API:
/// - [SetupWizard.open] pushes the wizard and resolves with a [SetupResult]
///   (null when dismissed before finishing).
/// - [state] is injectable for tests; when omitted a production [SetupState]
///   (`DioHubProbe` + `SecureTokenStore`) is built. No keychain read happens
///   during construction, so a smoke render never touches a platform channel.
/// - [onCompleted] is an optional alternative hand-back fired with the same
///   [SetupResult] when the user taps Done.
class SetupWizard extends StatefulWidget {
  const SetupWizard({super.key, this.state, this.onCompleted});

  final SetupState? state;
  final void Function(SetupResult result)? onCompleted;

  static Future<SetupResult?> open(BuildContext context, {SetupState? state}) {
    return Navigator.of(context).push<SetupResult>(
      MaterialPageRoute<SetupResult>(
        builder: (_) => SetupWizard(state: state),
      ),
    );
  }

  @override
  State<SetupWizard> createState() => _SetupWizardState();
}

class _SetupWizardState extends State<SetupWizard> {
  late final SetupState _state;
  late final bool _ownsState;
  late final TextEditingController _hubUrl;
  late final TextEditingController _ownerToken;
  late final TextEditingController _ownerId;
  late final TextEditingController _companionId;

  @override
  void initState() {
    super.initState();
    _ownsState = widget.state == null;
    _state = widget.state ??
        SetupState(probe: DioHubProbe(), store: SecureTokenStore());
    _hubUrl = TextEditingController(text: _state.hubUrl);
    _ownerToken = TextEditingController(text: _state.ownerToken);
    _ownerId = TextEditingController(text: _state.ownerId);
    _companionId = TextEditingController(text: _state.companionId);
    // Controller listeners (not onChanged) keep the state machine's copies in
    // sync regardless of how the text was entered, and stay stable across the
    // ListenableBuilder rebuilds.
    _hubUrl.addListener(_syncHubUrl);
    _ownerToken.addListener(_syncOwnerToken);
    _ownerId.addListener(_syncOwnerId);
    _companionId.addListener(_syncCompanionId);
  }

  void _syncHubUrl() => _state.setHubUrl(_hubUrl.text);
  void _syncOwnerToken() => _state.setOwnerToken(_ownerToken.text);
  void _syncOwnerId() => _state.setOwnerId(_ownerId.text);
  void _syncCompanionId() => _state.setCompanionId(_companionId.text);

  @override
  void dispose() {
    _hubUrl.dispose();
    _ownerToken.dispose();
    _ownerId.dispose();
    _companionId.dispose();
    if (_ownsState) _state.dispose();
    super.dispose();
  }

  Future<void> _onNext() async {
    await _state.next();
  }

  void _finish() {
    final SetupResult? result = _state.result;
    if (result == null) return;
    widget.onCompleted?.call(result);
    if (mounted) Navigator.of(context).pop<SetupResult>(result);
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(title: const Text('Set up Hub')),
      body: ListenableBuilder(
        listenable: _state,
        builder: (BuildContext context, Widget? _) {
          return Column(
            children: <Widget>[
              _stepIndicator(context),
              const Divider(height: 1),
              Expanded(
                child: SingleChildScrollView(
                  padding: const EdgeInsets.all(16),
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.stretch,
                    children: <Widget>[
                      _stepIntro(context),
                      const SizedBox(height: 16),
                      _stepBody(context),
                      _errorBanner(context),
                    ],
                  ),
                ),
              ),
              _navBar(context),
            ],
          );
        },
      ),
    );
  }

  Widget _stepIndicator(BuildContext context) {
    final ThemeData theme = Theme.of(context);
    return Padding(
      padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 8),
      child: Wrap(
        spacing: 6,
        runSpacing: 6,
        children: <Widget>[
          for (int i = 0; i < _state.steps.length; i++)
            _stepChip(context, _state.steps[i], i, theme),
        ],
      ),
    );
  }

  Widget _stepChip(BuildContext context, SetupStep s, int index, ThemeData theme) {
    final bool active = s == _state.step;
    final bool passed = index < _state.stepIndex;
    return Chip(
      visualDensity: VisualDensity.compact,
      backgroundColor: active
          ? theme.colorScheme.primaryContainer
          : (passed ? theme.colorScheme.secondaryContainer : null),
      label: Text(
        '${index + 1}. ${_label(s)}',
        style: TextStyle(
          color: active ? theme.colorScheme.onPrimaryContainer : null,
          fontSize: 12,
        ),
      ),
    );
  }

  Widget _stepIntro(BuildContext context) {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: <Widget>[
        Text(_title(_state.step), style: Theme.of(context).textTheme.titleLarge),
        const SizedBox(height: 4),
        Text(_subtitle(_state.step), style: Theme.of(context).textTheme.bodySmall),
      ],
    );
  }

  Widget _stepBody(BuildContext context) {
    switch (_state.step) {
      case SetupStep.hubUrl:
        return _hubUrlStep(context);
      case SetupStep.connection:
        return _connectionStep(context);
      case SetupStep.ownerToken:
        return _tokenStep(context);
      case SetupStep.identity:
        return _identityStep(context);
      case SetupStep.binding:
        return _bindingStep(context);
      case SetupStep.save:
        return _saveStep(context);
      case SetupStep.done:
        return _doneStep(context);
    }
  }

  Widget _hubUrlStep(BuildContext context) {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: <Widget>[
        TextField(
          controller: _hubUrl,
          keyboardType: TextInputType.url,
          decoration: const InputDecoration(
            labelText: 'Hub URL',
            hintText: 'http://127.0.0.1:8095',
            border: OutlineInputBorder(),
            isDense: true,
          ),
        ),
        const SizedBox(height: 12),
        const Text(
          'This is the base URL only. The wizard adds /health and /hub/... '
          'itself; you do not need to know the endpoints.',
        ),
      ],
    );
  }

  Widget _connectionStep(BuildContext context) {
    final HubHealth? h = _state.health;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: <Widget>[
        FilledButton.tonalIcon(
          onPressed: _state.busy ? null : _state.checkConnection,
          icon: _state.checking
              ? const SizedBox(
                  width: 16,
                  height: 16,
                  child: CircularProgressIndicator(strokeWidth: 2),
                )
              : const Icon(Icons.wifi_find),
          label: Text(_state.checking ? 'Checking…' : 'Check connection'),
        ),
        const SizedBox(height: 12),
        Text('Target: ${_state.hubUrl.trim()}  ->  GET /health',
            style: Theme.of(context).textTheme.bodySmall),
        const SizedBox(height: 12),
        if (h == null)
          const Text(
            'No check has run yet. The wizard makes no network call until you '
            'press the button above.',
          )
        else if (h.checkSucceeded)
          _authResult(context, h)
        else
          Text(
            h.failureMessage ?? 'The connection check failed.',
            style: TextStyle(color: Theme.of(context).colorScheme.error),
          ),
      ],
    );
  }

  Widget _authResult(BuildContext context, HubHealth h) {
    final ThemeData theme = Theme.of(context);
    final HubAuthMode auth = h.auth;
    final bool enabled = auth == HubAuthMode.enabled;
    return Card(
      color: theme.colorScheme.primaryContainer,
      child: Padding(
        padding: const EdgeInsets.all(12),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: <Widget>[
            Wrap(
              spacing: 8,
              crossAxisAlignment: WrapCrossAlignment.center,
              children: <Widget>[
                const Icon(Icons.check_circle_outline, size: 18),
                Text(
                  'Hub reachable'
                  '${h.service.isEmpty ? '' : ' (${h.service})'}'
                  ' · auth: ${enabled ? 'enabled' : 'disabled'}',
                  style: theme.textTheme.titleSmall,
                ),
              ],
            ),
            const SizedBox(height: 8),
            Text(
              enabled
                  ? 'Authentication is ENABLED. The Hub was started with '
                      'HUB_OWNER_TOKENS (a JSON object mapping owner id to bearer '
                      'token). Every non-exempt request needs '
                      '"Authorization: Bearer <token>". You will paste one next; '
                      'the Hub derives your owner id from the token (ADR-0009 §2.1).'
                  : 'Authentication is DISABLED. The Hub was started with '
                      'HUB_ALLOW_UNAUTHENTICATED=1 and answers requests '
                      'anonymously. No bearer token is needed — leave it blank. '
                      'Such a Hub cannot attribute a companion to an owner.',
              style: theme.textTheme.bodySmall,
            ),
          ],
        ),
      ),
    );
  }

  Widget _tokenStep(BuildContext context) {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: <Widget>[
        TextField(
          controller: _ownerToken,
          obscureText: true,
          decoration: const InputDecoration(
            labelText: 'Owner bearer token',
            hintText: 'a token from HUB_OWNER_TOKENS',
            border: OutlineInputBorder(),
            isDense: true,
          ),
        ),
        const SizedBox(height: 12),
        const Text(
          'The Hub reported auth: enabled. Paste one of the bearer tokens from '
          'the Hub\'s HUB_OWNER_TOKENS JSON object (owner id -> token). Requests '
          'carry it as "Authorization: Bearer <token>". The Hub derives the '
          'owner id from the token and rejects a mismatched owner_id '
          '(OWNER_MISMATCH), so the owner id you enter next must match it.',
        ),
      ],
    );
  }

  Widget _identityStep(BuildContext context) {
    final bool enabled = _state.health?.auth == HubAuthMode.enabled;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: <Widget>[
        TextField(
          controller: _ownerId,
          decoration: const InputDecoration(
            labelText: 'Owner id',
            border: OutlineInputBorder(),
            isDense: true,
          ),
        ),
        const SizedBox(height: 12),
        TextField(
          controller: _companionId,
          decoration: const InputDecoration(
            labelText: 'Companion id',
            border: OutlineInputBorder(),
            isDense: true,
          ),
        ),
        const SizedBox(height: 12),
        Text(
          enabled
              ? 'On an authenticated Hub the owner id must equal the one your '
                  'token maps to; the Hub compares them and rejects a mismatch. '
                  'The companion id is the character you act for.'
              : 'On an open Hub these are client-asserted labels (there is no '
                  'token to derive an owner from). The companion id is the '
                  'character you act for.',
        ),
      ],
    );
  }

  Widget _bindingStep(BuildContext context) {
    final bool enabled = _state.health?.auth == HubAuthMode.enabled;
    final BindingCheck? b = _state.binding;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: <Widget>[
        Text(
          enabled
              ? 'Verify that this companion is bound to your owner by POSTing '
                  '/hub/companions with your bearer token. The bind is '
                  'idempotent, so running it again is safe (ADR-0009 §2.2).'
              : 'This Hub is unauthenticated (HUB_ALLOW_UNAUTHENTICATED=1). It '
                  'has no owner identity to bind to, so POST /hub/companions '
                  'will be refused with AUTH_REQUIRED. Skipping is the correct '
                  'choice here.',
        ),
        const SizedBox(height: 12),
        Wrap(
          spacing: 8,
          runSpacing: 8,
          children: <Widget>[
            FilledButton.tonalIcon(
              onPressed: _state.busy ? null : _state.checkBinding,
              icon: _state.bindingBusy
                  ? const SizedBox(
                      width: 16,
                      height: 16,
                      child: CircularProgressIndicator(strokeWidth: 2),
                    )
                  : const Icon(Icons.link),
              label: const Text('Check / bind companion'),
            ),
          ],
        ),
        const SizedBox(height: 12),
        if (!_state.bindingChecked)
          const Text('Not checked yet. This step is optional; press Next to skip it.')
        else
          Text(
            (b?.ok ?? false)
                ? 'Companion binding verified.'
                : 'Companion binding was NOT verified (${b?.code ?? 'failed'}). '
                    'It is safe to continue — this step is optional.',
            style: TextStyle(
              color: (b?.ok ?? false) ? null : Theme.of(context).colorScheme.error,
            ),
          ),
      ],
    );
  }

  Widget _saveStep(BuildContext context) {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: <Widget>[
        _summaryRow('Hub URL', _state.hubUrl.trim()),
        _summaryRow('Auth mode', _state.health?.authLabel ?? 'unknown'),
        _summaryRow(
          'Owner token',
          _state.ownerToken.trim().isEmpty ? '(none — open Hub)' : '•••••• (set)',
        ),
        _summaryRow('Owner id', _state.ownerId.trim()),
        _summaryRow('Companion id', _state.companionId.trim()),
        _summaryRow(
          'Companion binding',
          _state.bindingVerified ? 'verified' : 'not verified (optional)',
        ),
        const SizedBox(height: 12),
        const Text('Press "Save & finish" to write these values to local storage.'),
      ],
    );
  }

  Widget _doneStep(BuildContext context) {
    final SetupResult? r = _state.result;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: <Widget>[
        Icon(Icons.check_circle_outline,
            size: 48, color: Theme.of(context).colorScheme.primary),
        const SizedBox(height: 12),
        const Text(
          'The Hub is configured. Tap Done to return; the saved values are '
          'handed back to the app.',
        ),
        const SizedBox(height: 12),
        if (r != null) ...<Widget>[
          _summaryRow('Hub URL', r.hubUrl),
          _summaryRow('Auth mode', r.authLabel),
          _summaryRow('Owner id', r.ownerId),
          _summaryRow('Companion id', r.companionId),
          _summaryRow(
            'Companion binding',
            r.bindingVerified ? 'verified' : 'not verified (skipped or failed)',
          ),
        ],
      ],
    );
  }

  Widget _summaryRow(String label, String value) {
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 4),
      child: Wrap(
        spacing: 8,
        crossAxisAlignment: WrapCrossAlignment.center,
        children: <Widget>[
          SizedBox(
            width: 130,
            child: Text(label, style: const TextStyle(fontWeight: FontWeight.bold)),
          ),
          Text(value),
        ],
      ),
    );
  }

  Widget _navBar(BuildContext context) {
    final bool done = _state.isLast;
    return Padding(
      padding: const EdgeInsets.fromLTRB(16, 8, 16, 16),
      child: Wrap(
        spacing: 8,
        runSpacing: 8,
        alignment: WrapAlignment.end,
        crossAxisAlignment: WrapCrossAlignment.center,
        children: <Widget>[
          if (!_state.isFirst && !done)
            OutlinedButton.icon(
              onPressed: _state.busy ? null : () => _state.back(),
              icon: const Icon(Icons.arrow_back, size: 18),
              label: const Text('Back'),
            ),
          if (done)
            FilledButton.icon(
              onPressed: _finish,
              icon: const Icon(Icons.check, size: 18),
              label: const Text('Done'),
            )
          else
            FilledButton.icon(
              onPressed: _state.busy ? null : _onNext,
              icon: const Icon(Icons.arrow_forward, size: 18),
              label: Text(_state.step == SetupStep.save ? 'Save & finish' : 'Next'),
            ),
        ],
      ),
    );
  }

  Widget _errorBanner(BuildContext context) {
    final String? error = _state.error;
    if (error == null) return const SizedBox.shrink();
    final ThemeData theme = Theme.of(context);
    return Container(
      width: double.infinity,
      margin: const EdgeInsets.only(top: 16),
      padding: const EdgeInsets.all(12),
      decoration: BoxDecoration(
        color: theme.colorScheme.errorContainer,
        borderRadius: BorderRadius.circular(8),
      ),
      child: Text(error, style: TextStyle(color: theme.colorScheme.onErrorContainer)),
    );
  }

  String _label(SetupStep s) => switch (s) {
        SetupStep.hubUrl => 'Hub URL',
        SetupStep.connection => 'Check',
        SetupStep.ownerToken => 'Token',
        SetupStep.identity => 'Identity',
        SetupStep.binding => 'Binding',
        SetupStep.save => 'Save',
        SetupStep.done => 'Done',
      };

  String _title(SetupStep s) => switch (s) {
        SetupStep.hubUrl => 'Where is the Hub?',
        SetupStep.connection => 'Check the connection',
        SetupStep.ownerToken => 'Owner bearer token',
        SetupStep.identity => 'Who are you here?',
        SetupStep.binding => 'Companion binding (optional)',
        SetupStep.save => 'Save configuration',
        SetupStep.done => 'Setup complete',
      };

  String _subtitle(SetupStep s) => switch (s) {
        SetupStep.hubUrl => 'Step 1 — the base URL of the FluxSpace Hub.',
        SetupStep.connection => 'Step 2 — read the real /health response and its auth mode.',
        SetupStep.ownerToken => 'Auth is enabled — paste one bearer token.',
        SetupStep.identity => 'The owner id and the companion you act for.',
        SetupStep.binding => 'Verify the companion is bound to your owner, or skip.',
        SetupStep.save => 'Review and persist the four values.',
        SetupStep.done => 'Hand the saved values back to the app.',
      };
}
