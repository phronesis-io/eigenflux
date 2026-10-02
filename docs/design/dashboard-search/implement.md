# Implementation and validation

Implemented private Dashboard search APIs across EigenFlux and Commission, with a CLI entry. The unified frontend search interface is deferred. Existing discovery search and Dashboard login-link commands remain available. PM, Item, Profile and Commission own their search RPCs and data access. The BFF only invokes service clients and projects results; literal filters run before pagination. Both HTTP and RPC adapters reject search requests when the downstream version does not confirm search support.

## Passed locally

- Core service build and independent CLI native build; CLI full test suite.
- PM, Item and Profile handler/DAL suites, shared matching helpers, Console V2 and trade BFF suites with PostgreSQL; targeted `go vet`. BFF tests inject RPC clients without a database, and private RPCs reject absent or mismatched owner identity.
- Commission full build and full Go package suite; targeted `go vet`.
- Commission/Order literal-query PostgreSQL tests and platform integration suite.
- Dedicated deployed search test using real HTTP, Console cookies, trusted Commission delegation, RPC, PostgreSQL and the CLI. Five groups and foreign-record exclusion pass, including exact owned-service detail, forbidden foreign-service detail and a query matching only the order counterparty name through Profile RPC.
- Authentication and Item integration suites.

## Environment limits

The broad legacy end-to-end suite could not complete because this isolated stack has no LLM/embedding API credentials (provider HTTP 401 while waiting for profile processing). PM and website integration tests also failed because their published item fixtures were discarded by the LLM processing path. These suites are not reported as passing. The dedicated search path does not require an LLM.

The signed multi-platform CLI release script requires signing-key configuration absent from this worktree. The native CLI build and full CLI tests pass; no release bundle was produced.

## Scope note

Current main has no persisted broadcast draft model. All existing broadcast states and Commission drafts are searchable. Adding a new broadcast-draft lifecycle is separate from searching existing records.
