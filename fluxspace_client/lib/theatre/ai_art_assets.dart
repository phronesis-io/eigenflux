import 'package:flutter/material.dart';

import 'placeholder_assets.dart' show TheatreAssets;

/// AI-generated art pack for the Flux Theatre render layer (ADR-0012).
///
/// Implements the same [TheatreAssets] contract as [PlaceholderAssets]. The
/// portraits and the stage backdrop are real AI-generated PNGs under
/// `assets/personas/` and `assets/stage/`; the procedural layers (spotlight,
/// vote flourish, ambient) stay pure-Flutter so the pack only depends on two
/// image files.
///
/// This is a **labelled interim**, not S3's formal acceptance. It is selected by
/// [defaultTheatreAssets] only when `kUseAiArtInterim` is true; the default
/// remains [PlaceholderAssets] so nothing is silently swapped, and [TheatreScreen]
/// shows a visible "AI-art interim" marker whenever this pack is active.
class AiArtAssets extends TheatreAssets {
  const AiArtAssets();

  /// persona id (snake_case or kebab-case) -> bundled portrait path.
  static const Map<String, String> _portraits = <String, String>{
    'lin_shen': 'assets/personas/lin_shen.png',
    'lin-shen': 'assets/personas/lin_shen.png',
    'su_wan': 'assets/personas/su_wan.png',
    'su-wan': 'assets/personas/su_wan.png',
    'gu_yuan': 'assets/personas/gu_yuan.png',
    'gu-yuan': 'assets/personas/gu_yuan.png',
    'shen_mo': 'assets/personas/shen_mo.png',
    'shen-mo': 'assets/personas/shen_mo.png',
  };

  /// Stable pool used for ids the Hub actually emits (companion ids), which are
  /// not the four character keys above.
  static const List<String> _portraitPool = <String>[
    'assets/personas/lin_shen.png',
    'assets/personas/su_wan.png',
    'assets/personas/gu_yuan.png',
    'assets/personas/shen_mo.png',
  ];

  /// Resolve a persona id to a bundled portrait path.
  ///
  /// Exact character keys win. Otherwise (real matches emit opaque companion
  /// ids) we hash the id so each companion gets a stable, distinct portrait
  /// instead of a neutral placeholder. Returns '' only for a null id.
  static String _resolve(String? personaId) {
    if (personaId == null) return '';
    final String? exact = _portraits[personaId];
    if (exact != null) return exact;
    int hash = 0;
    for (final int code in personaId.codeUnits) {
      hash = (hash * 31 + code) & 0x7fffffff;
    }
    return _portraitPool[hash % _portraitPool.length];
  }

  @override
  Widget portrait(String? personaId, double size) {
    final double edge = size <= 0 ? 1.0 : size;
    final String path = _resolve(personaId);
    if (path.isEmpty) {
      // Neutral, clearly-labelled fallback for a null / "not recorded" id.
      // Never requests a missing asset (which would throw on load).
      return SizedBox(
        width: edge,
        height: edge,
        child: DecoratedBox(
          decoration: BoxDecoration(
            color: const Color(0xFF9E9E9E),
            borderRadius: BorderRadius.circular(edge * 0.08),
          ),
          child: Center(
            child: Text(
              personaId ?? 'not recorded',
              style: TextStyle(color: Colors.white70, fontSize: edge * 0.1),
              textAlign: TextAlign.center,
            ),
          ),
        ),
      );
    }
    return SizedBox(
      width: edge,
      height: edge,
      child: Image.asset(path, width: edge, height: edge, fit: BoxFit.cover),
    );
  }

  @override
  Widget backdrop() =>
      SizedBox.expand(child: Image.asset('assets/stage/backdrop.png', fit: BoxFit.cover));

  @override
  Widget spotlight(double intensity, bool reducedMotion) {
    final double i = intensity.clamp(0.0, 1.0);
    return SizedBox.expand(
      child: DecoratedBox(
        decoration: BoxDecoration(
          gradient: RadialGradient(
            center: Alignment.center,
            radius: 0.8,
            colors: <Color>[
              Colors.white.withValues(alpha: 0.25 * i),
              Colors.transparent,
            ],
          ),
        ),
      ),
    );
  }

  @override
  Widget voteFlourish(double progress) {
    final double p = progress.clamp(0.0, 1.0);
    return SizedBox.expand(
      child: Opacity(
        opacity: 0.15 * p,
        child: const DecoratedBox(
          decoration: BoxDecoration(
            gradient: LinearGradient(
              begin: Alignment.topCenter,
              end: Alignment.bottomCenter,
              colors: <Color>[Colors.transparent, Color(0x33ECEAF4)],
            ),
          ),
        ),
      ),
    );
  }

  @override
  Widget ambient(String? lightGrade) {
    const Map<String, Color> grades = <String, Color>{
      'neutral': Color(0x00000000),
      'warm': Color(0x1AE0B080),
      'cold': Color(0x1A80B0E0),
      'alert': Color(0x1AE08080),
    };
    final Color tint = grades[lightGrade ?? 'neutral'] ?? const Color(0x00000000);
    return SizedBox.expand(child: DecoratedBox(decoration: BoxDecoration(color: tint)));
  }
}
