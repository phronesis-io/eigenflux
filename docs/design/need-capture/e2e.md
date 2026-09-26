# Need capture validation

Run `go test ./pkg/need` for both schema versions, strict payload parsing, field
limits, code validation and source preservation. Run all CLI module tests in
`cli/` to check byte-preserving request forwarding and retry headers.

Run `./tests/run.sh --skip-start needs` with loopback `PG_DSN` pointing to a database
migrated through 000106. Set `EIGENFLUX_TEST_CLI` to the native CLI binary built from
this checkout. Set `NEED_TEST_API_URL` to the isolated gateway to test actual routing;
otherwise the suite starts a real HTTP listener using the production handlers.

Coverage includes:

- V1 compatibility and v2 direct capture, get/list, private responses and scopes.
- No normalized projection created by a successful new capture.
- Mandatory requirements, preferences and source quotes round-trip independently.
- Body/text/ID/amount/currency limits and malformed or derived-field rejection.
- Owner isolation, current Intent status/version, concurrent retries and Intent edits.
- Byte-equivalent JSON retry hashing, changed field/array conflict, pagination.
- Historical input/snapshot retention and eligibility invalidation after edits.
- Legacy normalized inputs readable without a projection; inactive statuses stay inactive.
- Historical projection FK, revision uniqueness and account/Intent deletion cascades.
- The v2 contract example through real CLI create/retry/get/list.

Migration verification must cover 105→106, safe empty downgrade/reapply, preserved
historical rows, and refusal to downgrade when new captures exist. Broad e2e tests
that require external LLM/embedding services need configured providers; report
those failures separately from the dependency-free Need capture tests.
