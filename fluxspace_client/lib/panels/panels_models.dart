/// Typed, defensive wire models for the Flux Theatre observability panels.
///
/// These mirror the JSON emitted by the Hub's governance and archive HTTP
/// handlers (`hub/api/governance.go`, `hub/api/archives.go` and the `govern/*`
/// and `archive/*` structs). Field names are the wire names only inside
/// `fromJson`; Dart getters are camelCase.
///
/// Project invariant (ADR-0005, "do not invent metadata"): a field the Hub did
/// not record must stay UNKNOWN and render as [panelsNotRecorded] — never a
/// fabricated default such as `0`, `false`, `"rule_engine"`, or an invented
/// denial. [Field<T>] is the tri-state that makes this explicit: a recorded
/// value (even an empty one) is distinct from a missing/unknown one.
library;

/// The exact text rendered for any field the Hub did not record.
const String panelsNotRecorded = 'not recorded';

/// A field that may or may not have been recorded by the Hub.
///
/// `Field.unknown` means the key was absent or had an unusable type; the UI
/// renders [panelsNotRecorded]. `Field.recorded(v)` means the Hub sent the
/// field, so `v` is shown as-is. This is deliberately NOT a nullable value:
/// `null` would let a missing `monthly_tokens` silently become `0`.
class Field<T> {
  const Field.unknown()
      : _value = null,
        recorded = false;

  const Field.recorded(T value)
      : _value = value,
        recorded = true;

  final T? _value;
  final bool recorded;

  bool get isUnknown => !recorded;
  bool get isRecorded => recorded;
  T? get value => _value;

  /// Plain display text: the value when recorded, "not recorded" otherwise.
  String describe() => recorded ? '${_value ?? ''}' : panelsNotRecorded;

  /// Maps a recorded value to display text, otherwise "not recorded".
  String map(String Function(T value) render) {
    final T? v = _value;
    return recorded && v != null ? render(v) : panelsNotRecorded;
  }
}

/// The six replay viewing modes, in GDD table order
/// (`hub/archive/views.go: Modes()`).
class ArchiveModes {
  const ArchiveModes._();

  static const String full = 'full';
  static const String accelerated = 'accelerated';
  static const String singleCharacter = 'single_character';
  static const String omniscientComparison = 'omniscient_comparison';
  static const String keyMoments = 'key_moments';
  static const String sameSpecComparison = 'same_spec_comparison';

  static const List<String> all = <String>[
    full,
    accelerated,
    singleCharacter,
    omniscientComparison,
    keyMoments,
    sameSpecComparison,
  ];

  /// Short human label for the view selector.
  static String label(String mode) => switch (mode) {
        full => 'Full',
        accelerated => 'Accelerated 2x',
        singleCharacter => 'Single seat',
        omniscientComparison => 'Omniscient',
        keyMoments => 'Key moments',
        sameSpecComparison => 'Same-spec diff',
        _ => mode,
      };
}

/// The three permission tiers (`hub/govern/permission.go`). Default-deny means
/// an unconfigured companion has NO tier at all, which stays unknown here.
class PermissionTiers {
  const PermissionTiers._();

  static const String readOnly = 'read_only';
  static const String restrictedWrite = 'restricted_write';
  static const String highImpact = 'high_impact';

  static const List<String> all = <String>[readOnly, restrictedWrite, highImpact];
}

/// Audit outcomes (`hub/govern/audit.go`).
class AuditOutcomes {
  const AuditOutcomes._();

  static const String allowed = 'allowed';
  static const String denied = 'denied';
  static const String failed = 'failed';
}

/// Model-version consistency codes (`hub/govern/modelcheck.go`).
class ModelCodes {
  const ModelCodes._();

  static const String consistent = 'consistent';
  static const String mixedVersions = 'mixed_versions';
  static const String versionUnknown = 'version_unknown';
  static const String noParticipants = 'no_participants';
}

// ---------------------------------------------------------------------------
// Governance reads
// ---------------------------------------------------------------------------

/// `GET /hub/companions/{id}/budget`.
///
/// NOTE: this endpoint has NO `downgrade` key. The only route that emits
/// `downgrade: "rule_engine"` is the write `POST /hub/companions/{id}/budget/
/// usage` (a `BudgetDecision`), which also books token spend. This read-only
/// panel never calls a write, so [downgrade] stays unknown and renders
/// "not recorded" rather than being fabricated.
class BudgetState {
  const BudgetState({
    required this.companionId,
    required this.month,
    required this.monthlyTokens,
    required this.spentTokens,
    required this.remainingTokens,
    required this.configured,
    required this.downgrade,
  });

  factory BudgetState.fromJson(Map<String, dynamic> json) => BudgetState(
        companionId: _s(json['companion_id']),
        month: _s(json['month']),
        monthlyTokens: _intField(json, 'monthly_tokens'),
        spentTokens: _intField(json, 'spent_tokens'),
        remainingTokens: _intField(json, 'remaining_tokens'),
        configured: _boolField(json, 'configured'),
        downgrade: _strField(json, 'downgrade'),
      );

  final String companionId;
  final String month;
  final Field<int> monthlyTokens;
  final Field<int> spentTokens;
  final Field<int> remainingTokens;
  final Field<bool> configured;
  final Field<String> downgrade;
}

/// `GET /hub/companions/{id}/permissions`.
class PermissionState {
  const PermissionState({
    required this.companionId,
    required this.tier,
    required this.revoked,
    required this.updatedAt,
  });

  factory PermissionState.fromJson(Map<String, dynamic> json) => PermissionState(
        companionId: _s(json['companion_id']),
        tier: _strField(json, 'tier'),
        revoked: _boolField(json, 'revoked'),
        updatedAt: _intField(json, 'updated_at'),
      );

  final String companionId;
  final Field<String> tier;
  final Field<bool> revoked;
  final Field<int> updatedAt;

  bool get isHighImpact => tier.isRecorded && tier.value == PermissionTiers.highImpact;
}

/// One `AuditEntry` from `GET /hub/audit` (`hub/govern/audit.go`).
class AuditEntry {
  const AuditEntry({
    required this.seq,
    required this.ownerId,
    required this.companionId,
    required this.operation,
    required this.tier,
    required this.outcome,
    required this.detail,
    required this.at,
    required this.prevHash,
    required this.hash,
  });

  factory AuditEntry.fromJson(Map<String, dynamic> json) => AuditEntry(
        seq: _intField(json, 'seq'),
        ownerId: _strField(json, 'owner_id'),
        companionId: _s(json['companion_id']),
        operation: _s(json['operation']),
        tier: _strField(json, 'tier'),
        // A missing/unknown outcome is NOT a denial or an allowance.
        outcome: _strField(json, 'outcome'),
        detail: _strField(json, 'detail'),
        at: _intField(json, 'at'),
        prevHash: _strField(json, 'prev_hash'),
        hash: _strField(json, 'hash'),
      );

  final Field<int> seq;
  final Field<String> ownerId;
  final String companionId;
  final String operation;
  final Field<String> tier;
  final Field<String> outcome;
  final Field<String> detail;
  final Field<int> at;
  final Field<String> prevHash;
  final Field<String> hash;

  /// Only an explicitly recorded "denied" outcome counts. Unknown stays out.
  bool get isDenied => outcome.isRecorded && outcome.value == AuditOutcomes.denied;
  bool get isAllowed => outcome.isRecorded && outcome.value == AuditOutcomes.allowed;
}

/// `GET /hub/audit` response: `{entries: [...], count: N}`.
class AuditState {
  const AuditState({required this.entries, required this.count});

  factory AuditState.fromJson(Map<String, dynamic> json) => AuditState(
        entries: _listField<AuditEntry>(json, 'entries', AuditEntry.fromJson),
        count: _intField(json, 'count'),
      );

  final Field<List<AuditEntry>> entries;
  final Field<int> count;

  List<AuditEntry> get list => entries.value ?? const <AuditEntry>[];

  /// Denied operations are derived ONLY from recorded denials in the audit log;
  /// there is no separate "denials" endpoint and none is invented here.
  List<AuditEntry> get denied => <AuditEntry>[for (final AuditEntry e in list) if (e.isDenied) e];
}

/// `GET /hub/audit/verify`. A 409 `AUDIT_CHAIN_BROKEN` is a legitimate
/// "the chain is broken" answer, surfaced via [broken] rather than thrown.
class AuditVerification {
  const AuditVerification({
    required this.verified,
    required this.entries,
    required this.broken,
    required this.note,
  });

  factory AuditVerification.fromJson(Map<String, dynamic> json) => AuditVerification(
        verified: _boolField(json, 'verified'),
        entries: _intField(json, 'entries'),
        broken: false,
        note: '',
      );

  factory AuditVerification.broken(String message) => AuditVerification(
        verified: const Field<bool>.recorded(false),
        entries: const Field<int>.unknown(),
        broken: true,
        note: message,
      );

  final Field<bool> verified;
  final Field<int> entries;
  final bool broken;
  final String note;
}

/// One seat in `ModelConsistency.seats`.
class ModelUsage {
  const ModelUsage({
    required this.seatId,
    required this.companionId,
    required this.modelVersion,
    required this.official,
  });

  factory ModelUsage.fromJson(Map<String, dynamic> json) => ModelUsage(
        seatId: _s(json['seat_id']),
        companionId: _s(json['companion_id']),
        // An absent model_version is a real, reportable unknown.
        modelVersion: _strField(json, 'model_version'),
        official: _boolField(json, 'official'),
      );

  final String seatId;
  final String companionId;
  final Field<String> modelVersion;
  final Field<bool> official;
}

/// `GET /hub/archives/{id}/model-consistency`.
class ModelConsistencyState {
  const ModelConsistencyState({
    required this.consistent,
    required this.code,
    required this.versions,
    required this.seats,
    required this.reason,
  });

  factory ModelConsistencyState.fromJson(Map<String, dynamic> json) => ModelConsistencyState(
        consistent: _boolField(json, 'consistent'),
        code: _strField(json, 'code'),
        versions: _stringsField(json, 'versions'),
        seats: _listField<ModelUsage>(json, 'seats', ModelUsage.fromJson),
        reason: _s(json['reason']),
      );

  final Field<bool> consistent;
  final Field<String> code;
  final Field<List<String>> versions;
  final Field<List<ModelUsage>> seats;
  final String reason;
}

// ---------------------------------------------------------------------------
// Archive reads
// ---------------------------------------------------------------------------

/// One `Summary` from `GET /hub/archives` (`hub/archive/store.go`).
class ArchiveSummary {
  const ArchiveSummary({
    required this.id,
    required this.sessionId,
    required this.outcome,
    required this.rounds,
    required this.eventCount,
    required this.contentHash,
    required this.skeletonHash,
    required this.verified,
    required this.complete,
    required this.createdAt,
    required this.isPublic,
    required this.replayPolicy,
  });

  factory ArchiveSummary.fromJson(Map<String, dynamic> json) => ArchiveSummary(
        id: _s(json['id']),
        sessionId: _s(json['session_id']),
        outcome: _s(json['outcome']),
        rounds: _intField(json, 'rounds_played'),
        eventCount: _intField(json, 'event_count'),
        contentHash: _s(json['content_hash']),
        skeletonHash: _s(json['skeleton_hash']),
        verified: _boolField(json, 'verified'),
        complete: _boolField(json, 'complete'),
        createdAt: _intField(json, 'created_at'),
        isPublic: _boolField(json, 'public'),
        replayPolicy: _strField(json, 'replay_policy'),
      );

  final String id;
  final String sessionId;
  final String outcome;
  final Field<int> rounds;
  final Field<int> eventCount;
  final String contentHash;
  final String skeletonHash;
  final Field<bool> verified;
  final Field<bool> complete;
  final Field<int> createdAt;
  final Field<bool> isPublic;
  final Field<String> replayPolicy;
}

/// The `Integrity` block (`hub/archive/archive.go`). [missingFields] names
/// fields the Hub could not capture; each is rendered as "not recorded".
class ArchiveIntegrity {
  const ArchiveIntegrity({
    required this.eventCount,
    required this.contentHash,
    required this.skeletonHash,
    required this.verified,
    required this.complete,
    required this.missingFields,
  });

  factory ArchiveIntegrity.fromJson(Map<String, dynamic> json) => ArchiveIntegrity(
        eventCount: _intField(json, 'event_count'),
        contentHash: _s(json['content_hash']),
        skeletonHash: _s(json['skeleton_hash']),
        verified: _boolField(json, 'verified'),
        complete: _boolField(json, 'complete'),
        missingFields: _stringsField(json, 'missing_fields'),
      );

  final Field<int> eventCount;
  final String contentHash;
  final String skeletonHash;
  final Field<bool> verified;
  final Field<bool> complete;
  final Field<List<String>> missingFields;
}

/// `GET /hub/archives/{id}/verify`: `{verified, integrity}`.
class ArchiveIntegrityState {
  const ArchiveIntegrityState({required this.verified, required this.integrity});

  factory ArchiveIntegrityState.fromJson(Map<String, dynamic> json) => ArchiveIntegrityState(
        verified: _boolField(json, 'verified'),
        integrity: ArchiveIntegrity.fromJson(_m(json['integrity'])),
      );

  final Field<bool> verified;
  final ArchiveIntegrity integrity;
}

/// `PolicyDecision` (`hub/archive/consent.go`): the evaluated consent ledger
/// for one archive. Individual per-owner Grant rows have NO read endpoint and
/// are deliberately not fabricated here.
class PolicyDecision {
  const PolicyDecision({
    required this.policy,
    required this.participants,
    required this.granted,
    required this.missing,
    required this.isPublic,
    required this.reason,
  });

  factory PolicyDecision.fromJson(Map<String, dynamic> json) => PolicyDecision(
        policy: _strField(json, 'policy'),
        participants: _stringsField(json, 'participants'),
        granted: _stringsField(json, 'granted'),
        missing: _stringsField(json, 'missing'),
        isPublic: _boolField(json, 'public'),
        reason: _s(json['reason']),
      );

  final Field<String> policy;
  final Field<List<String>> participants;
  final Field<List<String>> granted;
  final Field<List<String>> missing;
  final Field<bool> isPublic;
  final String reason;
}

/// One `SeatConfig` from the archive config summary.
class SeatConfig {
  const SeatConfig({
    required this.seat,
    required this.seatId,
    required this.companionId,
    required this.displayName,
    required this.official,
    required this.personaId,
    required this.modelVersion,
    required this.personaSource,
  });

  factory SeatConfig.fromJson(Map<String, dynamic> json) => SeatConfig(
        seat: _intField(json, 'seat'),
        seatId: _s(json['seat_id']),
        companionId: _s(json['companion_id']),
        displayName: _s(json['display_name']),
        official: _boolField(json, 'official'),
        personaId: _strField(json, 'persona_id'),
        modelVersion: _strField(json, 'model_version'),
        personaSource: _s(json['persona_source']),
      );

  final Field<int> seat;
  final String seatId;
  final String companionId;
  final String displayName;
  final Field<bool> official;
  final Field<String> personaId;
  final Field<String> modelVersion;
  final String personaSource;
}

/// `config` block of `GET /hub/archives/{id}`.
class ArchiveConfig {
  const ArchiveConfig({
    required this.totalRounds,
    required this.seatCount,
    required this.seats,
    required this.replayConsent,
  });

  factory ArchiveConfig.fromJson(Map<String, dynamic> json) => ArchiveConfig(
        totalRounds: _intField(json, 'total_rounds'),
        seatCount: _intField(json, 'seat_count'),
        seats: _listField<SeatConfig>(json, 'seats', SeatConfig.fromJson),
        replayConsent: _strField(json, 'replay_consent'),
      );

  final Field<int> totalRounds;
  final Field<int> seatCount;
  final Field<List<SeatConfig>> seats;
  final Field<String> replayConsent;

  String? get firstSeatId {
    final List<SeatConfig> list = seats.value ?? const <SeatConfig>[];
    for (final SeatConfig s in list) {
      if (s.seatId.isNotEmpty) return s.seatId;
    }
    return null;
  }
}

/// `GET /hub/archives/{id}` (`archiveRecord` in `hub/api/archives.go`).
class ArchiveDetail {
  const ArchiveDetail({
    required this.summary,
    required this.integrity,
    required this.published,
    required this.publishedAt,
    required this.consent,
    required this.config,
  });

  factory ArchiveDetail.fromJson(Map<String, dynamic> json) {
    final Object? summaryJson = json['summary'];
    return ArchiveDetail(
      summary: summaryJson is Map ? ArchiveSummary.fromJson(_m(summaryJson)) : null,
      integrity: ArchiveIntegrity.fromJson(_m(json['integrity'])),
      published: _boolField(json, 'published'),
      publishedAt: _intField(json, 'published_at'),
      consent: PolicyDecision.fromJson(_m(json['consent'])),
      config: ArchiveConfig.fromJson(_m(json['config'])),
    );
  }

  final ArchiveSummary? summary;
  final ArchiveIntegrity integrity;
  final Field<bool> published;
  final Field<int> publishedAt;
  final PolicyDecision consent;
  final ArchiveConfig config;
}

// ---------------------------------------------------------------------------
// Views / replay / divergence
// ---------------------------------------------------------------------------

/// A `TimelineEvent` projection.
class ReplayEvent {
  const ReplayEvent({
    required this.seq,
    required this.type,
    required this.round,
    required this.visibility,
    required this.offsetMs,
  });

  factory ReplayEvent.fromJson(Map<String, dynamic> json) => ReplayEvent(
        seq: _intField(json, 'seq'),
        type: _s(json['type']),
        round: _intField(json, 'round'),
        visibility: _s(json['visibility']),
        offsetMs: _intField(json, 'offset_ms'),
      );

  final Field<int> seq;
  final String type;
  final Field<int> round;
  final String visibility;
  final Field<int> offsetMs;
}

/// A `RoundComparison` (omniscient mode).
class ReplayRound {
  const ReplayRound({
    required this.round,
    required this.agreement,
    required this.synergy,
    required this.decisionCount,
  });

  factory ReplayRound.fromJson(Map<String, dynamic> json) {
    final Field<List<Map<String, dynamic>>> decisions =
        _listField<Map<String, dynamic>>(json, 'decisions', _m);
    return ReplayRound(
      round: _intField(json, 'round'),
      agreement: _doubleField(json, 'agreement'),
      synergy: _doubleField(json, 'synergy'),
      decisionCount: decisions.isRecorded
          ? Field<int>.recorded(decisions.value!.length)
          : const Field<int>.unknown(),
    );
  }

  final Field<int> round;
  final Field<double> agreement;
  final Field<double> synergy;
  final Field<int> decisionCount;
}

/// A `Moment` (key_moments mode).
class ReplayMoment {
  const ReplayMoment({
    required this.eventId,
    required this.round,
    required this.kind,
    required this.reason,
  });

  factory ReplayMoment.fromJson(Map<String, dynamic> json) => ReplayMoment(
        eventId: _intField(json, 'event_id'),
        round: _intField(json, 'round'),
        kind: _s(json['kind']),
        reason: _s(json['reason']),
      );

  final Field<int> eventId;
  final Field<int> round;
  final String kind;
  final String reason;
}

/// A `SeatDiff` inside a divergence round.
class SeatDiff {
  const SeatDiff({
    required this.seatId,
    required this.originalAction,
    required this.repriseAction,
  });

  factory SeatDiff.fromJson(Map<String, dynamic> json) => SeatDiff(
        seatId: _s(json['seat_id']),
        originalAction: _s(json['original_action']),
        repriseAction: _s(json['reprise_action']),
      );

  final String seatId;
  final String originalAction;
  final String repriseAction;
}

/// A `RoundDiff` inside a divergence comparison.
class RoundDiff {
  const RoundDiff({required this.round, required this.differences});

  factory RoundDiff.fromJson(Map<String, dynamic> json) => RoundDiff(
        round: _intField(json, 'round'),
        differences: _listField<SeatDiff>(json, 'differences', SeatDiff.fromJson),
      );

  final Field<int> round;
  final Field<List<SeatDiff>> differences;
}

/// `RepriseComparison` (`hub/archive/reprise.go`). `divergence_point == 0`
/// means the two runs never diverged; a MISSING divergence_point is unknown.
class RepriseComparison {
  const RepriseComparison({
    required this.originalId,
    required this.repriseId,
    required this.skeletonHash,
    required this.identicalConfig,
    required this.divergencePoint,
    required this.divergenceSeat,
    required this.rounds,
    required this.originalOutcome,
    required this.repriseOutcome,
    required this.outcomeChanged,
  });

  factory RepriseComparison.fromJson(Map<String, dynamic> json) => RepriseComparison(
        originalId: _s(json['original_id']),
        repriseId: _s(json['reprise_id']),
        skeletonHash: _s(json['skeleton_hash']),
        identicalConfig: _boolField(json, 'identical_config'),
        divergencePoint: _intField(json, 'divergence_point'),
        divergenceSeat: _strField(json, 'divergence_seat'),
        rounds: _listField<RoundDiff>(json, 'rounds', RoundDiff.fromJson),
        originalOutcome: _s(json['original_outcome']),
        repriseOutcome: _s(json['reprise_outcome']),
        outcomeChanged: _boolField(json, 'outcome_changed'),
      );

  final String originalId;
  final String repriseId;
  final String skeletonHash;
  final Field<bool> identicalConfig;
  final Field<int> divergencePoint;
  final Field<String> divergenceSeat;
  final Field<List<RoundDiff>> rounds;
  final String originalOutcome;
  final String repriseOutcome;
  final Field<bool> outcomeChanged;

  /// True when the Hub recorded a positive divergence point.
  bool get hasDivergence => divergencePoint.isRecorded && (divergencePoint.value ?? 0) > 0;
}

/// A `View` response from `GET /hub/archives/{id}/views/{mode}`.
class ArchiveView {
  const ArchiveView({
    required this.mode,
    required this.archiveId,
    required this.sessionId,
    required this.speed,
    required this.seatId,
    required this.durationMs,
    required this.eventCount,
    required this.events,
    required this.rounds,
    required this.moments,
    required this.comparison,
  });

  factory ArchiveView.fromJson(Map<String, dynamic> json) {
    final Object? comparisonJson = json['comparison'];
    return ArchiveView(
      mode: _s(json['mode']),
      archiveId: _s(json['archive_id']),
      sessionId: _s(json['session_id']),
      speed: _intField(json, 'speed'),
      seatId: _strField(json, 'seat_id'),
      durationMs: _intField(json, 'duration_ms'),
      eventCount: _intField(json, 'event_count'),
      events: _listField<ReplayEvent>(json, 'events', ReplayEvent.fromJson),
      rounds: _listField<ReplayRound>(json, 'rounds', ReplayRound.fromJson),
      moments: _listField<ReplayMoment>(json, 'moments', ReplayMoment.fromJson),
      comparison:
          comparisonJson is Map ? RepriseComparison.fromJson(_m(comparisonJson)) : null,
    );
  }

  final String mode;
  final String archiveId;
  final String sessionId;
  final Field<int> speed;
  final Field<String> seatId;
  final Field<int> durationMs;
  final Field<int> eventCount;
  final Field<List<ReplayEvent>> events;
  final Field<List<ReplayRound>> rounds;
  final Field<List<ReplayMoment>> moments;
  final RepriseComparison? comparison;
}

/// A same-spec `Reprise` (`hub/archive/reprise.go`).
class Reprise {
  const Reprise({
    required this.id,
    required this.archiveId,
    required this.type,
    required this.status,
    required this.createdAt,
    required this.newSessionId,
    required this.consent,
    required this.comparison,
  });

  factory Reprise.fromJson(Map<String, dynamic> json) {
    final Object? consentJson = json['consent'];
    final Object? comparisonJson = json['comparison'];
    return Reprise(
      id: _s(json['id']),
      archiveId: _s(json['archive_id']),
      type: _s(json['type']),
      status: _s(json['status']),
      createdAt: _intField(json, 'created_at'),
      newSessionId: _s(json['new_session_id']),
      consent: consentJson is Map ? PolicyDecision.fromJson(_m(consentJson)) : null,
      comparison:
          comparisonJson is Map ? RepriseComparison.fromJson(_m(comparisonJson)) : null,
    );
  }

  final String id;
  final String archiveId;
  final String type;
  final String status;
  final Field<int> createdAt;
  final String newSessionId;
  final PolicyDecision? consent;
  final RepriseComparison? comparison;

  bool get isCompleted => status == 'completed';
}

// ---------------------------------------------------------------------------
// Aggregate load results (see panels_api.dart)
// ---------------------------------------------------------------------------

/// One endpoint's contribution to an aggregate load: either a typed value or
/// the error the endpoint returned. A degraded governance surface answers 503
/// on every stateful route, so the panel must see a FAILED section rather than
/// silently reading an empty list.
class Section<T> {
  const Section.value(T value)
      : _value = value,
        _error = null;

  const Section.failed(PanelsException error)
      : _value = null,
        _error = error;

  final T? _value;
  final PanelsException? _error;

  T? get value => _value;
  PanelsException? get error => _error;
  bool get isOk => _error == null;
  bool get isDegraded => _error?.isDegraded ?? false;
}

/// Bundled result of all governance reads for one companion.
class GovernanceData {
  const GovernanceData({
    required this.budget,
    required this.permissions,
    required this.audit,
    required this.verification,
    required this.modelConsistency,
  });

  final Section<BudgetState> budget;
  final Section<PermissionState> permissions;
  final Section<AuditState> audit;
  final Section<AuditVerification> verification;

  /// Null when no archive id was supplied (model consistency is archive-scoped
  /// and there is no companion-scoped endpoint for it).
  final Section<ModelConsistencyState>? modelConsistency;

  bool get isDegraded => <Section<dynamic>>[
        budget,
        permissions,
        audit,
        verification,
        if (modelConsistency != null) modelConsistency!,
      ].any((Section<dynamic> s) => s.isDegraded);
}

/// Bundled reads for one selected archive.
class ArchiveBundle {
  const ArchiveBundle({
    required this.detail,
    required this.integrity,
    required this.view,
    required this.reprises,
  });

  final Section<ArchiveDetail> detail;
  final Section<ArchiveIntegrityState> integrity;
  final Section<ArchiveView> view;
  final Section<List<Reprise>> reprises;

  bool get isDegraded => <Section<dynamic>>[detail, integrity, view, reprises]
      .any((Section<dynamic> s) => s.isDegraded);
}

// ---------------------------------------------------------------------------
// Error type
// ---------------------------------------------------------------------------

/// A typed failure from a panels endpoint.
///
/// The wire error envelope from the Hub is `{"error":{"code":..,"message":..}}`
/// (`hub/api/sessions.go: writeDomainError`). [code] carries the stable domain
/// code (e.g. `GOVERNANCE_DEGRADED`) and is what the UI branches on, never
/// English prose.
class PanelsException implements Exception {
  const PanelsException({
    required this.status,
    required this.code,
    required this.message,
  });

  /// HTTP status, or 0 when the request never reached the Hub.
  final int status;
  final String code;
  final String message;

  bool get isTransport => status == 0;

  /// 503 covers GOVERNANCE_DEGRADED, GOVERNANCE_UNCONFIGURED and
  /// ARCHIVE_STORE_UNCONFIGURED — every "stopped answering" state.
  bool get isDegraded => status == 503;

  bool get isGovernanceDegraded => code == 'GOVERNANCE_DEGRADED';

  @override
  String toString() => status == 0
      ? 'PanelsException($code: $message)'
      : 'PanelsException($status $code: $message)';
}

// ---------------------------------------------------------------------------
// Display helpers
// ---------------------------------------------------------------------------

/// Formats an epoch-millisecond timestamp for display without extra deps.
String formatEpochMs(int ms) {
  try {
    return DateTime.fromMillisecondsSinceEpoch(ms, isUtc: true).toIso8601String();
  } catch (_) {
    return '$ms';
  }
}

/// Truncates long hashes/ids so a Row never overflows.
String shortHash(String value, [int keep = 14]) =>
    value.length <= keep ? value : '${value.substring(0, keep)}…';

/// Divergence display: a recorded 0 is a real "never diverged", not an
/// unknown; an absent value stays "not recorded".
String divergenceText(RepriseComparison comparison) {
  if (comparison.divergencePoint.isUnknown) return panelsNotRecorded;
  final int point = comparison.divergencePoint.value!;
  return point == 0 ? '0 (never diverged)' : '$point';
}

// ---------------------------------------------------------------------------
// Defensive parsing primitives (all private)
// ---------------------------------------------------------------------------

String _s(Object? v) => v is String ? v : (v == null ? '' : '$v');

Map<String, dynamic> _m(Object? v) {
  if (v is Map<String, dynamic>) return v;
  if (v is Map) return Map<String, dynamic>.from(v);
  return <String, dynamic>{};
}

Field<String> _strField(Map<String, dynamic> j, String key) {
  final Object? v = j[key];
  if (v is String && v.isNotEmpty) return Field<String>.recorded(v);
  return Field<String>.unknown();
}

Field<int> _intField(Map<String, dynamic> j, String key) {
  final Object? v = j[key];
  if (v is num) return Field<int>.recorded(v.toInt());
  if (v is String) {
    final int? n = int.tryParse(v);
    if (n != null) return Field<int>.recorded(n);
  }
  return Field<int>.unknown();
}

Field<double> _doubleField(Map<String, dynamic> j, String key) {
  final Object? v = j[key];
  if (v is num) return Field<double>.recorded(v.toDouble());
  if (v is String) {
    final double? n = double.tryParse(v);
    if (n != null) return Field<double>.recorded(n);
  }
  return Field<double>.unknown();
}

Field<bool> _boolField(Map<String, dynamic> j, String key) {
  final Object? v = j[key];
  if (v is bool) return Field<bool>.recorded(v);
  return Field<bool>.unknown();
}

Field<List<T>> _listField<T>(
  Map<String, dynamic> j,
  String key,
  T Function(Map<String, dynamic>) from,
) {
  if (!j.containsKey(key)) return Field<List<T>>.unknown();
  final Object? v = j[key];
  // Go marshals a nil slice (e.g. missing_fields when complete) as `null`,
  // which means "none", not "unknown".
  if (v == null) return Field<List<T>>.recorded(<T>[]);
  if (v is List) {
    return Field<List<T>>.recorded(<T>[
      for (final Object? e in v)
        if (e is Map<String, dynamic>)
          from(e)
        else if (e is Map)
          from(Map<String, dynamic>.from(e)),
    ]);
  }
  return Field<List<T>>.unknown();
}

Field<List<String>> _stringsField(Map<String, dynamic> j, String key) {
  if (!j.containsKey(key)) return Field<List<String>>.unknown();
  final Object? v = j[key];
  if (v == null) return Field<List<String>>.recorded(<String>[]);
  if (v is List) {
    return Field<List<String>>.recorded(<String>[
      for (final Object? e in v)
        if (e is String) e,
    ]);
  }
  return Field<List<String>>.unknown();
}
