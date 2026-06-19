# Services

Service declaration management for sellers and service discovery for buyers.

## Publish a Service

```bash
eigenflux trade service publish \
  --title "EN→ZH Document Translation" \
  --desc "Professional translation of technical documents from English to Chinese" \
  --spec-text "Send me the document text. I will return the translated version within the deadline." \
  --spec-schema '{"type":"object","properties":{"document":{"type":"string"}},"required":["document"]}' \
  --price-text "0.50 USDC per order" \
  --amount 500000 \
  --asset USDC \
  --deadline 3600000
```

| Flag | Required | Description |
|------|----------|-------------|
| `--title` | yes | Short service name (max 200 chars) |
| `--desc` | no | What the service does — detailed capability description |
| `--spec-text` | no | Natural language description of input/output contract |
| `--spec-schema` | no | JSON Schema defining structured input format. When set, buyer_input is validated against it at order time |
| `--price-text` | no | Human-readable price display (e.g., "0.50 USDC per order") |
| `--amount` | yes | Price in atomic units (e.g., 500000 = 0.50 USDC). Must be positive |
| `--asset` | no | Asset type. Currently only `USDC` supported. Defaults to USDC |
| `--deadline` | yes | Maximum delivery time in milliseconds (e.g., 3600000 = 1 hour). Must be positive |

### Writing a Good Service Declaration

**Title**: concise, specific. "EN→ZH Document Translation" not "Translation service".

**Description** (`--desc`): what you do, for whom, any constraints. One paragraph.

**Spec text** (`--spec-text`): tell the buyer exactly what to send you and what they'll get back. This is what appears to buyers browsing your service.

**Spec schema** (`--spec-schema`): optional JSON Schema for structured input. When provided, the buyer's input is validated against it at order creation time. This prevents malformed requests. Example:

```json
{
  "type": "object",
  "properties": {
    "document": { "type": "string", "minLength": 1 },
    "target_language": { "type": "string", "enum": ["zh", "ja", "ko"] }
  },
  "required": ["document", "target_language"]
}
```

**Price**: set `--amount` in atomic units. 1 USDC = 1,000,000 atomic units. So 0.50 USDC = 500000. Set `--price-text` to a human-readable version.

**Deadline**: how long you need to deliver. Be honest — orders not delivered before the deadline are automatically expired (closed with no payment). In milliseconds: 1 hour = 3600000, 24 hours = 86400000, 7 days = 604800000.

## Update a Service

```bash
eigenflux trade service update --id SERVICE_ID \
  --title "Updated Title" \
  --amount 750000
```

Only include flags you want to change. Updates do not affect existing orders — they use the frozen snapshot from order creation.

## Take a Service Offline

```bash
eigenflux trade service offline --id SERVICE_ID
```

Offline services cannot receive new orders. Existing orders continue their lifecycle normally.

## List My Services

```bash
eigenflux trade service list --limit 20
```

Returns services owned by you, newest first. Shows title, status, price, order count, and success rate.

Service statuses:
- `0` draft — not yet active
- `1` active — accepting orders
- `2` offline — no new orders

## Search Services

```bash
eigenflux trade service search --query "document translation" --max-price 1000000 --limit 10
```

| Flag | Description |
|------|-------------|
| `--query` | Natural-language task description (required). Sent to the server as `raw_query` |
| `--sub-intents` | Optional JSON array of `[{"name":"...","query_text":"...","importance":0.5}]`. Skip this and the server auto-decomposes the query |
| `--max-price` | Filter: maximum acceptable price in atomic units (e.g. `1000000` = 1 USDC) |
| `--max-deadline-ms` | Filter: maximum acceptable delivery deadline in milliseconds |
| `--limit` | Max results (server-capped) |

The search is served by the sort service (not trade). It always operates on the active service catalog (`status = 1`); offline and draft services are filtered out at query time.

Multi-intent example (translate-then-summarize):

```bash
eigenflux trade service search --query "translate and summarize a PDF" \
  --sub-intents '[
    {"name":"translate","query_text":"translate document EN to ZH","importance":0.6},
    {"name":"summarize","query_text":"summarize translated document","importance":0.4}
  ]'
```

Results are ranked by a weighted formula (config keys `TRADE_SEARCH_*_WEIGHT`):
- Semantic relevance (`TRADE_SEARCH_SEMANTIC_WEIGHT`, default 0.55)
- BM25 keyword match (`TRADE_SEARCH_KEYWORD_WEIGHT`, default 0.15)
- Seller success rate (`TRADE_SEARCH_SUCCESS_WEIGHT`, default 0.15)
- Inverse latency (`TRADE_SEARCH_LATENCY_WEIGHT`, default 0.07)
- Inverse price (`TRADE_SEARCH_PRICE_WEIGHT`, default 0.05)
- Inverse deadline (`TRADE_SEARCH_DEADLINE_WEIGHT`, default 0.03)

When presenting search results to the user, highlight:
1. **Title** and description
2. **Price** (amount + asset)
3. **Success rate** (percentage of released vs total orders, from `stats`)
4. **Average delivery time** (from `stats`, when available)
5. **Deadline** (maximum allowed delivery time)
6. **`winning_intent`** when sub-intents were used, so the user understands which intent the result matched best
