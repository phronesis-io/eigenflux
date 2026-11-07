import 'dart:math' as math;

import 'package:flutter/material.dart';

/// The asset contract for the Flux Theatre render layer.
///
/// A "real" pack is a single implementation of this class backed by the
/// procured Rive artboards and the 2.5D stage kit (see
/// `production/epics/flux-theatre/S3_ASSET_PROCUREMENT.md`). The lead swaps the
/// placeholder by passing a different [TheatreAssets] to `TheatreScreen`; no
/// widget in `lib/theatre/` imports a concrete asset type, a Rive API, or a
/// Flame API.
///
/// ## What a real Rive/2.5D pack MUST implement
///
/// All five methods. Each returns a `Widget` that fills the box it is given
/// without throwing, for **any** argument the render layer can pass:
///
/// - [portrait] — a persona portrait. [personaId] may be `null` ("not
///   recorded") or an unrecognised id; the pack must render a neutral, clearly
///   labelled placeholder rather than throwing or silently reusing another
///   persona's art. [size] is the box edge in logical pixels and may be zero.
/// - [backdrop] — full-bleed stage background. Must fill any box, including a
///   zero-sized one during a layout pass.
/// - [spotlight] — the active-speaker light. [intensity] is 0..1;
///   [reducedMotion] `true` must yield a static, intentional pose (no ambient
///   loop and no drift), per the accessibility contract in the art direction.
/// - [voteFlourish] — decorative reveal art. [progress] is 0..1 from the
///   reveal `AnimationController`; the pack must NOT own its own animation loop
///   (timing lives in `vote_spotlight.dart`).
/// - [ambient] — mood / light-grade overlay. [lightGrade] is one of
///   `neutral`/`warm`/`cold`/`alert` (the four `stage.json` grades) or `null`
///   when not recorded; `null` must be a neutral overlay.
///
/// ## What stays unchanged after the swap
///
/// Everything in `theatre_models.dart`, `stage_view.dart`, `seat_card.dart`,
/// `vote_spotlight.dart`, `postmatch_replay.dart` and `theatre_screen.dart`.
/// The frame vocabulary, the mapping contract, the layout, the scrubber, the
/// animation timing and the tests are all asset-agnostic. Only this file is
/// replaced, and only the `TheatreAssets` argument passed to `TheatreScreen`
/// changes at the call site.
abstract class TheatreAssets {
  const TheatreAssets();

  /// Persona portrait. [personaId] selects the art; [size] is the box edge.
  Widget portrait(String? personaId, double size);

  /// Full-bleed stage backdrop.
  Widget backdrop();

  /// Active-speaker spotlight. [intensity] 0..1; [reducedMotion] is the host
  /// accessibility flag.
  Widget spotlight(double intensity, bool reducedMotion);

  /// Decorative vote-reveal flourish. [progress] 0..1 from the host controller.
  Widget voteFlourish(double progress);

  /// Ambient light-grade overlay.
  Widget ambient(String? lightGrade);
}

/// Pure-Flutter placeholder art: gradients and shapes drawn with
/// [CustomPainter], no image assets, no `rive`, no `flame`.
///
/// These drawings are a *layout and behaviour harness*, NOT a substitute for
/// the procured Rive/2.5D assets. They prove that the stage, seats, spotlight,
/// reveal and scrubber all compose and animate correctly at narrow widths.
/// They carry no persona body language, no 2.5D parallax and no 60fps rig, so
/// they cannot satisfy S3's exit criterion on their own.
class PlaceholderAssets extends TheatreAssets {
  const PlaceholderAssets();

  @override
  Widget portrait(String? personaId, double size) {
    final double edge = size <= 0 ? 1.0 : size;
    return SizedBox(
      width: edge,
      height: edge,
      child: CustomPaint(
        painter: _PortraitPainter(_portraitRgb(personaId), personaId ?? 'not recorded'),
      ),
    );
  }

  @override
  Widget backdrop() =>
      const SizedBox.expand(child: CustomPaint(painter: _BackdropPainter()));

  @override
  Widget spotlight(double intensity, bool reducedMotion) => SizedBox.expand(
        child: CustomPaint(painter: _SpotlightPainter(intensity, reducedMotion)),
      );

  @override
  Widget voteFlourish(double progress) => SizedBox.expand(
        child: CustomPaint(painter: _VoteFlourishPainter(progress)),
      );

  @override
  Widget ambient(String? lightGrade) => SizedBox.expand(
        child: CustomPaint(painter: _AmbientPainter(lightGrade)),
      );
}

// --- palette ---------------------------------------------------------------
// Four low-saturation accents roughly echo the four MVP personas so that the
// placeholder seats are visually distinguishable without pretending to be the
// procured character art. Selection is a stable hash of the persona id.

const List<List<int>> _palette = <List<int>>[
  <int>[126, 111, 176], // violet
  <int>[79, 163, 199], // cyan
  <int>[111, 169, 140], // green
  <int>[176, 122, 111], // amber
];

List<int> _portraitRgb(String? personaId) {
  if (personaId == null || personaId.isEmpty) {
    return const <int>[158, 158, 158]; // neutral grey = not recorded
  }
  final int hash = personaId.codeUnits
      .fold<int>(0, (int h, int c) => (h * 31 + c) & 0x7fffffff);
  return _palette[hash % _palette.length];
}

/// Builds a colour from explicit channels and a 0..1 opacity. Uses the stable
/// `Color.fromARGB` constructor so no version-specific colour API is involved.
Color _rgba(int r, int g, int b, double opacity) {
  final double o = opacity < 0.0 ? 0.0 : (opacity > 1.0 ? 1.0 : opacity);
  return Color.fromARGB((o * 255).round(), r, g, b);
}

// --- painters --------------------------------------------------------------
// Every painter guards against a degenerate (zero) size so a layout pass can
// never throw; `shouldRepaint` keeps redraws bounded.

class _BackdropPainter extends CustomPainter {
  const _BackdropPainter();

  @override
  void paint(Canvas canvas, Size size) {
    if (size.width <= 0 || size.height <= 0) return;
    final Rect full = Offset.zero & size;
    canvas.drawRect(
      full,
      Paint()
        ..shader = const LinearGradient(
          begin: Alignment.topLeft,
          end: Alignment.bottomRight,
          colors: <Color>[
            Color(0xFF1B1830),
            Color(0xFF16303A),
            Color(0xFF0E2233),
          ],
        ).createShader(full),
    );
    final Rect band =
        Rect.fromLTWH(0, size.height * 0.62, size.width, size.height * 0.38);
    canvas.drawRect(
      band,
      Paint()
        ..shader = LinearGradient(
          begin: Alignment.topCenter,
          end: Alignment.bottomCenter,
          colors: <Color>[
            _rgba(0, 0, 0, 0.0),
            _rgba(110, 111, 168, 0.20),
          ],
        ).createShader(band),
    );
    // Low-contrast "glass" arcs are a hint of depth, not real parallax.
    final Paint arc = Paint()
      ..color = _rgba(159, 211, 224, 0.10)
      ..style = PaintingStyle.stroke
      ..strokeWidth = 2;
    for (int i = 1; i <= 3; i++) {
      canvas.drawCircle(
        Offset(size.width * 0.5, size.height * 0.92),
        size.width * 0.16 * i,
        arc,
      );
    }
  }

  @override
  bool shouldRepaint(covariant _BackdropPainter oldDelegate) => false;
}

class _PortraitPainter extends CustomPainter {
  const _PortraitPainter(this.rgb, this.key);

  final List<int> rgb;
  final String key;

  @override
  void paint(Canvas canvas, Size size) {
    if (size.width <= 0 || size.height <= 0) return;
    final double w = size.width;
    final double h = size.height;
    final Rect full = Offset.zero & size;
    canvas.drawRRect(
      RRect.fromRectAndRadius(full, Radius.circular(w * 0.18)),
      Paint()
        ..shader = RadialGradient(
          center: const Alignment(0, -0.4),
          radius: 1.2,
          colors: <Color>[
            _rgba(rgb[0], rgb[1], rgb[2], 0.55),
            const Color(0xFF14121F),
          ],
        ).createShader(full),
    );
    // A simple head + shoulders silhouette. These are stand-in shapes, not
    // character art: there is no pose, expression or body language here.
    final Paint figure =
        Paint()..color = _rgba(rgb[0], rgb[1], rgb[2], 0.92);
    canvas.drawCircle(Offset(w * 0.5, h * 0.40), w * 0.17, figure);
    canvas.drawOval(
      Rect.fromCenter(
        center: Offset(w * 0.5, h * 0.88),
        width: w * 0.62,
        height: h * 0.44,
      ),
      figure,
    );
    // Two tick marks make the placeholder unmistakable at a glance.
    final Paint tick = Paint()
      ..color = _rgba(255, 255, 255, 0.35)
      ..strokeWidth = 1.5;
    canvas.drawLine(
      Offset(w * 0.16, h * 0.10),
      Offset(w * 0.34, h * 0.10),
      tick,
    );
    canvas.drawLine(
      Offset(w * 0.16, h * 0.15),
      Offset(w * 0.27, h * 0.15),
      tick,
    );
  }

  @override
  bool shouldRepaint(covariant _PortraitPainter oldDelegate) =>
      oldDelegate.key != key;
}

class _SpotlightPainter extends CustomPainter {
  const _SpotlightPainter(this.intensity, this.reducedMotion);

  final double intensity;
  final bool reducedMotion;

  @override
  void paint(Canvas canvas, Size size) {
    if (size.width <= 0 || size.height <= 0) return;
    final double a =
        intensity < 0.0 ? 0.0 : (intensity > 1.0 ? 1.0 : intensity);
    final Path cone = Path()
      ..moveTo(size.width * 0.5, 0)
      ..lineTo(size.width * 0.06, size.height)
      ..lineTo(size.width * 0.94, size.height)
      ..close();
    canvas.drawPath(
      cone,
      Paint()
        ..shader = LinearGradient(
          begin: Alignment.topCenter,
          end: Alignment.bottomCenter,
          colors: <Color>[
            _rgba(255, 233, 168, 0.50 * a),
            _rgba(159, 211, 224, 0.04 * a),
          ],
        ).createShader(Offset.zero & size),
    );
    // reducedMotion: one static rim instead of any animated bloom/drift.
    if (reducedMotion) {
      canvas.drawPath(
        cone,
        Paint()
          ..color = _rgba(255, 233, 168, 0.8)
          ..style = PaintingStyle.stroke
          ..strokeWidth = 2,
      );
    }
  }

  @override
  bool shouldRepaint(covariant _SpotlightPainter oldDelegate) =>
      oldDelegate.intensity != intensity ||
      oldDelegate.reducedMotion != reducedMotion;
}

class _VoteFlourishPainter extends CustomPainter {
  const _VoteFlourishPainter(this.progress);

  final double progress;

  @override
  void paint(Canvas canvas, Size size) {
    if (size.width <= 0 || size.height <= 0) return;
    final double p =
        progress < 0.0 ? 0.0 : (progress > 1.0 ? 1.0 : progress);
    final Offset center = Offset(size.width / 2, size.height / 2);
    final double maxR = size.shortestSide / 2;
    final Paint ray = Paint()
      ..color = _rgba(159, 224, 208, 0.5 * p)
      ..strokeWidth = 3
      ..strokeCap = StrokeCap.round;
    const int rays = 12;
    final int visible = (rays * p).round();
    for (int i = 0; i < visible; i++) {
      final double theta = (2 * math.pi / rays) * i;
      final Offset dir = Offset(math.cos(theta), math.sin(theta));
      canvas.drawLine(
        center + dir * (maxR * 0.35),
        center + dir * (maxR * 0.85),
        ray,
      );
    }
    canvas.drawCircle(
      center,
      maxR * 0.30 * p,
      Paint()..color = _rgba(159, 224, 208, 0.25 * p),
    );
  }

  @override
  bool shouldRepaint(covariant _VoteFlourishPainter oldDelegate) =>
      oldDelegate.progress != progress;
}

class _AmbientPainter extends CustomPainter {
  const _AmbientPainter(this.lightGrade);

  final String? lightGrade;

  @override
  void paint(Canvas canvas, Size size) {
    if (size.width <= 0 || size.height <= 0) return;
    final List<int>? rgb = switch (lightGrade) {
      'warm' => const <int>[224, 168, 111],
      'cold' => const <int>[111, 168, 208],
      'alert' => const <int>[208, 111, 111],
      _ => null, // neutral / not recorded -> no tint
    };
    if (rgb == null) return;
    canvas.drawRect(
      Offset.zero & size,
      Paint()..color = _rgba(rgb[0], rgb[1], rgb[2], 0.16),
    );
  }

  @override
  bool shouldRepaint(covariant _AmbientPainter oldDelegate) =>
      oldDelegate.lightGrade != lightGrade;
}
