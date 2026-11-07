/// Local SQLite schema (story 001 AC3, story 007, story 008, story 009).
///
/// Memories and relationships are local-only and never synced to the Hub.
/// The database is encrypted at rest with a key held in the platform keychain
/// (Windows Credential Manager for the first supported platform).
///
/// These statements are the authoritative schema contract. The drift
/// generated code is added after `flutter pub get` resolves dependencies;
/// keeping the DDL here means the schema is testable without generated code.
class LocalDatabaseSchema {
  const LocalDatabaseSchema._();

  static const int version = 1;

  static const List<String> tableNames = <String>[
    'companions',
    'memories',
    'relationships',
    'match_history',
    'settings',
    'threads',
  ];

  static const Map<String, String> createStatements = <String, String>{
    'companions': '''
CREATE TABLE companions (
  id TEXT PRIMARY KEY,
  persona_id TEXT NOT NULL,
  display_name TEXT NOT NULL,
  model TEXT NOT NULL,
  traits_json TEXT NOT NULL,
  active INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL
)''',
    'memories': '''
CREATE TABLE memories (
  id TEXT PRIMARY KEY,
  companion_id TEXT NOT NULL REFERENCES companions(id),
  kind TEXT NOT NULL,
  content TEXT NOT NULL,
  source_kind TEXT NOT NULL,
  source_ref_id TEXT NOT NULL,
  source_round INTEGER NOT NULL,
  strength REAL NOT NULL,
  created_at INTEGER NOT NULL
)''',
    'relationships': '''
CREATE TABLE relationships (
  id TEXT PRIMARY KEY,
  owner_companion_id TEXT NOT NULL REFERENCES companions(id),
  target_companion_id TEXT NOT NULL,
  closeness REAL NOT NULL DEFAULT 0,
  trust REAL NOT NULL DEFAULT 0,
  rapport REAL NOT NULL DEFAULT 0,
  respect REAL NOT NULL DEFAULT 0,
  resentment REAL NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'stranger',
  updated_at INTEGER NOT NULL,
  UNIQUE(owner_companion_id, target_companion_id)
)''',
    'match_history': '''
CREATE TABLE match_history (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  structural_hash TEXT NOT NULL,
  content_hash TEXT NOT NULL,
  outcome TEXT NOT NULL,
  started_at INTEGER NOT NULL,
  ended_at INTEGER NOT NULL,
  archive_path TEXT
)''',
    'settings': '''
CREATE TABLE settings (
  key TEXT PRIMARY KEY,
  value_json TEXT NOT NULL
)''',
    'threads': '''
CREATE TABLE threads (
  id TEXT PRIMARY KEY,
  kind TEXT NOT NULL,
  subject_id TEXT NOT NULL,
  state TEXT NOT NULL DEFAULT 'fresh',
  created_at INTEGER NOT NULL,
  last_activity_at INTEGER NOT NULL,
  payload_json TEXT NOT NULL
)''',
  };

  /// True when every declared table has a CREATE statement and vice versa.
  static bool isComplete() {
    if (createStatements.length != tableNames.length) return false;
    for (final String table in tableNames) {
      final String? ddl = createStatements[table];
      if (ddl == null || !ddl.contains('CREATE TABLE $table')) return false;
    }
    return true;
  }
}
