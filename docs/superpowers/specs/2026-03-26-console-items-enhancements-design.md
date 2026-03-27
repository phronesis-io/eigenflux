# Console Items Enhancements Design

## Overview

Three enhancements to the console items page and API:

1. **Agent detail popup** - Author Agent ID becomes a clickable link that opens a modal with agent details
2. **Exclude email suffix filter** - New filter to exclude items by author email suffix
3. **Inline status update** - Status column becomes a dropdown that updates the database on change

## Feature 1: Get Agent by ID & Agent Detail Popup

### Backend

**New endpoint**: `GET /console/api/v1/agents/:agent_id`

**IDL changes** (`console/console_api/idl/console.thrift`):
- Add `GetAgentReq` struct with `agent_id` path param
- Add `GetAgentResp` struct returning `ConsoleAgentInfo` in data
- Add `GetAgent` method to `ConsoleService`

**DAL changes** (`console/console_api/internal/dal/query.go`):
- Add `GetAgentByID(db *gorm.DB, agentID int64) (*AgentWithProfile, error)` - queries agents LEFT JOIN agent_profiles WHERE agent_id = ?

**Handler changes** (`console/console_api/handler_gen/eigenflux/console/console_service.go`):
- Add `GetAgent` handler: parse `agent_id` path param, call DAL, return agent info via `toConsoleAgentInfo`
- Add response types: `GetAgentData`, `GetAgentResp`

**Router changes**:
- Since hz auto-generates routes in `console.go` (DO NOT EDIT), we run `hz update` after IDL change. But per CLAUDE.md, hz regenerates handler stubs if it can't find them. Since we add the handler manually before running hz, the route should wire correctly.
- Alternatively, manually register `GET /:agent_id` in `console.go` alongside the existing `PUT /:agent_id` route - but this file is auto-generated. We will add the route manually in `middleware.go` or via a custom registration function that won't be overwritten.
- **Decision**: Add the route manually in `router_gen/eigenflux/console/console.go` alongside the existing `PUT /:agent_id`. Since hz update would regenerate this file, we note that running `hz update` later will need re-adding this route. Alternatively, we register it in `middleware.go` — but middleware files only return `[]app.HandlerFunc`, they don't register routes.
- **Final approach**: Add IDL definition, run `bash console/console_api/scripts/generate_api.sh` to let hz generate the route and model stub, then implement the handler logic.

### Frontend

**Items page** (`console/webapp/src/pages/items/list.tsx`):
- `author_agent_id` column render becomes `<a onClick>` that opens a modal
- Add state: `agentModalOpen`, `agentDetail`, `agentLoading`
- On click: `axios.get(/console/api/v1/agents/${agentId})` to fetch agent details
- Modal displays: agent_name, email, bio, profile_status, profile_keywords, created_at, updated_at

## Feature 2: Exclude Email Suffix Filter

### Backend

**IDL changes**: Add `optional string exclude_email_suffixes (api.query="exclude_email_suffixes")` to `ListItemsReq`

**DAL changes** (`ListItemsParams` + `ListItems`):
- Add `ExcludeEmailSuffixes []string` to `ListItemsParams`
- When non-empty, add WHERE clause: `raw_items.author_agent_id NOT IN (SELECT agent_id FROM agents WHERE email ILIKE '%@suffix1' OR email ILIKE '%@suffix2' ...)`
- Use parameterized queries to prevent SQL injection

**Handler changes**: Parse `exclude_email_suffixes` query param as comma-separated string, split into slice, pass to DAL

### Frontend

**Items page header**: Add `<Select mode="tags">` component
- Placeholder: "Exclude email suffixes"
- User types suffixes like `@test.com`, `@bot.ai` and presses Enter to add each
- On change, join values with comma and pass as `exclude_email_suffixes` filter
- Reset pagination to page 1 on filter change

## Feature 3: Inline Status Update via `PUT /console/api/v1/items/:item_id`

### Backend

**New endpoint**: `PUT /console/api/v1/items/:item_id`

**IDL changes** (`console/console_api/idl/console.thrift`):
- Add `UpdateItemReq` struct with `item_id` path param and optional fields (initially just `status`)
- Add `UpdateItemResp` struct returning updated `ConsoleItemInfo`
- Add `UpdateItem` method to `ConsoleService`

**DAL changes** (`console/console_api/internal/dal/query.go`):
- Add `UpdateItemParams` struct with `Status *int32` (extensible for future fields)
- Add `UpdateItem(db *gorm.DB, itemID int64, params UpdateItemParams) (*ItemWithProcessed, error)`:
  - Verify item exists in `raw_items`
  - Build GORM update map for non-nil fields
  - Update `processed_items` table (set `updated_at = now`)
  - Re-read and return full `ItemWithProcessed`
- Add `ErrItemNotFound` sentinel error

**Handler changes**:
- Add `updateItemReq` struct with `Status *int32 json:"status"` (pointer for partial update detection)
- Add `UpdateItemData`, `UpdateItemResp` response types
- Add `UpdateItem` handler: parse item_id, bind JSON body, validate at least one field, call DAL, return updated item
- Add `parseItemID` helper

### Frontend

**Items page** (`console/webapp/src/pages/items/list.tsx`):
- Replace static `<Tag>` with `<Select>` dropdown in the status column
- Options: all 5 status values with color-coded labels
- On change: `axios.put(/console/api/v1/items/${itemId}, { status: newValue })`
- Show loading state during update
- On success: update local data via refetch or optimistic update
- On error: show error message, revert to original value

## Data Flow

```
[Items Table] --click agent_id--> [GET /agents/:id] --> [Agent Detail Modal]
[Items Table] --change status--> [PUT /items/:id {status}] --> [Update processed_items] --> [Refetch list]
[Filter bar] --exclude suffixes--> [GET /items?exclude_email_suffixes=...] --> [SQL subquery NOT IN] --> [Filtered results]
```

## Files to Modify

### Backend (console_api)
1. `idl/console.thrift` - Add GetAgent, UpdateItem structs and service methods; add exclude_email_suffixes to ListItemsReq
2. Run `bash console/console_api/scripts/generate_api.sh` - Regenerate routes and models
3. `internal/dal/query.go` - Add GetAgentByID, UpdateItem functions; update ListItems for email suffix filter
4. `handler_gen/eigenflux/console/console_service.go` - Add GetAgent, UpdateItem handlers; update ListItems handler
5. `router_gen/eigenflux/console/middleware.go` - Add middleware stubs for new routes (if hz generates them)

### Frontend (webapp)
1. `src/pages/items/list.tsx` - All three UI changes (agent popup, email suffix filter, status dropdown)
2. `src/dataProvider.ts` - Update `getOne` to handle console API response format; update `update` to use PUT instead of PATCH

### Documentation
1. `CLAUDE.md` - Update Console API Endpoints table with new endpoints
