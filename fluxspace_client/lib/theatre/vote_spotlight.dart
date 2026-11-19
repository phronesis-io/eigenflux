import 'package:flutter/material.dart';

import 'placeholder_assets.dart';
import 'theatre_models.dart';

/// Animated voting reveal with winner emphasis.
///
/// Uses a single [AnimationController] owned by this widget (via
/// [TickerProviderStateMixin]); the controller is disposed in [dispose]. The
/// asset pack supplies only the decorative [TheatreAssets.voteFlourish] — it
/// must NOT run its own animation loop, because the timing lives here. The
/// reveal restarts whenever a *new* [reveal] object arrives (a fresh reveal in
/// a live stream, or stepping to a frame with a different reveal).
class VoteSpotlight extends StatefulWidget {
  const VoteSpotlight({
    super.key,
    required this.assets,
    this.reveal,
    this.duration = const Duration(milliseconds: 700),
  });

  final TheatreAssets assets;

  /// Aggregated reveal; `null` = no reveal recorded, shown as a static notice.
  final TheatreReveal? reveal;

  /// Duration of the reveal animation.
  final Duration duration;

  @override
  State<VoteSpotlight> createState() => _VoteSpotlightState();
}

class _VoteSpotlightState extends State<VoteSpotlight>
    with TickerProviderStateMixin {
  late AnimationController _controller;

  @override
  void initState() {
    super.initState();
    _controller = AnimationController(vsync: this, duration: widget.duration);
    if (widget.reveal != null) _controller.forward();
  }

  @override
  void didUpdateWidget(covariant VoteSpotlight oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (widget.reveal != oldWidget.reveal && widget.reveal != null) {
      _controller
        ..reset()
        ..forward();
    }
  }

  @override
  void dispose() {
    _controller.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final ThemeData theme = Theme.of(context);
    final TheatreReveal? reveal = widget.reveal;
    return SizedBox(
      height: 132,
      child: Stack(
        children: <Widget>[
          Positioned.fill(
            child: AnimatedBuilder(
              animation: _controller,
              builder: (BuildContext context, Widget? _) =>
                  widget.assets.voteFlourish(_controller.value),
            ),
          ),
          Center(
            child: Padding(
              padding: const EdgeInsets.all(12),
              child: reveal == null
                  ? _NoReveal(theme: theme)
                  : _RevealContent(
                      theme: theme,
                      reveal: reveal,
                      animation: _controller,
                    ),
            ),
          ),
        ],
      ),
    );
  }
}

class _RevealContent extends StatelessWidget {
  const _RevealContent({
    required this.theme,
    required this.reveal,
    required this.animation,
  });

  final ThemeData theme;
  final TheatreReveal reveal;
  final Animation<double> animation;

  @override
  Widget build(BuildContext context) {
    final String winner = reveal.winnerId ?? 'no winner recorded';
    final List<MapEntry<String, int>> entries = reveal.tally.entries.toList()
      ..sort((MapEntry<String, int> a, MapEntry<String, int> b) =>
          b.value.compareTo(a.value));
    return Column(
      mainAxisSize: MainAxisSize.min,
      children: <Widget>[
        ScaleTransition(
          scale: Tween<double>(begin: 0.85, end: 1.0).animate(
            CurvedAnimation(parent: animation, curve: Curves.easeOutBack),
          ),
          child: FadeTransition(
            opacity: animation,
            child: Text(
              'Winner: $winner',
              textAlign: TextAlign.center,
              style: theme.textTheme.titleMedium?.copyWith(
                color: theme.colorScheme.primary,
                fontWeight: FontWeight.bold,
              ),
            ),
          ),
        ),
        const SizedBox(height: 6),
        if (reveal.note != null)
          Text(
            reveal.note!,
            maxLines: 1,
            overflow: TextOverflow.ellipsis,
            textAlign: TextAlign.center,
            style: theme.textTheme.bodySmall,
          ),
        const SizedBox(height: 6),
        // Wrap, not Row: an arbitrary tally must never overflow a narrow box.
        Wrap(
          spacing: 6,
          runSpacing: 4,
          alignment: WrapAlignment.center,
          children: <Widget>[
            for (final MapEntry<String, int> entry in entries)
              Chip(
                label: Text('${entry.key}: ${entry.value}'),
                visualDensity: VisualDensity.compact,
                padding: EdgeInsets.zero,
              ),
          ],
        ),
      ],
    );
  }
}

class _NoReveal extends StatelessWidget {
  const _NoReveal({required this.theme});

  final ThemeData theme;

  @override
  Widget build(BuildContext context) {
    return Text(
      'no reveal recorded',
      style: theme.textTheme.bodySmall?.copyWith(color: theme.colorScheme.outline),
    );
  }
}
