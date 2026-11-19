import 'placeholder_assets.dart' show PlaceholderAssets, TheatreAssets;
import 'ai_art_assets.dart' show AiArtAssets;

/// Opt-in switch for the AI-generated art pack (ADR-0012).
///
/// false keeps the pure-Flutter [PlaceholderAssets] as the default so nothing is
/// silently swapped. true makes the theatre render layer use [AiArtAssets] (AI
/// portraits + 2.5D backdrop). Flip only after the interim art is reviewed; the
/// UI shows a visible "AI-art interim" marker whenever [AiArtAssets] is active.
const bool kUseAiArtInterim = true;

/// The art pack the theatre render layer should use, honouring [kUseAiArtInterim].
TheatreAssets get defaultTheatreAssets =>
    kUseAiArtInterim ? const AiArtAssets() : const PlaceholderAssets();
