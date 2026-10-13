# Deployed private Dashboard search

This regression needs a disposable loopback stack with real API, Auth, Profile,
PM and Item services plus current Commission, Order and Commission HTTP services.
Both repositories' migrations must be applied to the same PostgreSQL database;
use a separate `commission_goose_db_version` table for Commission. Redis and etcd
are required. Configure the Hub's `COMMISSION_ENDPOINT` and Ed25519 delegation
key; Commission needs the matching `CONSOLE_DELEGATION_PUBLIC_KEYS`. No LLM,
payment provider or real account is needed.

```bash
DASHBOARD_SEARCH_TEST_URL=http://127.0.0.1:18092 \
DASHBOARD_SEARCH_COMMISSION_URL=http://127.0.0.1:18095 \
EIGENFLUX_TEST_CLI=/absolute/path/to/build/cli/eigenflux \
PG_DSN='postgres://eigenflux:eigenflux123@127.0.0.1:15432/eigenflux?sslmode=disable' \
go test -v ./tests/dashboardsearch -run '^TestDashboardSearchDeployed$' -count=1
```

Fixtures create two completed Agent V2 principals, Console sessions and distinct
friend/message/broadcast/service/order records. Fixture creation uses SQL; the
test subsequently calls real `Unfriend`, `CloseConv` and `DeleteCommission`
business APIs. For each revocation, a unique query creates a fresh cache entry,
is repeated, and is re-read through both auth entries within five seconds.
Read-only SQL also checks durable removal/hiding/tombstoning. The other owner
and the deleted listing's immutable historical order must retain visibility.

Additional assertions cover exact foreign IDs in every category, category scopes,
credential entry separation, literal SQL metacharacters, counterparty public-name
lookup, descending cursor pagination and status filtering, both owners through
the native CLI, and owner rate limiting with `Retry-After`. These test the API
and CLI; the unified frontend search page remains deferred upstream.

Fixture rows deliberately remain in the disposable stack for inspection. The
test accepts only explicitly supplied loopback HTTP and PostgreSQL endpoints.
Never select shared development or production data. With no
`DASHBOARD_SEARCH_TEST_URL`, ordinary unit runs skip this deployed tier.

`DASHBOARD_SEARCH_EVIDENCE_FILE` optionally writes credential-free JSON containing
strict boolean facts and revocation latency. Arena's
`scripts/probes/dashboard_search_deployed.py` requires this evidence plus exactly
one non-skipped passing Go test, checks the owned containers' loopback bindings,
and records image, source, test, probe and CLI hashes. The Arena scenario is
`console-dashboard-search-privacy`; it uses an explicit `arena-dashboard-*`
stack, not the default core-only Hub, because empty trade groups are insufficient.
