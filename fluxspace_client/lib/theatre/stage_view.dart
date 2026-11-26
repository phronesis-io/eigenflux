import 'package:flutter/material.dart';

import 'placeholder_assets.dart';
import 'seat_card.dart';
import 'theatre_models.dart';

/// Composes the stage: backdrop, ambient, round/phase indicator, an
/// active-speaker spotlight and N seats.
///
/// Layout is width-driven via [LayoutBuilder] + [Wrap]. There are no
/// unconstrained [Row]s: seat cards get a bounded width derived from the
/// available width, and [Wrap] moves them onto more lines as the window
/// narrows, so a narrow viewport wraps instead of overflowing.
class StageView extends StatelessWidget {
  const StageView({
    super.key,
    required this.frame,
    required this.assets,
  });

  final TheatreFrame frame;
  final TheatreAssets assets;

  @override
  Widget build(BuildContext context) {
    // The host accessibility flag is forwarded to the spotlight so a real pack
    // can honour reduced motion; the placeholder draws a static rim for it.
    final bool reducedMotion =
        MediaQuery.maybeOf(context)?.disableAnimations ?? false;

    return LayoutBuilder(
      builder: (BuildContext context, BoxConstraints constraints) {
        const double stagePadding = 14.0;
        const double spacing = 12.0;
        final double maxWidth =
            constraints.hasBoundedWidth ? constraints.maxWidth : 900.0;
        double available = maxWidth - stagePadding * 2;
        if (available < 1.0) available = 1.0;

        final int perRow = _perRow(maxWidth);
        double cardWidth = (available - spacing * (perRow - 1)) / perRow;
        if (cardWidth > 240.0) cardWidth = 240.0;
        if (cardWidth < 1.0) cardWidth = 1.0;
        // A single card must never exceed the space Wrap has for a run.
        if (cardWidth > available) cardWidth = available;

        return Stack(
          children: <Widget>[
            // Positioned layers fill the Stack size; they are laid out after
            // the non-positioned Padding below has determined that size.
            Positioned.fill(child: assets.backdrop()),
            Positioned.fill(child: assets.ambient(frame.lightGrade)),
            Padding(
              padding: const EdgeInsets.all(stagePadding),
              child: Column(
                mainAxisSize: MainAxisSize.min,
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: <Widget>[
                  _RoundPhaseBar(frame: frame),
                  const SizedBox(height: 12),
                  if (frame.seats.isEmpty)
                    Text(
                      'no seats recorded',
                      style: Theme.of(context).textTheme.bodySmall,
                    )
                  else
                    Wrap(
                      spacing: spacing,
                      runSpacing: spacing,
                      children: <Widget>[
                        for (final TheatreSeat seat in frame.seats)
                          SizedBox(
                            width: cardWidth,
                            child: _seat(seat, reducedMotion),
                          ),
                      ],
                    ),
                ],
              ),
            ),
          ],
        );
      },
    );
  }

  Widget _seat(TheatreSeat seat, bool reducedMotion) {
    final bool active =
        frame.activeSpeakerId != null && frame.activeSpeakerId == seat.id;
    final Widget card = SeatCard(
      seat: seat,
      vote: frame.voteFor(seat.id),
      assets: assets,
      spotlighted: active,
    );
    if (!active) return card;
    return _SpotlightFrame(
      // Keyed by the active speaker id so the spotlight can be observed to
      // "follow" activeSpeakerId across frame changes.
      key: Key('active_speaker_${seat.id}'),
      assets: assets,
      reducedMotion: reducedMotion,
      child: card,
    );
  }

  /// How many cards we aim for per run. Wrap still decides the real count;
  /// this only sizes each card sensibly for the current width.
  int _perRow(double width) {
    if (width >= 760.0) return 4;
    if (width >= 560.0) return 3;
    if (width >= 360.0) return 2;
    return 1;
  }
}

/// Wraps the active speaker's card with the asset-pack spotlight.
class _SpotlightFrame extends StatelessWidget {
  const _SpotlightFrame({
    super.key,
    required this.assets,
    required this.reducedMotion,
    required this.child,
  });

  final TheatreAssets assets;
  final bool reducedMotion;
  final Widget child;

  @override
  Widget build(BuildContext context) {
    return Stack(
      children: <Widget>[
        child, // non-positioned: determines this Stack's size
        Positioned.fill(
          child: IgnorePointer(
            child: assets.spotlight(1.0, reducedMotion),
          ),
        ),
      ],
    );
  }
}

class _RoundPhaseBar extends StatelessWidget {
  const _RoundPhaseBar({required this.frame});

  final TheatreFrame frame;

  @override
  Widget build(BuildContext context) {
    final ThemeData theme = Theme.of(context);
    final String round =
        frame.round == null ? 'not recorded' : '${frame.round}';
    final String total =
        frame.totalRounds == null ? '?' : '${frame.totalRounds}';
    final String phase = frame.phase ?? 'not recorded';

    return Wrap(
      spacing: 8,
      runSpacing: 6,
      crossAxisAlignment: WrapCrossAlignment.center,
      children: <Widget>[
        Chip(
          avatar: Icon(Icons.hourglass_empty,
              size: 16, color: theme.colorScheme.primary),
          label: Text('Round $round / $total'),
          visualDensity: VisualDensity.compact,
        ),
        Chip(
          avatar:
              Icon(Icons.layers, size: 16, color: theme.colorScheme.primary),
          label: Text('Phase: $phase'),
          visualDensity: VisualDensity.compact,
        ),
        if (frame.viewMode != null)
          Chip(
            avatar: Icon(Icons.visibility,
                size: 16, color: theme.colorScheme.primary),
            label: Text(frame.viewMode!),
            visualDensity: VisualDensity.compact,
          ),
      ],
    );
  }
}
