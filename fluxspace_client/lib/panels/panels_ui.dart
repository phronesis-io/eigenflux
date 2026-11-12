/// Shared, overflow-safe building blocks for the observability panels.
///
/// Every multi-child layout uses [Wrap] or an [Expanded]-bounded [Row], so a
/// narrow (800x600) viewport never produces a RenderFlex overflow.
library;

import 'package:flutter/material.dart';

import 'panels_models.dart';

const double panelsGutter = 12.0;

/// A titled card whose body is a start-aligned column.
class PanelCard extends StatelessWidget {
  const PanelCard({
    super.key,
    required this.title,
    required this.children,
    this.trailing,
  });

  final String title;
  final List<Widget> children;
  final Widget? trailing;

  @override
  Widget build(BuildContext context) {
    return Card(
      margin: const EdgeInsets.only(bottom: panelsGutter),
      child: Padding(
        padding: const EdgeInsets.all(panelsGutter),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          mainAxisSize: MainAxisSize.min,
          children: <Widget>[
            Wrap(
              spacing: 8,
              runSpacing: 4,
              crossAxisAlignment: WrapCrossAlignment.center,
              children: <Widget>[
                Text(title, style: Theme.of(context).textTheme.titleMedium),
                if (trailing != null) trailing!,
              ],
            ),
            const SizedBox(height: 8),
            ...children,
          ],
        ),
      ),
    );
  }
}

/// A label/value line that wraps. An unknown value renders italic grey
/// "not recorded".
class Kv extends StatelessWidget {
  const Kv(this.label, this.value, {super.key, this.valueColor});

  final String label;
  final String value;
  final Color? valueColor;

  @override
  Widget build(BuildContext context) {
    final bool unknown = value == panelsNotRecorded || value.isEmpty;
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 2),
      child: Wrap(
        spacing: 6,
        runSpacing: 2,
        crossAxisAlignment: WrapCrossAlignment.center,
        children: <Widget>[
          Text(
            '$label:',
            style: Theme.of(context)
                .textTheme
                .bodySmall
                ?.copyWith(fontWeight: FontWeight.w600),
          ),
          Text(
            value.isEmpty ? panelsNotRecorded : value,
            style: Theme.of(context).textTheme.bodyMedium?.copyWith(
                  color: valueColor ?? (unknown ? Colors.grey : null),
                  fontStyle: unknown ? FontStyle.italic : FontStyle.normal,
                ),
          ),
        ],
      ),
    );
  }
}

/// A compact coloured status chip.
class Badge extends StatelessWidget {
  const Badge(this.label, {super.key, required this.color, this.icon});

  final String label;
  final Color color;
  final IconData? icon;

  @override
  Widget build(BuildContext context) {
    return Chip(
      avatar: icon != null ? Icon(icon, size: 14, color: color) : null,
      label: Text(label, style: TextStyle(color: color, fontSize: 11)),
      backgroundColor: color.withValues(alpha: 0.12),
      side: BorderSide(color: color.withValues(alpha: 0.45)),
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(8)),
      visualDensity: VisualDensity.compact,
      materialTapTargetSize: MaterialTapTargetSize.shrinkWrap,
      padding: const EdgeInsets.symmetric(horizontal: 4),
    );
  }
}

/// The explicit "governance / archive surface degraded" state. A 503 is shown
/// as a banner with its domain code, never as an empty list.
class DegradedBanner extends StatelessWidget {
  const DegradedBanner({super.key, required this.code, required this.message});

  final String code;
  final String message;

  @override
  Widget build(BuildContext context) {
    return Container(
      width: double.infinity,
      margin: const EdgeInsets.only(bottom: panelsGutter),
      padding: const EdgeInsets.all(panelsGutter),
      decoration: BoxDecoration(
        color: Colors.orange.withValues(alpha: 0.14),
        border: Border.all(color: Colors.orange.withValues(alpha: 0.6)),
        borderRadius: BorderRadius.circular(10),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        mainAxisSize: MainAxisSize.min,
        children: <Widget>[
          Wrap(
            spacing: 8,
            crossAxisAlignment: WrapCrossAlignment.center,
            children: <Widget>[
              const Icon(Icons.warning_amber_rounded, color: Colors.orange, size: 18),
              Text(
                'Surface degraded · $code (HTTP 503)',
                style: Theme.of(context)
                    .textTheme
                    .titleSmall
                    ?.copyWith(color: Colors.orange.shade900),
              ),
            ],
          ),
          const SizedBox(height: 6),
          Text(
            message.isEmpty
                ? 'The Hub stopped answering these authority queries. '
                    'This is an explicit error state, NOT an empty list.'
                : message,
            style: Theme.of(context).textTheme.bodySmall,
          ),
          const SizedBox(height: 4),
          Text(
            'This is an explicit error state, NOT an empty list.',
            style: Theme.of(context)
                .textTheme
                .bodySmall
                ?.copyWith(fontStyle: FontStyle.italic),
          ),
        ],
      ),
    );
  }
}

/// A small caption line.
class Caption extends StatelessWidget {
  const Caption(this.text, {super.key});

  final String text;

  @override
  Widget build(BuildContext context) => Padding(
        padding: const EdgeInsets.only(top: 6),
        child: Text(
          text,
          style: Theme.of(context)
              .textTheme
              .bodySmall
              ?.copyWith(color: Colors.grey.shade600),
        ),
      );
}

/// An explicit empty-collection state (distinct from a degraded endpoint).
class EmptyHint extends StatelessWidget {
  const EmptyHint(this.text, {super.key});

  final String text;

  @override
  Widget build(BuildContext context) => Padding(
        padding: const EdgeInsets.symmetric(vertical: 8),
        child: Text(
          text,
          style: Theme.of(context)
              .textTheme
              .bodyMedium
              ?.copyWith(color: Colors.grey, fontStyle: FontStyle.italic),
        ),
      );
}

/// Renders a failed [Section] as readable rows, preserving the domain code.
List<Widget> sectionErrorRows(BuildContext context, Section<dynamic> section) {
  final PanelsException error = section.error!;
  return <Widget>[
    Kv('Endpoint', '${error.code} (HTTP ${error.status})', valueColor: Colors.deepOrange),
    Kv('Detail', error.message.isEmpty ? panelsNotRecorded : error.message),
    Caption('This section failed; its data is not recorded, not empty.'),
  ];
}

/// Plain display text for any scalar [Field].
String fieldText(Field<dynamic> field) => field.describe();

/// Display text for an epoch-millisecond [Field].
String tsText(Field<int> field) => field.map(formatEpochMs);

/// Display text for a recorded list of strings.
String listText(Field<List<String>> field, {String empty = 'none recorded'}) {
  if (field.isUnknown) return panelsNotRecorded;
  final List<String> value = field.value!;
  return value.isEmpty ? empty : value.join(', ');
}

/// A compact hash label that never overflows.
class HashLabel extends StatelessWidget {
  const HashLabel(this.label, this.field, {super.key});

  final String label;
  final Field<String> field;

  @override
  Widget build(BuildContext context) => Text(
        '$label: ${field.isRecorded ? shortHash(field.value!) : panelsNotRecorded}',
        style: Theme.of(context).textTheme.bodySmall,
        overflow: TextOverflow.ellipsis,
      );
}
