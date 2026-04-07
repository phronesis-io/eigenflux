# IDL & Database Workflow

## IDL Modification Workflow

**Important**: All IDL fields must be explicitly marked as `required` or `optional`, do not use default mode.

### RPC IDL (kitex)

```bash
# 1. Modify the relevant thrift file in idl/
# 2. Regenerate
export PATH=$PATH:$(go env GOPATH)/bin
kitex -module eigenflux_server idl/profile.thrift
kitex -module eigenflux_server idl/item.thrift
kitex -module eigenflux_server idl/sort.thrift
kitex -module eigenflux_server idl/feed.thrift
kitex -module eigenflux_server idl/auth.thrift
kitex -module eigenflux_server idl/notification.thrift
kitex -module eigenflux_server idl/pm.thrift
# 3. Update handler implementation
# 4. go build ./...
```

### HTTP API IDL (hz)

```bash
# 1. Modify idl/api.thrift
# 2. Regenerate
hz update -idl idl/api.thrift -module eigenflux_server
# 3. Update business logic in handler_gen
# 4. go build ./...
```

### Console API IDL (hz)

```bash
# Run from console/console_api/, NOT from root
# 1. Modify console/console_api/idl/console.thrift
# 2. Regenerate
cd console/console_api
bash scripts/generate_api.sh
# 3. Update business logic in handler_gen/eigenflux/console/console_service.go
# 4. go build .
```

## hz Tool Constraints

- Console IDL must only be generated from `console/console_api/` directory. Running `hz update` with console IDL from the project root will pollute `api/` with console code.
- hz requires all handler functions for a service to be in one file. The file name is derived from the IDL service name (e.g. `ConsoleService` -> `console_service.go`). If you split handlers across multiple files, hz will not find them and will regenerate empty stubs.
- Swagger annotations must use uppercase `@Router`, `@Summary`, `@Param`, `@Success` etc. hz generates lowercase `@router` which swag ignores. After hz generates new handler stubs, add proper swagger annotations manually with uppercase tags.

## Database Changes

- Database schema must be managed via versioned SQL (`migrations/`), service startup must not auto-modify schema
- Migration execution unified via scripts:
  1. `./scripts/common/migrate_up.sh`
  2. `./scripts/common/migrate_down.sh [version]`
  3. `./scripts/common/migrate_status.sh`
- `rpc/*/dal/db.go` responsible for code mapping, no longer serves as production DDL execution entry
