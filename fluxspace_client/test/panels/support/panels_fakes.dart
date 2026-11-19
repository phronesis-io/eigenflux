/// Network-free fixtures for the panels tests.
///
/// `http_mock_adapter` is NOT a dependency of this project, so every test uses
/// the injectable [PanelsFetcher] seam instead. [ScriptedFetcher] records the
/// paths/query it was asked for and can simulate a 503 on named paths.
library;

import 'package:fluxspace_client/panels/panels_api.dart';
import 'package:fluxspace_client/panels/panels_models.dart';

/// A [PanelsFetcher] backed by a canned response table.
class ScriptedFetcher {
  ScriptedFetcher({
    Map<String, dynamic> Function(String path, Map<String, dynamic>? query)? respond,
    Set<String> degradedContains = const <String>{},
    String degradedCode = 'GOVERNANCE_DEGRADED',
  })  : _respond = respond ?? cannedPanelsJson,
        _degradedContains = degradedContains,
        _degradedCode = degradedCode;

  final Map<String, dynamic> Function(String path, Map<String, dynamic>? query) _respond;
  final Set<String> _degradedContains;
  final String _degradedCode;

  final List<String> paths = <String>[];
  final List<Map<String, dynamic>?> queries = <Map<String, dynamic>?>[];

  Future<Map<String, dynamic>> call(
    String path, {
    Map<String, dynamic>? query,
  }) async {
    paths.add(path);
    queries.add(query);
    for (final String needle in _degradedContains) {
      if (path.contains(needle)) {
        throw PanelsException(
          status: 503,
          code: _degradedCode,
          message: '$_degradedCode: the Hub stopped answering authority queries',
        );
      }
    }
    return _respond(path, query);
  }

  PanelsApi api() => PanelsApi(fetcher: call);
}

// --- canonical wire fixtures ------------------------------------------------

final Map<String, dynamic> permissionsJson = <String, dynamic>{
  'companion_id': 'comp_alpha',
  'tier': 'restricted_write',
  'revoked': false,
  'updated_at': 1699999999000,
};

final Map<String, dynamic> budgetJson = <String, dynamic>{
  'companion_id': 'comp_alpha',
  'month': '2026-11',
  'monthly_tokens': 200000,
  'spent_tokens': 1200,
  'remaining_tokens': 198800,
  'configured': true,
  // NOTE: deliberately NO 'downgrade' key — GET /budget has none.
};

final Map<String, dynamic> auditJson = <String, dynamic>{
  'entries': <dynamic>[
    <String, dynamic>{
      'seq': 1,
      'owner_id': 'owner_alpha',
      'companion_id': 'comp_alpha',
      'operation': 'model_call',
      'tier': 'budget',
      'outcome': 'allowed',
      'detail': 'of 200000 tokens remain',
      'at': 1699999999000,
      'prev_hash': '',
      'hash': 'sha256:1111111111111111',
    },
    <String, dynamic>{
      'seq': 2,
      'companion_id': 'comp_alpha',
      'operation': 'export_contacts',
      'tier': 'high_impact',
      'outcome': 'denied',
      'at': 1699999999100,
      'prev_hash': 'sha256:1111111111111111',
      'hash': 'sha256:2222222222222222',
      // 'detail' intentionally omitted (omitempty).
    },
  ],
  'count': 2,
};

final Map<String, dynamic> auditVerifyJson = <String, dynamic>{
  'verified': true,
  'entries': 2,
};

final Map<String, dynamic> modelJson = <String, dynamic>{
  'consistent': true,
  'code': 'consistent',
  'versions': <dynamic>['flux-1.4'],
  'seats': <dynamic>[
    <String, dynamic>{
      'seat_id': 'seat_0',
      'companion_id': 'comp_alpha',
      'model_version': 'flux-1.4',
      'official': false,
    },
    <String, dynamic>{
      'seat_id': 'seat_1',
      'companion_id': 'comp_beta',
      // no model_version -> "not recorded"
      'official': true,
    },
  ],
  'reason': 'all 1 seat(s) declare flux-1.4',
};

final Map<String, dynamic> summaryOne = <String, dynamic>{
  'id': 'replay_ab12',
  'session_id': 'sess_1',
  'outcome': 'accord',
  'rounds_played': 6,
  'event_count': 42,
  'content_hash': 'sha256:aaaaaaaaaaaaaaaa',
  'skeleton_hash': 'sha256:bbbbbbbbbbbbbbbb',
  'verified': true,
  'complete': false,
  'created_at': 1699999999000,
  'public': true,
  'replay_policy': 'all',
};

final Map<String, dynamic> summaryTwo = <String, dynamic>{
  'id': 'replay_cd34',
  'session_id': 'sess_2',
  'outcome': 'betrayal',
  'rounds_played': 6,
  'event_count': 40,
  'content_hash': 'sha256:cccccccccccccccc',
  'skeleton_hash': 'sha256:bbbbbbbbbbbbbbbb',
  'verified': true,
  'complete': true,
  'created_at': 1699999999500,
  'public': false,
  'replay_policy': 'majority',
};

final Map<String, dynamic> archivesJson = <String, dynamic>{
  'archives': <dynamic>[summaryOne, summaryTwo],
};

final Map<String, dynamic> detailJson = <String, dynamic>{
  'summary': summaryOne,
  'integrity': <String, dynamic>{
    'event_count': 42,
    'content_hash': 'sha256:aaaaaaaaaaaaaaaa',
    'skeleton_hash': 'sha256:bbbbbbbbbbbbbbbb',
    'verified': true,
    'complete': false,
    'missing_fields': <dynamic>[
      'config.seats[0].model_version',
      'config.seats[0].persona_id',
    ],
  },
  'published': true,
  'published_at': 1699999999999,
  'consent': <String, dynamic>{
    'policy': 'all',
    'participants': <dynamic>['comp_alpha', 'comp_beta'],
    'granted': <dynamic>['comp_alpha'],
    'missing': <dynamic>['comp_beta'],
    'public': false,
    'reason': '1 participant(s) have not consented',
  },
  'config': <String, dynamic>{
    'total_rounds': 6,
    'seat_count': 2,
    'seats': <dynamic>[
      <String, dynamic>{
        'seat': 0,
        'seat_id': 'seat_0',
        'companion_id': 'comp_alpha',
        'display_name': 'Lin',
        'official': false,
        'persona_snapshot': <String, dynamic>{},
        'persona_source': 'hub_seat_claim',
      },
      <String, dynamic>{
        'seat': 1,
        'seat_id': 'seat_1',
        'companion_id': 'comp_beta',
        'display_name': 'Su',
        'official': false,
        'model_version': 'flux-1.4',
        'persona_snapshot': <String, dynamic>{},
        'persona_source': 'hub_seat_claim',
      },
    ],
    'replay_consent': 'all',
  },
};

final Map<String, dynamic> archiveVerifyJson = <String, dynamic>{
  'verified': true,
  'integrity': detailJson['integrity'],
};

final Map<String, dynamic> repriseComparisonJson = <String, dynamic>{
  'original_id': 'replay_ab12',
  'reprise_id': 'reprise_1',
  'skeleton_hash': 'sha256:bbbbbbbbbbbbbbbb',
  'identical_config': true,
  'divergence_point': 3,
  'divergence_seat': 'seat_1',
  'rounds': <dynamic>[
    <String, dynamic>{
      'round': 3,
      'differences': <dynamic>[
        <String, dynamic>{
          'seat_id': 'seat_1',
          'original_action': 'cooperate',
          'reprise_action': 'defect',
        },
      ],
    },
  ],
  'original_outcome': 'accord',
  'reprise_outcome': 'betrayal',
  'outcome_changed': true,
};

final Map<String, dynamic> reprisesJson = <String, dynamic>{
  'reprises': <dynamic>[
    <String, dynamic>{
      'id': 'reprise_1',
      'archive_id': 'replay_ab12',
      'type': 'same_spec',
      'status': 'completed',
      'created_at': 1699999999900,
      'new_session_id': 'sess_2',
      'consent': detailJson['consent'],
      'comparison': repriseComparisonJson,
    },
  ],
};

Map<String, dynamic> _fullView() => <String, dynamic>{
      'mode': ArchiveModes.full,
      'archive_id': 'replay_ab12',
      'session_id': 'sess_1',
      'event_count': 2,
      'duration_ms': 9000,
      'events': <dynamic>[
        <String, dynamic>{
          'seq': 1,
          'type': 'match_start',
          'round': 0,
          'phase': 'setup',
          'visibility': 'public',
          'ts': 1000,
          'offset_ms': 0,
          'payload': <String, dynamic>{},
        },
        <String, dynamic>{
          'seq': 2,
          'type': 'decision',
          'round': 1,
          'phase': 'action',
          'visibility': 'public',
          'ts': 4000,
          'offset_ms': 3000,
          'payload': <String, dynamic>{},
        },
      ],
    };

/// Returns a plausible [View] JSON for whichever mode appears in [path].
Map<String, dynamic> viewJsonForPath(String path) {
  final String mode = path.split('/views/').last;
  switch (mode) {
    case ArchiveModes.sameSpecComparison:
      return <String, dynamic>{
        'mode': mode,
        'archive_id': 'replay_ab12',
        'session_id': 'sess_1',
        'comparison': repriseComparisonJson,
      };
    case ArchiveModes.omniscientComparison:
      return <String, dynamic>{
        'mode': mode,
        'archive_id': 'replay_ab12',
        'session_id': 'sess_1',
        'event_count': 42,
        'rounds': <dynamic>[
          <String, dynamic>{
            'round': 1,
            'agreement': 0.75,
            'synergy': 0.5,
            'decisions': <dynamic>[
              <String, dynamic>{'seat_id': 'seat_0', 'action_id': 'a1', 'official': false},
            ],
          },
        ],
      };
    case ArchiveModes.keyMoments:
      return <String, dynamic>{
        'mode': mode,
        'archive_id': 'replay_ab12',
        'session_id': 'sess_1',
        'event_count': 42,
        'moments': <dynamic>[
          <String, dynamic>{
            'event_id': 9,
            'round': 3,
            'kind': 'branch',
            'magnitude': 12.0,
            'reason': 'branch b1 fired',
          },
        ],
      };
    case ArchiveModes.accelerated:
      return <String, dynamic>{..._fullView(), 'mode': mode, 'speed': 2};
    case ArchiveModes.singleCharacter:
      return <String, dynamic>{..._fullView(), 'mode': mode, 'seat_id': 'seat_0'};
    default:
      return _fullView();
  }
}

/// The default canned response table for every read endpoint this slice calls.
Map<String, dynamic> cannedPanelsJson(
  String path,
  Map<String, dynamic>? query,
) {
  if (path.endsWith('/permissions')) return permissionsJson;
  if (path.endsWith('/budget')) return budgetJson;
  if (path == PanelsApi.pathAudit) return auditJson;
  if (path == PanelsApi.pathAuditVerify) return auditVerifyJson;
  if (path == PanelsApi.pathArchives) return archivesJson;
  if (path.contains('/views/')) return viewJsonForPath(path);
  if (path.endsWith('/model-consistency')) return modelJson;
  if (path.endsWith('/verify')) return archiveVerifyJson;
  if (path.endsWith('/reprises')) return reprisesJson;
  // GET /hub/archives/{id} and GET /hub/reprises/{id}.
  return detailJson;
}
