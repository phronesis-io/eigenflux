# Request Info and RPC Bootstrap Unification

## Summary

Unify request metadata types under `pkg/reqinfo/` and unify Kitex client/server bootstrap options under a shared package so that API-to-RPC and RPC-to-RPC wiring follow one convention.

This refactor keeps current business behavior unchanged. It reorganizes request metadata access, centralizes TTHeader/transmeta wiring, raises the default RPC timeout to 10 seconds, and removes duplicated bootstrap code.

## Goals

- Move request metadata packages into one discoverable directory: `pkg/reqinfo/`
- Keep `ClientInfo` and `AuthInfo` as separate types with clear responsibilities
- Centralize common Kitex client options and server options in one reusable package
- Apply the shared RPC bootstrap convention to all current API and RPC entrypoints
- Set a default RPC timeout of 10 seconds while allowing per-client overrides
- Preserve current metainfo keys and current notification-expression behavior
- Verify the refactor with build and notify E2E tests
- Update primary project docs to reflect the new RPC convention

## Non-Goals

- No IDL changes
- No changes to audience expression variables or semantics
- No changes to middleware responsibilities
- No changes to service discovery topology
- No changes to retry policy, circuit breaking, or tracing implementation
- No generic "request context map" abstraction

## Current State

### Request metadata

Request metadata currently lives in two top-level packages:

- `pkg/clientinfo/`
- `pkg/authinfo/`

This keeps type responsibilities clear, but the packages are physically separated even though they serve the same request-metainfo propagation concern.

### RPC bootstrap

Kitex client wiring currently exists in two places:

- `api/main.go`
- `rpc/feed/main.go`

Kitex server wiring is repeated in each RPC service `main.go`.

The TTHeader/transmeta setup is now broadly correct, but it is duplicated. Any future change to timeout, transport protocol, or metadata propagation requires another repository-wide sweep.

## Design

### 1. Request metadata package layout

Create a single directory:

```text
pkg/reqinfo/
  client.go
  auth.go
```

The directory groups request metadata by concern, but the types remain separate.

#### `ClientInfo`

`ClientInfo` continues to represent client-supplied request metadata, currently:

- `skill_ver`
- `skill_ver_num`

It remains responsible for:

- metainfo keys for client request metadata
- decoding values from `context.Context`
- exporting expression variables via `ToVars()`

#### `AuthInfo`

`AuthInfo` continues to represent authentication-derived request metadata, currently:

- `agent_id`
- `email`

It remains responsible for:

- metainfo keys for auth metadata
- decoding values from `context.Context`
- exporting expression variables via `ToVars()`

#### API shape

The package exposes typed helpers under one package namespace. Use the shorter form for consistency with existing code style:

```go
reqinfo.ClientFromContext(ctx) ClientInfo
reqinfo.AuthFromContext(ctx) AuthInfo
```

#### Migration rule

- Replace imports of `pkg/clientinfo` with `pkg/reqinfo`
- Replace imports of `pkg/authinfo` with `pkg/reqinfo`
- Keep middleware split:
  - `api/middleware/clientinfo.go` still writes client-derived metadata
  - `api/middleware/auth.go` still writes auth-derived metadata
- Delete the old `pkg/clientinfo/` and `pkg/authinfo/` directories after all references move

### 2. Shared RPC bootstrap package

Create a shared package for Kitex bootstrap conventions:

```text
pkg/rpcx/
  options.go
```

The package centralizes default client/server options.

### 2.1 Client options

Provide a helper that builds common Kitex client options:

```go
func ClientOptions(resolver discovery.Resolver, extra ...client.Option) []client.Option
```

Default behavior:

- `client.WithResolver(...)`
- `client.WithRPCTimeout(10 * time.Second)`
- `client.WithTransportProtocol(transport.TTHeader)`
- `client.WithMetaHandler(transmeta.ClientTTHeaderHandler)`

`extra` options are appended after defaults. Kitex applies options in order, so later options override earlier ones. This allows callers to override timeout or other defaults explicitly.

Intended usage:

- `api/main.go`
- `rpc/feed/main.go`

### 2.2 Server options

Provide a helper that builds common Kitex server options:

```go
func ServerOptions(addr net.Addr, registry registry.Registry, serviceName string, extra ...server.Option) []server.Option
```

Default behavior:

- `server.WithServiceAddr(...)`
- `server.WithRegistry(...)`
- `server.WithServerBasicInfo(&rpcinfo.EndpointBasicInfo{ServiceName: ...})`
- `server.WithMetaHandler(transmeta.ServerTTHeaderHandler)`

`extra` options remain available for future extension.

### 2.3 Timeout change rationale and override policy

**Current state**: Most RPC clients use 3-second timeout. AuthService uses 5 seconds.

**Proposed change**: Raise default to 10 seconds.

**Rationale**:
- Current 3-second timeout is tight for cross-service calls under load
- 10 seconds provides headroom for P99 latency spikes without masking real performance issues
- Aligns with common microservice timeout conventions
- Still short enough to fail fast on genuine unavailability

**Impact**:
- Clients will wait longer before timing out on slow or unavailable services
- This is acceptable because the system already handles async processing for heavy workloads
- No change to business logic or retry behavior

**Override policy**:
- 10 seconds is the repository-wide default
- A specific client may override timeout by passing `client.WithRPCTimeout(...)` in the `extra` options
- Overrides should be local and obvious at callsite construction time

This keeps the default centralized without blocking narrow exceptions.

## Rollout Scope

### Client-side replacement

Replace handwritten client option assembly in:

- `api/main.go`
- `rpc/feed/main.go`

### Server-side replacement

Replace handwritten server option assembly in:

- `rpc/auth/main.go`
- `rpc/profile/main.go`
- `rpc/item/main.go`
- `rpc/sort/main.go`
- `rpc/feed/main.go`
- `rpc/notification/main.go`
- `rpc/pm/main.go`

### Metadata consumers

Update imports and helper calls in all current metadata consumers, including at least:

- `api/middleware/clientinfo.go`
- `api/middleware/auth.go`
- `rpc/notification/handler.go`
- `rpc/sort/handler.go`
- any other file that currently imports `pkg/clientinfo` or `pkg/authinfo`

### Cleanup

As part of this refactor:

- remove temporary metadata debug logging added for investigation in `rpc/sort/handler.go` (specifically the `log.Printf("[Sort] metadata clientinfo=%+v authinfo=%+v", ci, ai)` line added during metainfo propagation debugging)
- remove no-longer-needed duplicated timeout / transport / transmeta setup
- delete obsolete packages after import migration completes

## Behavior Guarantees

This refactor must preserve the following behaviors:

- `ef.skill_ver`, `ef.skill_ver_num`, `ef.agent_id`, and `ef.email` remain unchanged
- `ClientInfoMiddleware` continues to parse `X-Skill-Ver`
- `AuthMiddleware` continues to write authenticated identity metadata
- notification audience-expression evaluation continues to work without IDL changes
- feed-to-sort and feed-to-item metainfo propagation remains enabled through TTHeader

## Verification

### Build verification

Run:

```bash
bash scripts/common/build.sh
```

### Test verification

Run the main E2E suite for the feature touched by request-metainfo propagation:

```bash
go test -v ./tests/notify/
```

**Test migration note**: Tests in `tests/notify/` currently import `pkg/clientinfo` or `pkg/authinfo` indirectly through test helpers. After the refactor, if any test file directly imports these packages, update those imports to `pkg/reqinfo`. The `tests/testutil/` package should not need changes as it does not directly manipulate request metadata types.

### Regression focus

Specifically confirm:

- notification expression filtering still works for `skill_ver`, `skill_ver_num`, `agent_id`, and `email`
- feed-to-sort metainfo propagation remains intact after the bootstrap abstraction
- no service lost TTHeader/transmeta configuration during the migration

## Documentation Updates

### `CLAUDE.md`

Update the RPC-related guidance to describe:

- `pkg/reqinfo/` as the home for request metadata types
- `pkg/rpcx/` as the standard bootstrap path for Kitex setup
- default RPC timeout = 10 seconds
- local overrides are allowed only when clearly justified
- TTHeader/transmeta are part of the default RPC convention

### `docs/architecture_overview.md`

Add or update a short section describing:

- request metadata propagation through Hertz -> Kitex using metainfo + TTHeader
- the repository convention that Kitex clients and servers should be configured through shared bootstrap helpers rather than per-service handwritten option blocks

**Note on PM service**: The PM (Private Message) RPC service exists in the codebase but is not yet documented in `architecture_overview.md`. This refactor will update PM's bootstrap code but will not add full PM service documentation. PM documentation should be added separately when the service reaches production readiness.

## Trade-offs

### Benefits

- one place to find request metadata types
- one place to change RPC timeout or metadata propagation defaults
- lower risk of partial TTHeader rollout mistakes
- cleaner service entrypoints with less repeated bootstrapping code

### Costs

- small repo-wide import churn
- one more shared infrastructure package to maintain
- client option precedence must be kept obvious so overrides do not become confusing

## Recommended Implementation Order

1. Add `pkg/reqinfo/` and move metadata types/helpers
2. Replace imports and remove old request-metadata packages
3. Add `pkg/rpcx/` helpers
4. Replace client bootstrap in `api/main.go` and `rpc/feed/main.go`
5. Replace server bootstrap in every RPC `main.go`
6. Remove temporary debug code and duplicate option blocks
7. Run build and notify E2E tests
8. Update `CLAUDE.md` and `docs/architecture_overview.md`

## Acceptance Criteria

The refactor is complete when all of the following are true:

- no code imports `pkg/clientinfo` or `pkg/authinfo`
- API-to-RPC and RPC-to-RPC clients use the shared client option helper
- all RPC servers use the shared server option helper
- default timeout is 10 seconds in one central place
- notify E2E tests pass
- primary project docs describe the new convention accurately
