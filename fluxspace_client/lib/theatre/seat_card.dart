import 'package:flutter/material.dart';

import 'placeholder_assets.dart';
import 'theatre_models.dart';

/// A persona card: portrait, name, state chip and an optional vote badge.
///
/// The card is width-driven by its parent (the stage uses a bounded
/// [SizedBox]); all text is single-line with ellipsis so a narrow card cannot
/// push an unbounded [Row] into a RenderFlex overflow.
class SeatCard extends StatelessWidget {
  const SeatCard({
    super.key,
    required this.seat,
    required this.assets,
    required this.spotlighted,
    this.vote,
  });

  final TheatreSeat seat;
  final TheatreAssets assets;
  final bool spotlighted;

  /// Optional ballot cast by this seat; `null` = no vote badge.
  final TheatreVote? vote;

  @override
  Widget build(BuildContext context) {
    final ThemeData theme = Theme.of(context);
    final String name = seat.displayName ?? 'not recorded';
    final String stateLabel = seat.state?.label ?? 'not recorded';
    final Color stateColor = _stateColor(seat.state, theme);

    return Card(
      elevation: spotlighted ? 6 : 1,
      color: theme.colorScheme.surface,
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(12),
        side: BorderSide(
          color: spotlighted
              ? theme.colorScheme.primary
              : theme.colorScheme.outlineVariant,
          width: spotlighted ? 2 : 1,
        ),
      ),
      child: Padding(
        padding: const EdgeInsets.all(10),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.center,
          children: <Widget>[
            ClipRRect(
              borderRadius: BorderRadius.circular(10),
              child: assets.portrait(seat.personaId, 56),
            ),
            const SizedBox(height: 8),
            Text(
              name,
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
              textAlign: TextAlign.center,
              style: theme.textTheme.titleSmall,
            ),
            const SizedBox(height: 6),
            Chip(
              label: Text(stateLabel),
              backgroundColor: stateColor.withAlpha(48),
              labelStyle:
                  theme.textTheme.labelSmall?.copyWith(color: stateColor),
              visualDensity: VisualDensity.compact,
              padding: EdgeInsets.zero,
              materialTapTargetSize: MaterialTapTargetSize.shrinkWrap,
            ),
            if (seat.isOfficial == true) ...<Widget>[
              const SizedBox(height: 4),
              const _OfficialBadge(),
            ],
            if (vote != null) ...<Widget>[
              const SizedBox(height: 4),
              _VoteBadge(vote: vote!),
            ],
          ],
        ),
      ),
    );
  }
}

Color _stateColor(TheatreSeatState? state, ThemeData theme) {
  switch (state) {
    case TheatreSeatState.thinking:
      return theme.colorScheme.tertiary;
    case TheatreSeatState.speaking:
      return theme.colorScheme.primary;
    case TheatreSeatState.voted:
      return theme.colorScheme.secondary;
    case TheatreSeatState.eliminated:
      return theme.colorScheme.error;
    case null:
      return theme.colorScheme.outline;
  }
}

class _OfficialBadge extends StatelessWidget {
  const _OfficialBadge();

  @override
  Widget build(BuildContext context) {
    final ThemeData theme = Theme.of(context);
    return Row(
      mainAxisSize: MainAxisSize.min,
      children: <Widget>[
        Icon(Icons.verified, size: 12, color: theme.colorScheme.primary),
        const SizedBox(width: 4),
        Text('Official', style: theme.textTheme.labelSmall),
      ],
    );
  }
}

class _VoteBadge extends StatelessWidget {
  const _VoteBadge({required this.vote});

  final TheatreVote vote;

  @override
  Widget build(BuildContext context) {
    final ThemeData theme = Theme.of(context);
    final String target = vote.targetId ?? 'not recorded';
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 4),
      decoration: BoxDecoration(
        color: theme.colorScheme.primaryContainer,
        borderRadius: BorderRadius.circular(12),
      ),
      child: Text(
        vote.label ?? 'voted -> $target',
        maxLines: 1,
        overflow: TextOverflow.ellipsis,
        style: theme.textTheme.labelSmall
            ?.copyWith(color: theme.colorScheme.onPrimaryContainer),
      ),
    );
  }
}
