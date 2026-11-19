import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';

import 'placeholder_assets.dart';
import 'ai_art_assets.dart';
import 'postmatch_replay.dart';
import 'stage_view.dart';
import 'theatre_models.dart';
import 'vote_spotlight.dart';

/// The single widget the lead adds to the app to show the Flux Theatre.
///
/// Constructor (kept deliberately simple):
/// ```
/// TheatreScreen(
///   frames: <TheatreFrame>[frame1, frame2, ...],  // replay/archive: scrubber on
///   // OR
///   frame: myValueNotifier,   // ValueListenable<TheatreFrame>: live, no scrubber
///   assets: defaultTheatreAssets,      // ADR-0012: PlaceholderAssets or AI-art interim via flag
///   divergenceRound: 3,                // optional same-spec reprise marker
///   title: 'The Locked Ward',          // optional heading
/// )
/// ```
/// Supply exactly one of [frames] / [frame]:
///
/// - [frames] is a recorded timeline. It enables the round-by-round scrubber;
///   the selected frame drives the stage, seats, spotlight and reveal.
/// - [frame] is a [ValueListenable] of one live [TheatreFrame]. The screen
///   rebuilds on each notification; the timeline is that single frame, so the
///   scrubber is disabled.
///
/// If both are supplied, [frame] takes precedence (a live match wins over a
/// recorded one). [assets] defaults to [PlaceholderAssets]; ADR-0012 adds
/// [AiArtAssets] as an AI-generated, labelled interim selected by
/// `defaultTheatreAssets` (off by default via `kUseAiArtInterim`). It is the
/// only argument that changes when the art pack is swapped.
///
/// The body is a [SingleChildScrollView] around a `mainAxisSize.min` [Column]:
/// vertical content scrolls instead of overflowing a fixed-height [Column].
/// The stage itself uses `LayoutBuilder` + `Wrap` to stay within narrow widths.
class TheatreScreen extends StatefulWidget {
  const TheatreScreen({
    super.key,
    this.frames,
    this.frame,
    this.assets = const PlaceholderAssets(),
    this.divergenceRound,
    this.title,
  });

  /// Recorded timeline (replay / archive mode). Nullable; empty when absent.
  final List<TheatreFrame>? frames;

  /// Live frame source. When non-null it takes precedence over [frames].
  final ValueListenable<TheatreFrame>? frame;

  /// Art pack. Defaults to [PlaceholderAssets]; swap in the procured pack here.
  final TheatreAssets assets;

  /// Optional round where a same-spec reprise first diverged.
  final int? divergenceRound;

  /// Optional heading; falls back to the frame's own title, then 'Flux Theatre'.
  final String? title;

  @override
  State<TheatreScreen> createState() => _TheatreScreenState();
}

class _TheatreScreenState extends State<TheatreScreen> {
  int _index = 0;

  @override
  Widget build(BuildContext context) {
    final ValueListenable<TheatreFrame>? live = widget.frame;
    if (live != null) {
      return ValueListenableBuilder<TheatreFrame>(
        valueListenable: live,
        builder: (BuildContext context, TheatreFrame value, Widget? _) =>
            _compose(context, <TheatreFrame>[value], live: true),
      );
    }
    return _compose(
      context,
      widget.frames ?? const <TheatreFrame>[],
      live: false,
    );
  }

  Widget _compose(
    BuildContext context,
    List<TheatreFrame> timeline, {
    required bool live,
  }) {
    final int index = _clampIndex(_index, timeline.length);
    final TheatreFrame current =
        timeline.isEmpty ? const TheatreFrame() : timeline[index];
    final ThemeData theme = Theme.of(context);

    return Material(
      color: theme.colorScheme.surface,
      child: SafeArea(
        child: SingleChildScrollView(
          padding: const EdgeInsets.all(12),
          child: Column(
            mainAxisSize: MainAxisSize.min,
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: <Widget>[
              _header(context, current),
              const SizedBox(height: 12),
              StageView(frame: current, assets: widget.assets),
              const SizedBox(height: 12),
              VoteSpotlight(assets: widget.assets, reveal: current.reveal),
              const SizedBox(height: 12),
              PostmatchReplay(
                frames: timeline,
                index: index,
                onIndexChanged: live
                    ? null
                    : (int i) => setState(() => _index = i),
                divergenceRound: widget.divergenceRound,
              ),
            ],
          ),
        ),
      ),
    );
  }

  Widget _header(BuildContext context, TheatreFrame frame) {
    final ThemeData theme = Theme.of(context);
    return Column(
      mainAxisSize: MainAxisSize.min,
      crossAxisAlignment: CrossAxisAlignment.start,
      children: <Widget>[
        Wrap(
          spacing: 8,
          runSpacing: 6,
          crossAxisAlignment: WrapCrossAlignment.center,
          children: <Widget>[
            Text(
              widget.title ?? frame.title ?? 'Flux Theatre',
              style: theme.textTheme.titleLarge,
            ),
            Chip(
              label: Text(
                widget.assets is AiArtAssets
                    ? 'AI-art interim — not S3 final'
                    : 'placeholder art',
              ),
              visualDensity: VisualDensity.compact,
            ),
            if (frame.viewMode != null)
              Chip(
                label: Text(frame.viewMode!),
                visualDensity: VisualDensity.compact,
              ),
          ],
        ),
        const SizedBox(height: 6),
        Text(
          key: const Key('current_round'),
          'round ${frame.round ?? 'not recorded'}'
          ' - ${frame.phase ?? 'not recorded'}',
          style: theme.textTheme.bodyMedium,
        ),
        if (frame.summary != null) ...<Widget>[
          const SizedBox(height: 6),
          Text(
            frame.summary!,
            maxLines: 3,
            overflow: TextOverflow.ellipsis,
            style: theme.textTheme.bodySmall,
          ),
        ],
      ],
    );
  }

  int _clampIndex(int index, int length) {
    if (length <= 0) return 0;
    if (index < 0) return 0;
    if (index >= length) return length - 1;
    return index;
  }
}
