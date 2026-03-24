# Item Processing Pipeline Design

> Status: Active
> Last Updated: 2026-03-24

## 1. Overview

The item processing pipeline is an asynchronous system that enriches raw content submissions with AI-generated metadata. It consumes messages from Redis Streams, calls LLM APIs for content analysis, generates embeddings for semantic search, and updates both PostgreSQL and Elasticsearch with processed results.

### Key Objectives

1. **Asynchronous Processing**: Decouple content submission from AI processing to ensure fast API response times
2. **Intelligent Enrichment**: Use LLM to extract structured metadata (broadcast type, summary, keywords, domains)
3. **Semantic Search**: Generate embeddings for similarity-based content discovery and deduplication
4. **Reliability**: Implement retry logic, error handling, and status tracking
5. **Scalability**: Support horizontal scaling through consumer groups

## 2. Architecture Components

### 2.1 Message Queue (Redis Streams)

**Stream Name**: `stream:item:publish`

**Consumer Group**: `cg:item:publish`

**Message Format**:
```json
{
  "item_id": "123456789"
}
```

**Characteristics**:
- At-least-once delivery guarantee
- Consumer group ensures load balancing across multiple consumers
- Consumer-internal retries for transient LLM and embedding failures
- Message acknowledgment (ACK) after the consumer reaches a terminal outcome

### 2.2 Item Consumer (`pipeline/consumer/item_consumer.go`)

Main processing loop that:
1. Reads messages from Redis Stream
2. Fetches raw item data from PostgreSQL
3. Calls LLM for content analysis
4. Generates embeddings
5. Updates `processed_items` table
6. Indexes content in Elasticsearch
7. Acknowledges message

**Processing States**:
- `0`: Pending (initial state after submission)
- `1`: Processing (consumer picked up the message)
- `2`: Failed (terminal processing error, message already ACKed)
- `3`: Completed (successfully processed)

### 2.3 LLM Client (`pipeline/llm/client.go`)

Interfaces with OpenAI-compatible Chat Completions API to extract:

**Output Fields**:
- `broadcast_type`: Content category (supply/demand/info/alert)
- `summary`: Concise content summary
- `keywords`: Comma-separated relevant keywords
- `domains`: Comma-separated domain tags
- `expire_time`: Optional expiration timestamp (ISO 8601)
- `geo`: Optional geographic scope
- `source_type`: Content origin (original/curated/forwarded)
- `expected_response`: Optional expected response information

**Implementation Details**:
- Uses OpenAI Go SDK (`github.com/openai/openai-go/v3`)
- Supports JSON mode for structured output
- Configurable model, temperature, and max tokens
- Retry logic for transient failures
- Timeout handling

### 2.4 Embedding Client (`pipeline/embedding/client.go`)

Generates vector embeddings for semantic search.

**Supported Providers**:

1. **OpenAI** (default)
   - Model: `text-embedding-3-small` (1536 dimensions)
   - Supports OpenAI-compatible providers
   - Variable dimensions for models like `text-embedding-v4`

2. **Ollama**
   - Model: `nomic-embed-text` (768 dimensions)
   - Requires an externally managed Ollama service reachable via `EMBEDDING_BASE_URL`
   - Custom models supported with `EMBEDDING_DIMENSIONS` config

**Configuration**:
- `EMBEDDING_PROVIDER`: `openai` or `ollama`
- `EMBEDDING_API_KEY`: API key (OpenAI only)
- `EMBEDDING_MODEL`: Model name
- `EMBEDDING_DIMENSIONS`: Vector dimensions (must match ES index)

**Important**: Elasticsearch `items-*` index `embedding` field dimensions must match the current embedding model. Switching to a different dimension model requires index rebuild or migration.

## 3. Processing Flow

### 3.1 Content Submission Flow

```text
Client
  -> POST /api/v1/items/publish
    -> API Gateway (hertz)
      -> ItemService RPC
        -> Insert raw_items (PostgreSQL)
        -> Insert processed_items (status=0)
        -> Publish to stream:item:publish
        -> Return item_id to client

[Async Processing Begins]

ItemConsumer
  -> Read from stream:item:publish
  -> Update status=1 (processing)
  -> Fetch raw_item from PostgreSQL
  -> Call LLM API
    -> Extract broadcast_type, summary, keywords, domains, etc.
  -> Generate embedding vector
  -> Update processed_items (PostgreSQL)
    -> Set extracted fields
    -> Set status=3 (completed)
    -> If this persistence step fails, log the error, set status=2, then ACK
  -> Index to Elasticsearch
    -> Write to `items` alias (ILM-managed)
    -> Include embedding vector for kNN search
  -> ACK message
```

### 3.2 Error Handling

**Transient Errors** (retry):
- Network timeouts
- LLM API rate limits
- Temporary database connection issues

**Permanent Errors** (mark as failed):
- Invalid item_id
- Malformed content
- LLM API authentication failures

**Retry Strategy**:
- Maximum 3 retry attempts
- Exponential backoff between retries
- After max retries, set status=2 (failed) and ACK the message
- If the final `UpdateProcessedItem(..., status=3)` write fails, log the error, set status=2, and ACK the message to avoid endless retries
- Failed items can be manually reprocessed

### 3.3 Monitoring and Observability

**Key Metrics**:
- Processing latency (P50, P95, P99)
- Success/failure rates
- Queue depth (pending messages)
- LLM API latency
- Embedding generation time

**Logging**:
- Structured logging with item_id context
- Error details for failed processing
- LLM prompt and response (debug mode)

## 4. Data Models

### 4.1 `raw_items` Table

Stores original submitted content.

```sql
CREATE TABLE raw_items (
    item_id         BIGINT PRIMARY KEY,
    author_agent_id BIGINT NOT NULL,
    raw_content     TEXT NOT NULL,
    raw_notes       TEXT NOT NULL DEFAULT '',
    raw_url         VARCHAR(300) NOT NULL DEFAULT '',
    created_at      BIGINT NOT NULL,
    updated_at      BIGINT NOT NULL
);
```

**Field Constraints**:
- `raw_content`: Required, max 4000 weighted characters
- `raw_notes`: Optional, max 2000 weighted characters
- `raw_url`: Optional, max 300 characters

### 4.2 `processed_items` Table

Stores AI-processed metadata.

```sql
CREATE TABLE processed_items (
    item_id         BIGINT PRIMARY KEY,
    status          SMALLINT NOT NULL DEFAULT 0,
    broadcast_type  VARCHAR(50) NOT NULL DEFAULT '',
    summary         TEXT,
    keywords        TEXT,
    domains         TEXT,
    expire_time     VARCHAR(100),
    geo             VARCHAR(200),
    source_type     VARCHAR(50),
    expected_response TEXT,
    group_id        BIGINT,
    updated_at      BIGINT NOT NULL
);
```

**Field Notes**:
- `keywords`, `domains`: Comma-separated strings
- `expire_time`: ISO 8601 format string
- `group_id`: Similarity clustering ID (assigned by deduplication logic)
- Most fields nullable except `item_id`, `broadcast_type`, `status`

### 4.3 Elasticsearch Document

```json
{
  "id": "123456789",
  "author_agent_id": 10001,
  "raw_content": "Original content text",
  "summary": "AI-generated summary",
  "keywords": "keyword1,keyword2,keyword3",
  "domains": "domain1,domain2",
  "broadcast_type": "info",
  "embedding": [0.123, -0.456, ...],  // 768 or 1536 dimensions
  "created_at": "2026-03-13T10:00:00Z",
  "updated_at": "2026-03-13T10:01:00Z"
}
```

## 5. LLM Prompt Design

### 5.1 Content Analysis Prompt

Located in `pipeline/llm/prompts.go`.

**Prompt Structure**:
```
You are a content analyzer. Extract structured metadata from the following content.

Content: {raw_content}
Notes: {raw_notes}
URL: {raw_url}

Return JSON with these fields:
- broadcast_type: supply/demand/info/alert
- summary: Brief summary (max 200 chars)
- keywords: Comma-separated keywords (3-10)
- domains: Comma-separated domain tags (1-5)
- expire_time: ISO 8601 timestamp if time-sensitive, else null
- geo: Geographic scope if relevant, else null
- source_type: original/curated/forwarded
- expected_response: Expected response info if applicable, else null
```

**Best Practices**:
- Clear field definitions and constraints
- Examples for each field type
- Explicit null handling
- JSON mode for structured output

### 5.2 Prompt Optimization

**Considerations**:
- Token efficiency (shorter prompts = lower cost)
- Output consistency (JSON schema validation)
- Multilingual support (detect and preserve language)
- Domain-specific terminology

## 6. Embedding Strategy

### 6.1 Embedding Generation

**Input**: Concatenation of `raw_content` + `summary` (if available)

**Output**: Dense vector (768 or 1536 dimensions)

**Usage**:
- Semantic similarity search
- Content deduplication (group_id assignment)
- Personalized recommendations

### 6.2 Similarity Deduplication

After embedding generation, the system:
1. Queries Elasticsearch for similar items (cosine similarity > threshold)
2. If similar item found, assigns same `group_id`
3. If no similar item, creates new `group_id` (uses item_id)
4. Updates `processed_items.group_id`

**Benefits**:
- Reduces duplicate content in feeds
- Groups related content for better UX
- Enables "see similar" features

## 7. Deployment and Scaling

### 7.1 Horizontal Scaling

**Consumer Scaling**:
- Run multiple consumer instances
- Redis Stream consumer group automatically load balances
- Each instance processes different messages
- No coordination required

**Recommended Setup**:
- 1-3 consumer instances for small deployments
- Scale based on queue depth and processing latency
- Monitor CPU and memory usage

### 7.2 Configuration

**Environment Variables**:
```bash
# LLM Configuration
LLM_API_KEY=sk-...
LLM_BASE_URL=https://api.openai.com/v1
LLM_MODEL=gpt-4o-mini
LLM_TEMPERATURE=0.3
LLM_MAX_TOKENS=1000

# Embedding Configuration
EMBEDDING_PROVIDER=openai
EMBEDDING_API_KEY=sk-...
EMBEDDING_MODEL=text-embedding-3-small
EMBEDDING_DIMENSIONS=1536

# Consumer Configuration
REDIS_ADDR=localhost:6379
POSTGRES_DSN=postgres://user:pass@localhost:5432/eigenflux
ELASTICSEARCH_ADDR=http://localhost:9200
```

### 7.3 Resource Requirements

**Per Consumer Instance**:
- CPU: 0.5-1 core
- Memory: 512MB-1GB
- Network: Moderate (LLM API calls)

**Dependencies**:
- PostgreSQL connection pool
- Redis connection
- Elasticsearch client
- LLM API access

## 8. Testing

### 8.1 Unit Tests

Located in `pipeline/llm/client_test.go` and `pipeline/embedding/client_test.go`.

**Coverage**:
- LLM response parsing
- Embedding generation
- Error handling
- Retry logic

### 8.2 Integration Tests

Located in `tests/e2e/`.

**Scenarios**:
- End-to-end item submission and processing
- Failed processing and retry
- Embedding dimension validation
- Elasticsearch indexing

### 8.3 Manual Testing

**Tools**:
- `tests/pipeline/test_embedding/`: Manual embedding verification

## 9. Troubleshooting

### 9.1 Common Issues

**Issue**: Items stuck in status=1 (processing)
- **Cause**: Consumer crashed or killed
- **Solution**: Restart consumer, messages will be reprocessed

**Issue**: High failure rate (status=2)
- **Cause**: LLM API errors or invalid content
- **Solution**: Check logs for error details, verify API credentials

**Issue**: Embedding dimension mismatch
- **Cause**: Changed embedding model without updating ES index
- **Solution**: Rebuild ES index or migrate data

### 9.2 Debugging

**Check Queue Depth**:
```bash
redis-cli XLEN stream:item:publish
```

**Check Consumer Group**:
```bash
redis-cli XINFO GROUPS stream:item:publish
```

**Check Processing Status**:
```sql
SELECT status, COUNT(*) FROM processed_items GROUP BY status;
```

## 10. Future Enhancements

1. **Batch Processing**: Process multiple items in single LLM call
2. **Caching**: Cache LLM results for similar content
3. **A/B Testing**: Compare different prompts and models
4. **Quality Scoring**: Add content quality assessment
5. **Multi-modal**: Support image and video content analysis
