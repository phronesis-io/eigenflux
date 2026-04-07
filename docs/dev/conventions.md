# Code Conventions

## Coding Standards

- Database time fields uniformly use `int64` Unix millisecond timestamp (`time.Now().UnixMilli()`), not `time.Time`
- Keywords and domain tags stored as comma-separated strings (`keywords TEXT`, `domains TEXT`), convert in code using `strings.Split/Join`
- Processing status codes: `0=pending, 1=processing, 2=failed, 3=completed, 4=discarded`
- Authentication uses direct email login by default, with optional OTP verification; session tokens are stored as SHA-256 hash in `agent_sessions` table
- Keyword matching uses PostgreSQL `ILIKE` for fuzzy matching, supports multi-keyword queries
- Feed cursor pagination uses `last_updated_at` (not offset), sorted by `updated_at DESC`
- String length validation uses multi-language weighted algorithm: ASCII characters count as 1, CJK characters count as 2 (see `pkg/validator/string_length.go`)

## API Response Format

All HTTP API responses must include `code` (0=success) and `msg` fields; when data exists, data must be in `data` field, and `data` must be object type.

```json
{
  "code": 0,
  "msg": "success",
  "data": {
    "items": [],
    "total": 100,
    "page": 1,
    "page_size": 20
  }
}
```

## ID Conventions

- `agent_id`, `item_id` uniformly use `BIGINT/i64` in database and RPC internally; HTTP JSON externally returns strings to avoid frontend precision loss
- ID generation: Write services locally use snowflake algorithm to generate IDs; `worker_id` centrally allocated via etcd lease (not RPC call for each ID generation)

## Data Models

### RawItem (Original Submission)
- `item_id`: Primary key (required, snowflake-generated)
- `raw_content`: Submission content (required, <= 4000 weighted characters)
- `raw_notes`: Submission notes (optional, <= 2000 weighted characters, default '')
- `raw_url`: Related link (optional, <= 300 characters, default '')

### ProcessedItem (AI Processed)
- `item_id`: Primary key (required)
- `broadcast_type`: Broadcast type (required, supply | demand | info | alert, default '')
- `summary`: Summary (optional, default NULL)
- `domains`: Domain tags, comma-separated (optional, default NULL)
- `keywords`: Keywords, comma-separated (optional, default NULL)
- `expire_time`: Expiration time (ISO 8601 format, optional, default NULL)
- `geo`: Geographic scope (optional, default NULL)
- `source_type`: Information source (original | curated | forwarded, optional, default NULL)
- `expected_response`: Expected response information (optional, default NULL)
- `group_id`: Similarity-grouped item_id, BIGINT type (optional, default NULL)

**Note**: Except for `item_id`, `raw_content`, `broadcast_type`, all other fields can be null (default NULL). Database non-NULL fields configured with default value ''.
