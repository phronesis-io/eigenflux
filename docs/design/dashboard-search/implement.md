# Implementation and validation

Implemented private Dashboard search across EigenFlux, Commission and the website. Existing discovery search and Dashboard login-link commands remain available. Service and order matching stays in its owning service; literal filters run before pagination. Both HTTP and RPC adapters reject search requests when the downstream version does not confirm search support.

## Passed locally

- Core service build and independent CLI native build; CLI full test suite.
- Console V2 and trade BFF package suites with PostgreSQL; targeted `go vet`.
- Commission full build and full Go package suite; targeted `go vet`.
- Commission/Order literal-query PostgreSQL tests and platform integration suite.
- Dedicated deployed search test using real HTTP, Console cookies, trusted Commission delegation, RPC, PostgreSQL and the CLI. Five groups and foreign-record exclusion pass, including exact owned-service detail and forbidden foreign-service detail.
- Authentication integration suite.
- Website production build, complete Vitest suite, and desktop/mobile browser rendering checks. Dedicated interaction tests cover literal highlights, large IDs, stale queries/accounts, pagination, retry, service details and exact message anchors.

## Environment limits

The broad legacy end-to-end suite could not complete because this isolated stack has no LLM/embedding API credentials (provider HTTP 401 while waiting for profile processing). Two existing website statistics tests also failed because their published items were discarded by the LLM processing path. These suites are not reported as passing. The dedicated search path does not require an LLM.

The signed multi-platform CLI release script requires signing-key configuration absent from this worktree. The native CLI build and full CLI tests pass; no release bundle was produced.

## Scope note

Current main has no persisted broadcast draft model. All existing broadcast states and Commission drafts are searchable. Adding a new broadcast-draft lifecycle is separate from searching existing records.
