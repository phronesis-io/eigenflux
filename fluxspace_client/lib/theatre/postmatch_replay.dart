import 'dart:async';

import 'package:flutter/material.dart';

import 'theatre_models.dart';

/// Round-by-round scrubber over a [List] of [TheatreFrame]s.
///
/// Play/pause auto-advances with a [Timer]; step buttons and a [Slider] scrub
/// manually. When [divergenceRound] is supplied (a same-spec reprise) the round
/// where decisions first diverged is labelled. The widget owns no frame state:
/// the parent holds the selected [index] and [onIndexChanged] reports changes,
/// which is what lets a single selected frame drive the whole stage.
class PostmatchReplay extends StatefulWidget {
  const PostmatchReplay({
    super.key,
    required this.frames,
    required this.index,
    this.onIndexChanged,
    this.divergenceRound,
    this.playInterval = const Duration(milliseconds: 900),
  });

  final List<TheatreFrame> frames;
  final int index;

  /// `null` disables all controls (live mode / read-only history).
  final ValueChanged<int>? onIndexChanged;

  /// Optional round where a same-spec reprise first diverged.
  final int? divergenceRound;

  final Duration playInterval;

  @override
  State<PostmatchReplay> createState() => _PostmatchReplayState();
}

class _PostmatchReplayState extends State<PostmatchReplay> {
  Timer? _timer;
  bool _playing = false;

  bool get _enabled => widget.onIndexChanged != null && widget.frames.length > 1;

  void _setIndex(int i) {
    final int max = widget.frames.length - 1;
    final int clamped = i < 0 ? 0 : (i > max ? max : i);
    widget.onIndexChanged?.call(clamped);
  }

  void _togglePlay() {
    if (_playing) {
      _stop();
      return;
    }
    if (!_enabled) return;
    setState(() => _playing = true);
    _timer = Timer.periodic(widget.playInterval, (Timer timer) {
      final int next = widget.index + 1;
      if (next >= widget.frames.length) {
        _stop();
        return;
      }
      _setIndex(next);
    });
  }

  void _stop() {
    _timer?.cancel();
    _timer = null;
    if (_playing && mounted) setState(() => _playing = false);
  }

  @override
  void didUpdateWidget(covariant PostmatchReplay oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (_playing && !_enabled) _stop();
  }

  @override
  void dispose() {
    _timer?.cancel();
    _timer = null;
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final ThemeData theme = Theme.of(context);
    final int count = widget.frames.length;
    final int current = _clamp(widget.index, count);
    final TheatreFrame? frame = count == 0 ? null : widget.frames[current];
    final String roundLabel =
        frame?.round == null ? 'not recorded' : '${frame!.round}';
    final int? divergence = widget.divergenceRound;

    return Card(
      child: Padding(
        padding: const EdgeInsets.all(12),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: <Widget>[
            Wrap(
              spacing: 8,
              runSpacing: 6,
              crossAxisAlignment: WrapCrossAlignment.center,
              children: <Widget>[
                Text('Replay', style: theme.textTheme.titleSmall),
                _InfoChip(theme: theme, label: 'frame ${current + 1} / $count'),
                _InfoChip(theme: theme, label: 'round $roundLabel'),
                if (divergence != null)
                  Chip(
                    avatar: const Icon(Icons.call_split, size: 16),
                    label: Text('divergence - round $divergence'),
                    backgroundColor: theme.colorScheme.tertiaryContainer,
                    visualDensity: VisualDensity.compact,
                  ),
              ],
            ),
            const SizedBox(height: 8),
            if (count > 1)
              Slider(
                key: const Key('replay_slider'),
                value: current.toDouble(),
                min: 0,
                max: (count - 1).toDouble(),
                divisions: count - 1,
                label: 'frame ${current + 1}',
                onChanged: _enabled
                    ? (double value) => _setIndex(value.round())
                    : null,
              )
            else
              Text(
                'single frame - no scrubbing',
                textAlign: TextAlign.center,
                style: theme.textTheme.bodySmall,
              ),
            const SizedBox(height: 4),
            // Wrap, not Row: three controls with labels must wrap on a narrow
            // window instead of overflowing.
            Wrap(
              spacing: 8,
              runSpacing: 8,
              alignment: WrapAlignment.center,
              children: <Widget>[
                IconButton(
                  key: const Key('replay_prev'),
                  tooltip: 'Previous frame',
                  onPressed: _enabled ? () => _setIndex(current - 1) : null,
                  icon: const Icon(Icons.skip_previous),
                ),
                IconButton(
                  key: const Key('replay_play'),
                  tooltip: _playing ? 'Pause' : 'Play',
                  onPressed: _enabled ? _togglePlay : null,
                  icon: Icon(_playing ? Icons.pause : Icons.play_arrow),
                ),
                IconButton(
                  key: const Key('replay_next'),
                  tooltip: 'Next frame',
                  onPressed: _enabled ? () => _setIndex(current + 1) : null,
                  icon: const Icon(Icons.skip_next),
                ),
              ],
            ),
          ],
        ),
      ),
    );
  }

  int _clamp(int index, int length) {
    if (length <= 0) return 0;
    if (index < 0) return 0;
    if (index >= length) return length - 1;
    return index;
  }
}

class _InfoChip extends StatelessWidget {
  const _InfoChip({required this.theme, required this.label});

  final ThemeData theme;
  final String label;

  @override
  Widget build(BuildContext context) {
    return Chip(
      label: Text(label),
      visualDensity: VisualDensity.compact,
      backgroundColor: theme.colorScheme.surfaceContainerHighest,
    );
  }
}
