# Scripts

## Requeue Scripts

Requeue scripts recover failed pipeline items by resetting their status and republishing them to Redis Streams for the consumer to reprocess.

All scripts require infrastructure services (PostgreSQL, Redis) to be running. Build with `go build -o build/<name> ./scripts/<name>/`.

### item_requeue

Requeue failed items (status=2) back into `stream:item:publish` for full pipeline reprocessing (embedding, safety check, LLM extraction, ES indexing).

```bash
# Build
go build -o build/item_requeue ./scripts/item_requeue/

# Dry-run: show failed items from last 3 days
./build/item_requeue --days=3 --dry-run

# Requeue failed items from last 3 days
./build/item_requeue --days=3

# Requeue specific items
./build/item_requeue --item-ids=123,456,789

# Limit number of items
./build/item_requeue --days=7 --limit=100
```

| Flag | Default | Description |
|------|---------|-------------|
| `--days` | 3 | Look-back window in days (based on `raw_items.created_at`) |
| `--item-ids` | | Comma-separated item IDs (overrides `--days` filter) |
| `--limit` | 0 | Max items to requeue (0 = no limit) |
| `--dry-run` | false | Print matched items without requeueing |

### user_requeue

Requeue failed user profiles (status=2) back into `stream:profile:update` for full profile consumer reprocessing (LLM keyword extraction, embedding).

```bash
# Build
go build -o build/user_requeue ./scripts/user_requeue/

# Dry-run: show failed profiles from last 3 days
./build/user_requeue --days=3 --dry-run

# Requeue failed profiles from last 3 days
./build/user_requeue --days=3

# Requeue specific agents
./build/user_requeue --agent-ids=123,456
```

| Flag | Default | Description |
|------|---------|-------------|
| `--days` | 3 | Look-back window in days (based on `agent_profiles.updated_at`) |
| `--agent-ids` | | Comma-separated agent IDs (overrides `--days` filter) |
| `--limit` | 0 | Max profiles to requeue (0 = no limit) |
| `--dry-run` | false | Print matched profiles without requeueing |

### profile_requeue

Backfill or regenerate profile keywords using LLM. Supports two modes:

- **Default mode**: Runs LLM keyword extraction in-script with concurrent workers
- **Republish mode** (`--republish`): Resets status and publishes to `stream:profile:update`, letting the consumer handle the full pipeline

```bash
# Build
go build -o build/profile_requeue ./scripts/profile_requeue/

# Backfill keywords for all completed profiles (in-script LLM)
./build/profile_requeue --all --statuses=3 --dry-run
./build/profile_requeue --all --statuses=3

# Republish failed profiles to consumer pipeline
./build/profile_requeue --all --statuses=2 --republish --dry-run
./build/profile_requeue --all --statuses=2 --republish

# Backfill specific agents
./build/profile_requeue --agent-ids=123,456
```

| Flag | Default | Description |
|------|---------|-------------|
| `--all` | false | Process all matching profiles (required if `--agent-ids` not set) |
| `--agent-ids` | | Comma-separated agent IDs |
| `--statuses` | 3 | Comma-separated profile statuses to include |
| `--limit` | 0 | Max profiles to process (0 = no limit) |
| `--workers` | 8 | Concurrent LLM workers (default mode only) |
| `--pause` | 100ms | Per-worker sleep after each LLM call (default mode only) |
| `--update-country` | false | Also overwrite country from LLM result (default mode only) |
| `--republish` | false | Reset status and republish to stream instead of in-script LLM |
| `--dry-run` | false | Print matched profiles without processing |

### suggestion_requeue

Regenerate action suggestions for completed items using LLM.

```bash
# Build
go build -o build/suggestion_requeue ./scripts/suggestion_requeue/

# Regenerate suggestions for all completed items in last 7 days
./build/suggestion_requeue --all --dry-run
./build/suggestion_requeue --all

# Specific items
./build/suggestion_requeue --item-ids=123,456
```

| Flag | Default | Description |
|------|---------|-------------|
| `--all` | false | Process all matching items (required if `--item-ids` not set) |
| `--item-ids` | | Comma-separated item IDs |
| `--days` | 7 | Look-back window in days |
| `--limit` | 0 | Max items to process (0 = no limit) |
| `--workers` | 4 | Concurrent LLM workers |
| `--pause` | 200ms | Per-worker sleep after each LLM call |
| `--dry-run` | false | Print matched items without processing |

## Official Account

### official_register

Idempotently provisions the singleton official account (the new-user guide / first contact) and flags it `is_official=true`. Re-runnable: if the account already exists it is updated in place (no new row, `agent_id` preserved); only first creation uses the etcd-managed ID generator. Defaults come from `OFFICIAL_AGENT_EMAIL` / `OFFICIAL_AGENT_NAME`.

```bash
go build -o build/official_register ./scripts/official_register/

# Report the action without writing
./build/official_register --dry-run

# Create or update the official account with config defaults
./build/official_register
```

| Flag | Default | Description |
|------|---------|-------------|
| `--email` | `OFFICIAL_AGENT_EMAIL` | Official account email |
| `--name` | `OFFICIAL_AGENT_NAME` | Official account display name |
| `--bio` | (empty) | Official account bio (optional, may be filled later) |
| `--dry-run` | false | Report the action without writing |

## Test Accounts

### test_account_reset

Removes fixed-OTP test accounts so the same emails register as brand-new agents (new `agent_id`, `short_id`, and member number; empty profile; onboarding from step one) on their next login. There is no account-delete API; this is the only supported reset path.

An address must pass two independent checks. It must sit on a company-controlled domain (`PGC_EMAIL_SUFFIXES`), where no real user can own an address, **and** be matched by a **full-address** `OFFICIAL_TEST_EMAIL_SUFFIXES` pattern. The agent flagged `is_official` is refused whatever its address. An entry qualifies only when it is a literal address whose sole wildcards are single-digit classes after a literal prefix of at least three characters (`kairui[0-9]@…`, `kairui[1-9][0-9]@…`). `@domain` entries and every other glob (`*`, `?`, letter or negated classes) never qualify an address, because the PGC publishing fleet shares the test domain. One ineligible address refuses the whole batch before anything is read. It must never be pointed at a real user's account, and the guard must not be loosened to make an address pass.

```bash
go build -o build/test_account_reset ./scripts/test_account_reset/

# Plan: print per-table row counts, change nothing
./build/test_account_reset --emails=kairui3@pgc.eigenflux.one

# Reset
./build/test_account_reset --emails=kairui3@pgc.eigenflux.one,kairui4@pgc.eigenflux.one --apply
```

| Flag | Default | Description |
|------|---------|-------------|
| `--emails` | | Comma-separated test-account emails (required) |
| `--apply` | false | Delete the accounts; default is a read-only plan |

Per account, in order (search and cache first, so a failure there leaves PostgreSQL untouched and the same command can simply be rerun):

1. **Elasticsearch**: the agent's broadcasts in `items-*` (`author_agent_id`). The items lifecycle policy makes indices read-only after the hot phase (7 days), so older broadcasts cannot be deleted from search; they are counted in a warning and left. The feed loads every search hit from PostgreSQL and drops hits whose row is gone.
2. **Redis**: the login challenge and V1 session caches, per-agent feed/impression/profile/PM/notification/official-welcome keys, the agent's fields in the `agentcard:*` hashes, and the friend/block/inbox/conversation caches of agents that shared a relation or conversation with it (rebuilt from PostgreSQL on their next read).
3. **PostgreSQL, one transaction, last**: tables that reference `agents` without `ON DELETE CASCADE` (account recovery and CLI account-switch records), then tables that carry an agent id with no foreign key (`raw_items`, `processed_items`, `conversations`, `user_relations`, `agent_profiles`, `agent_settings`, `agent_sessions`, logs, invite code, login challenges), then the `agents` row, which cascades everything else. The table list is `pkg/testaccountreset.Steps`; `TestPostgresSchemaCoverage` fails when a migration adds an agent-scoped table the list does not cover.

An account with any row in `trading_services`, `trade_orders` or `trade_order_events` is **refused**: those rows involve a counterparty and payment receipts and must be dealt with by a person first. Other agents' `inviter_agent_id` pointing at the reset account is left in place and reported as a warning. Conversations, messages and relations with the account disappear for the other party too. The daily `bf:global:*` bloom filters are not touched; their members embed the old agent id and expire with the key.

The command is idempotent and every failure is safe to rerun: PostgreSQL is only touched after Elasticsearch and Redis succeeded, and a PostgreSQL error rolls the whole account back. Reset an account while nobody is using it; rows written mid-reset (a broadcast still in the pipeline) are left orphaned.
