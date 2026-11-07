/// The S5 governance panel.
///
/// Visible: monthly budget (limit / spent / remaining + downgrade indicator),
/// permission tier + denied operations, the seq+prev_hash audit log with a
/// chain-verified badge, and archive-scoped model-version consistency.
///
/// A 503 `GOVERNANCE_DEGRADED` (or `GOVERNANCE_UNCONFIGURED`) is rendered as an
/// explicit [DegradedBanner], never as an empty list. Every scalar the Hub did
/// not record is shown as "not recorded".
library;

import 'package:flutter/material.dart' hide Badge;

import 'panels_api.dart';
import 'panels_models.dart';
import 'panels_ui.dart';

class GovernancePanel extends StatefulWidget {
  /// [api] performs the read-only governance calls. [companionId] selects the
  /// companion whose budget/permissions/audit are shown. [archiveId] is
  /// optional: model-version consistency is archive-scoped and is left as
  /// "not recorded" when no archive is supplied.
  const GovernancePanel({
    super.key,
    required this.api,
    this.companionId = '',
    this.archiveId = '',
  });

  final PanelsApi api;
  final String companionId;
  final String archiveId;

  @override
  State<GovernancePanel> createState() => _GovernancePanelState();
}

class _GovernancePanelState extends State<GovernancePanel> {
  GovernanceData? _data;
  bool _loading = true;

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    if (!mounted) return;
    setState(() => _loading = true);
    final GovernanceData data = await widget.api.loadGovernance(
      companionId: widget.companionId,
      archiveId: widget.archiveId.isEmpty ? null : widget.archiveId,
    );
    if (!mounted) return;
    setState(() {
      _data = data;
      _loading = false;
    });
  }

  @override
  Widget build(BuildContext context) {
    if (_loading || _data == null) {
      return const Center(child: CircularProgressIndicator());
    }
    final GovernanceData data = _data!;
    final PanelsException? degraded = _degradedError(data);
    return SingleChildScrollView(
      padding: const EdgeInsets.all(panelsGutter),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: <Widget>[
          Wrap(
            spacing: 8,
            crossAxisAlignment: WrapCrossAlignment.center,
            children: <Widget>[
              Text(
                'Companion: ${widget.companionId.isEmpty ? panelsNotRecorded : widget.companionId}',
                style: Theme.of(context).textTheme.titleSmall,
              ),
              IconButton(
                icon: const Icon(Icons.refresh),
                tooltip: 'Reload governance state',
                onPressed: () => _load(),
              ),
            ],
          ),
          if (degraded != null)
            DegradedBanner(code: degraded.code, message: degraded.message),
          _budgetCard(context, data),
          _permissionsCard(context, data),
          _auditCard(context, data),
          _modelCard(context, data),
        ],
      ),
    );
  }

  PanelsException? _degradedError(GovernanceData data) {
    final List<Section<dynamic>> sections = <Section<dynamic>>[
      data.budget,
      data.permissions,
      data.audit,
      data.verification,
      if (data.modelConsistency != null) data.modelConsistency!,
    ];
    for (final Section<dynamic> section in sections) {
      if (section.isDegraded) return section.error;
    }
    return null;
  }

  // --- budget --------------------------------------------------------------

  Widget _budgetCard(BuildContext context, GovernanceData data) {
    final Section<BudgetState> section = data.budget;
    return PanelCard(
      title: 'Monthly token budget',
      trailing: section.isOk
          ? Badge(
              section.value!.configured
                  .map((bool v) => v ? 'configured' : 'default cap'),
              color: Colors.blueGrey,
            )
          : const Badge('unavailable', color: Colors.grey),
      children: <Widget>[
        if (!section.isOk) ...sectionErrorRows(context, section),
        if (section.isOk) ..._budgetRows(section.value!),
      ],
    );
  }

  List<Widget> _budgetRows(BudgetState budget) => <Widget>[
        Kv('Companion',
            budget.companionId.isEmpty ? panelsNotRecorded : budget.companionId),
        Kv('Month (UTC)', budget.month.isEmpty ? panelsNotRecorded : budget.month),
        Kv('Monthly limit', fieldText(budget.monthlyTokens)),
        Kv('Spent', fieldText(budget.spentTokens)),
        Kv('Remaining', fieldText(budget.remainingTokens)),
        Kv('Configured',
            budget.configured.map((bool v) => v ? 'yes' : 'no (default cap, ADR-0004)')),
        const Divider(),
        Kv('Downgrade', fieldText(budget.downgrade)),
        const Caption(
          'GET /hub/companions/{id}/budget has NO downgrade field. The '
          'downgrade:"rule_engine" value is emitted only by the write '
          'POST .../budget/usage (a BudgetDecision that also books token spend). '
          'This read-only panel never calls a write, so the value stays '
          '"not recorded" rather than being fabricated.',
        ),
      ];

  // --- permissions ---------------------------------------------------------

  Widget _permissionsCard(BuildContext context, GovernanceData data) {
    final Section<PermissionState> section = data.permissions;
    return PanelCard(
      title: 'Permission tier (default-deny)',
      children: <Widget>[
        if (!section.isOk) ...sectionErrorRows(context, section),
        if (section.isOk) ..._permissionRows(section.value!, data),
      ],
    );
  }

  List<Widget> _permissionRows(PermissionState permission, GovernanceData data) =>
      <Widget>[
        Kv('Companion',
            permission.companionId.isEmpty ? panelsNotRecorded : permission.companionId),
        Kv('Tier', permission.tier.describe()),
        Kv('Revoked',
            permission.revoked.map((bool v) => v ? 'yes — all operations denied' : 'no')),
        Kv('Updated at', tsText(permission.updatedAt)),
        const SizedBox(height: 6),
        Wrap(
          spacing: 6,
          runSpacing: 6,
          children: <Widget>[
            for (final String tier in PermissionTiers.all)
              Badge(
                tier,
                color: permission.tier.value == tier ? Colors.green : Colors.grey,
                icon: permission.tier.value == tier ? Icons.check : null,
              ),
          ],
        ),
        const Caption(
          'Three tiers: read_only < restricted_write < high_impact. An '
          'unconfigured companion has no tier and is denied (default-deny).',
        ),
        const Caption(
          'High-impact operations require a fresh one-shot approval. Approvals '
          'have NO read endpoint, so this panel cannot display them (see doc).',
        ),
        const SizedBox(height: 6),
        Kv(
          'Denied operations (from audit)',
          data.audit.isOk
              ? '${data.audit.value!.denied.length} recorded'
              : panelsNotRecorded,
        ),
      ];

  // --- audit ---------------------------------------------------------------

  Widget _auditCard(BuildContext context, GovernanceData data) {
    final Section<AuditState> section = data.audit;
    return PanelCard(
      title: 'Audit log (append-only, hash-chained)',
      trailing: _chainBadge(data.verification),
      children: <Widget>[
        if (!data.verification.isOk) ...sectionErrorRows(context, data.verification),
        if (data.verification.isOk)
          Kv('Chain entries', data.verification.value!.entries.describe()),
        const SizedBox(height: 6),
        if (!section.isOk) ...sectionErrorRows(context, section),
        if (section.isOk) ..._auditRows(context, section.value!),
      ],
    );
  }

  Widget _chainBadge(Section<AuditVerification> section) {
    if (!section.isOk) {
      return Badge(
        section.isDegraded
            ? 'verify degraded'
            : 'verify unavailable (${section.error!.code})',
        color: section.isDegraded ? Colors.orange : Colors.grey,
      );
    }
    final AuditVerification verification = section.value!;
    if (verification.broken) {
      return const Badge('chain broken', color: Colors.red, icon: Icons.error);
    }
    if (verification.verified.value == true) {
      return const Badge('chain verified', color: Colors.green, icon: Icons.verified);
    }
    if (verification.verified.isUnknown) {
      return const Badge('chain status not recorded', color: Colors.grey);
    }
    return const Badge('chain not verified', color: Colors.red);
  }

  List<Widget> _auditRows(BuildContext context, AuditState audit) {
    final List<AuditEntry> entries = audit.list;
    final List<AuditEntry> denied = audit.denied;
    return <Widget>[
      Kv('Entries reported', audit.count.describe()),
      Kv('Denied operations', '${denied.length} recorded'),
      if (denied.isNotEmpty)
        Wrap(
          spacing: 6,
          runSpacing: 6,
          children: <Widget>[
            for (final AuditEntry entry in denied.take(12))
              Badge(
                entry.operation.isEmpty ? panelsNotRecorded : entry.operation,
                color: Colors.red,
              ),
          ],
        ),
      const SizedBox(height: 6),
      if (entries.isEmpty)
        const EmptyHint('No audit entries recorded (empty list).')
      else
        ...entries.take(20).map((AuditEntry e) => _entryTile(context, e)),
    ];
  }

  Widget _entryTile(BuildContext context, AuditEntry entry) {
    final Color outcomeColor = entry.isDenied
        ? Colors.red
        : (entry.isAllowed ? Colors.green : Colors.grey);
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 4),
      child: Wrap(
        spacing: 6,
        runSpacing: 4,
        crossAxisAlignment: WrapCrossAlignment.center,
        children: <Widget>[
          Badge('seq ${entry.seq.describe()}', color: Colors.blueGrey),
          Badge(entry.outcome.describe(), color: outcomeColor),
          Text(
            entry.operation.isEmpty ? panelsNotRecorded : entry.operation,
            style: Theme.of(context).textTheme.bodyMedium,
          ),
          Text('tier ${entry.tier.describe()}',
              style: Theme.of(context).textTheme.bodySmall),
          HashLabel('prev', entry.prevHash),
          HashLabel('hash', entry.hash),
        ],
      ),
    );
  }

  // --- model consistency ---------------------------------------------------

  Widget _modelCard(BuildContext context, GovernanceData data) {
    final Section<ModelConsistencyState>? section = data.modelConsistency;
    if (section == null) {
      return const PanelCard(
        title: 'Model-version consistency',
        children: <Widget>[
          Kv('Status', panelsNotRecorded),
          Caption(
            'No archive selected. Consistency is archive-scoped '
            '(GET /hub/archives/{id}/model-consistency); there is no '
            'companion-scoped endpoint.',
          ),
        ],
      );
    }
    if (!section.isOk) {
      return PanelCard(
        title: 'Model-version consistency',
        children: sectionErrorRows(context, section),
      );
    }
    final ModelConsistencyState model = section.value!;
    final bool consistent = model.consistent.value == true;
    return PanelCard(
      title: 'Model-version consistency',
      trailing: Badge(
        model.consistent.describe(),
        color: consistent ? Colors.green : Colors.red,
        icon: consistent ? Icons.check_circle : Icons.error,
      ),
      children: <Widget>[
        Kv('Code', model.code.describe()),
        Kv('Versions', listText(model.versions)),
        Kv('Reason', model.reason.isEmpty ? panelsNotRecorded : model.reason),
        ..._modelSeats(context, model),
      ],
    );
  }

  List<Widget> _modelSeats(BuildContext context, ModelConsistencyState model) {
    if (model.seats.isUnknown) {
      return const <Widget>[Kv('Seats', panelsNotRecorded)];
    }
    final List<ModelUsage> seats = model.seats.value!;
    if (seats.isEmpty) {
      return const <Widget>[Kv('Seats', 'none recorded')];
    }
    return <Widget>[
      Kv('Seats', '${seats.length}'),
      for (final ModelUsage usage in seats.take(10))
        Padding(
          padding: const EdgeInsets.symmetric(vertical: 2),
          child: Wrap(
            spacing: 6,
            runSpacing: 3,
            crossAxisAlignment: WrapCrossAlignment.center,
            children: <Widget>[
              Text(
                usage.seatId.isEmpty ? panelsNotRecorded : usage.seatId,
                style: Theme.of(context).textTheme.bodySmall,
              ),
              Kv('model', usage.modelVersion.describe()),
              Badge(
                usage.official.value == true ? 'official' : 'participant',
                color: Colors.blueGrey,
              ),
            ],
          ),
        ),
    ];
  }
}
