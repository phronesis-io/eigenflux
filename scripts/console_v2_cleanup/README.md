# Console V2 retention worker

This command is dry-run by default and prints its bounded cleanup plan. It uses
at most two PostgreSQL connections, mutates at most `batch-size` rows per SQL
statement, and stops at `time-budget` so it is safe to invoke periodically.

```bash
PG_DSN='postgres://...' go run ./scripts/console_v2_cleanup
PG_DSN='postgres://...' go run ./scripts/console_v2_cleanup --apply --batch-size=1000 --time-budget=30s
```

Production rollback disables Console V2 feature flags and preserves V2 data.
Do not run Goose `down` against a production database containing V2 data.
