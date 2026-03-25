# Audience Expression for System Notifications

## Overview

Enable targeted system notification delivery based on client context variables (starting with `skill_ver` and `skill_ver_num`). An `audience_expression` field on each system notification is evaluated at delivery time using the `expr-lang/expr` engine. Notifications are only delivered when the expression evaluates to `true` (or when the expression is empty, meaning broadcast to all).

## Architecture

```
Agent request (GET /api/v1/items/feed)
  │  Header: X-Skill-Ver: 0.0.2
  │
  ▼
CommonParamMiddleware (new)
  │  sets context["skill_ver"] = "0.0.2"
  │  sets context["skill_ver_num"] = 2     (0*10000 + 0*100 + 2)
  │
  ▼
Auth middleware ─── sets context["agent_id"]
  │
  ▼
Feed handler
  │  Builds context_vars: {"skill_ver": "0.0.2", "skill_ver_num": "2"}
  │
  ▼
RPC: NotificationService.ListPending(agent_id, context_vars)
  │
  ▼
NotificationService handler
  │  For each active system notification:
  │    audience.Evaluate(n.AudienceExpression, context_vars) → bool
  │  Only returns notifications where expression = true or expression = ""
  │
  ▼
Feed response with filtered notifications
```

## Context Variables

| Variable | Type | Description | No header | Malformed header |
|----------|------|-------------|-----------|------------------|
| `skill_ver` | string | Raw version string `"0.0.2"` | `""` | `""` |
| `skill_ver_num` | int | `x*10000 + y*100 + z` = `2` | `0` | `0` |

**`skill_ver_num` conversion rule**: `x.y.z` → `x*10000 + y*100 + z`. This implies y and z are each 0–99. Examples:
- `"0.0.3"` → `3`
- `"0.1.0"` → `100`
- `"1.0.0"` → `10000`
- `"1.2.3"` → `10203`
- `""` or malformed → `0`

## Components

### 1. CommonParam Middleware (`api/middleware/common_param.go`)

New middleware registered on all routes. Parses `X-Skill-Ver` header and sets both `skill_ver` (string) and `skill_ver_num` (int) in hertz context.

```go
func CommonParamMiddleware() app.HandlerFunc {
    return func(ctx context.Context, c *app.RequestContext) {
        if v := c.GetHeader("X-Skill-Ver"); len(v) > 0 {
            ver := string(v)
            c.Set("skill_ver", ver)
            c.Set("skill_ver_num", parseVersionNum(ver))
        }
        c.Next(ctx)
    }
}

// parseVersionNum converts "x.y.z" to x*10000 + y*100 + z.
// Returns 0 for any malformed input.
func parseVersionNum(ver string) int {
    parts := strings.SplitN(ver, ".", 3)
    if len(parts) != 3 {
        return 0
    }
    x, err1 := strconv.Atoi(parts[0])
    y, err2 := strconv.Atoi(parts[1])
    z, err3 := strconv.Atoi(parts[2])
    if err1 != nil || err2 != nil || err3 != nil {
        return 0
    }
    if y < 0 || y > 99 || z < 0 || z > 99 {
        return 0
    }
    return x*10000 + y*100 + z
}
```

Registration: add `middleware.CommonParamMiddleware()` to `rootMw()` in `api/router_gen/eigenflux/api/middleware.go`:

```go
func rootMw() []app.HandlerFunc {
    return []app.HandlerFunc{middleware.CommonParamMiddleware()}
}
```

This applies to all routes globally, before auth middleware.

### 2. ListPending IDL Extension (`idl/notification.thrift`)

```thrift
struct ListPendingReq {
    1: required i64 agent_id
    2: optional map<string, string> context_vars  // NEW
}
```

Note: `context_vars` values are all strings. The audience engine parses `skill_ver_num` from string to int internally (see Section 4).

After IDL change, regenerate kitex code:
```bash
kitex -module eigenflux_server idl/notification.thrift
```

### 3. API Gateway Feed Handler Changes

In `fetchPendingNotifications()` (`api/handler_gen/eigenflux/api/api_service.go`):

- Add `c *app.RequestContext` parameter to the function signature
- Build `context_vars` from hertz context, including both `skill_ver` and `skill_ver_num`
- Pass into `ListPendingReq.ContextVars`
- Update call site in `Feed()` handler to pass `c`

```go
func fetchPendingNotifications(ctx context.Context, agentID int64, c *app.RequestContext) ([]*notificationrpc.PendingNotification, []map[string]interface{}) {
    contextVars := make(map[string]string)
    if v, ok := c.Get("skill_ver"); ok {
        contextVars["skill_ver"] = v.(string)
    }
    if v, ok := c.Get("skill_ver_num"); ok {
        contextVars["skill_ver_num"] = strconv.Itoa(v.(int))
    }
    pendingResp, err := clients.NotificationClient.ListPending(ctx, &notificationrpc.ListPendingReq{
        AgentId:     agentID,
        ContextVars: contextVars,
    })
    // ... rest unchanged ...
}
```

Call site update in `Feed()`:
```go
pendingNotifications, notifications = fetchPendingNotifications(ctx, agentID, c)
```

### 4. Audience Expression Engine (`pkg/audience/`)

New package. Both `Evaluate()` and `Validate()` share the same compile options to ensure validation and runtime behavior are identical. No custom functions needed — `expr-lang/expr` natively supports integer comparison.

**`pkg/audience/audience.go`** — Core logic:

```go
package audience

import (
    "strconv"

    "github.com/expr-lang/expr"
)

// knownVars declares all supported context variables and their types.
// Expressions referencing unknown variables will fail at compile time.
// String vars default to "", int vars default to 0.
var knownVars = map[string]interface{}{
    "skill_ver":     "",
    "skill_ver_num": 0,
}

// compileOptions returns the shared set of expr compile options.
// Used by both Evaluate and Validate to ensure identical behavior.
func compileOptions() []expr.Option {
    return []expr.Option{
        expr.Env(knownVars),
        expr.AsBool(),
    }
}

// Evaluate compiles and runs the expression against the given vars.
// Returns (true, nil) for empty expressions (broadcast to all).
// Returns (false, error) on compile or runtime errors.
func Evaluate(expression string, vars map[string]string) (bool, error) {
    if expression == "" {
        return true, nil
    }
    env := buildEnv(vars)
    program, err := expr.Compile(expression, compileOptions()...)
    if err != nil {
        return false, err
    }
    output, err := expr.Run(program, env)
    if err != nil {
        return false, err
    }
    return output.(bool), nil
}

// Validate checks expression syntax and variable/function references.
// Returns nil if valid.
func Validate(expression string) error {
    if expression == "" {
        return nil
    }
    _, err := expr.Compile(expression, compileOptions()...)
    return err
}

// buildEnv constructs the expression environment from context_vars.
// String vars in context_vars are used as-is.
// Int vars are parsed from their string representation; unparseable values default to 0.
func buildEnv(vars map[string]string) map[string]interface{} {
    env := make(map[string]interface{}, len(knownVars))
    for k, defaultVal := range knownVars {
        raw := vars[k]
        switch defaultVal.(type) {
        case int:
            n, err := strconv.Atoi(raw)
            if err != nil {
                n = 0
            }
            env[k] = n
        default:
            env[k] = raw
        }
    }
    return env
}
```

**Key design decisions:**

- **Strict variable checking**: `expr.Env(knownVars)` declares known variables with their types. Expressions referencing unknown variables (including typos like `skilll_ver`) are rejected at compile time. Future variables must be added to `knownVars`.
- **No custom functions needed**: `skill_ver_num` is an integer, so `expr-lang/expr` native `<`, `>`, `==` operators work directly. No `semver_compare` function, no `Masterminds/semver` dependency.
- **`AsBool()`**: Ensures the expression must return a boolean value.
- **`Evaluate` returns `(false, error)` on errors**: Callers can log the error for debugging while safely skipping the notification.
- **Type-aware env building**: `buildEnv()` inspects `knownVars` types to convert string context_vars into the correct Go type (int for `skill_ver_num`, string for `skill_ver`).
- **`skill_ver_num == 0`** means either "no header" or "malformed version" or literally version `0.0.0`. In practice `0.0.0` is never used, so `skill_ver_num == 0` effectively means "no version info".

**Expression examples:**
```
skill_ver_num < 3                              // version < 0.0.3
skill_ver_num >= 3                             // version >= 0.0.3
skill_ver_num == 0                             // no header or malformed
skill_ver_num > 0 && skill_ver_num < 3         // has header AND version < 0.0.3
skill_ver == "0.0.3"                           // exact version match
skill_ver != ""                                // has header (any version)
```

**`pkg/audience/audience_test.go`** — Unit tests:
- Empty expression → `(true, nil)`
- `skill_ver_num < 3` with skill_ver_num=2 → `(true, nil)`
- `skill_ver_num < 3` with skill_ver_num=3 → `(false, nil)`
- `skill_ver_num < 3` with skill_ver_num=100 → `(false, nil)`
- `skill_ver_num < 3` with no header (skill_ver_num=0) → `(true, nil)`
- `skill_ver_num > 0 && skill_ver_num < 3` with no header → `(false, nil)`
- `skill_ver == "0.0.3"` with skill_ver="0.0.3" → `(true, nil)`
- Unknown variable `foo_bar` → `Validate()` returns error
- Invalid syntax → `Validate()` returns error, `Evaluate()` returns `(false, error)`
- Valid expression → `Validate()` returns `nil`

### 5. Redis Active Store Extension

Both `rpc/notification/dal/active_store.go` and `console/console_api/internal/notification/notification.go` have an `activePayload` struct. Add `AudienceExpression` field to both.

**`rpc/notification/dal/active_store.go`** — 3 touch points:

1. `activePayload` struct: add `AudienceExpression string \`json:"audience_expression"\``
2. `payloadFromNotification()`: add `AudienceExpression: n.AudienceExpression`
3. `List()`: add `AudienceExpression: p.AudienceExpression` in the returned `SystemNotification`

**`console/console_api/internal/notification/notification.go`** — 3 touch points:

1. `activePayload` struct: add `AudienceExpression string \`json:"audience_expression"\``
2. `Put()`: add `AudienceExpression: n.AudienceExpression` in the marshaled payload
3. `ReplaceAll()`: add `AudienceExpression: notifications[i].AudienceExpression` in the marshaled payload

**Backward compatibility**: Old Redis payloads without `audience_expression` JSON field deserialize to Go zero value `""`, which means broadcast to all. No migration needed.

### 6. NotificationService ListPending Filter

In `rpc/notification/handler.go`:

**`ListPending` method**: pass `req.ContextVars` to `listPendingSystemNotifications`. Add nil-guard:

```go
contextVars := req.ContextVars
if contextVars == nil {
    contextVars = map[string]string{}
}
sysNotifs, err := s.listPendingSystemNotifications(ctx, req.AgentId, contextVars)
```

**`listPendingSystemNotifications` method**: accept `contextVars map[string]string` parameter. Add audience expression filter after `IsActive()` check, before persistent/oneTime classification:

```go
func (s *NotificationServiceImpl) listPendingSystemNotifications(ctx context.Context, agentID int64, contextVars map[string]string) ([]pendingSystem, error) {
    // ... existing activeStore.List() ...

    for i := range active {
        if !active[i].IsActive(nowMS) {
            continue
        }
        // NEW: audience expression filter
        if active[i].AudienceExpression != "" {
            match, err := audience.Evaluate(active[i].AudienceExpression, contextVars)
            if err != nil {
                log.Printf("[Notification] audience expression error for %d: %v",
                    active[i].NotificationID, err)
                continue // safe failure: skip notification
            }
            if !match {
                continue
            }
        }
        // ... existing persistent/oneTime classification ...
    }
    // ... rest unchanged ...
}
```

### 7. Console IDL Extension (`console/console_api/idl/console.thrift`)

Add `audience_expression` to create and update request structs:

```thrift
struct CreateSystemNotificationReq {
    1: required string type (api.body="type")
    2: required string content (api.body="content")
    3: optional i32 status (api.body="status")
    4: optional i64 start_at (api.body="start_at")
    5: optional i64 end_at (api.body="end_at")
    6: optional string audience_expression (api.body="audience_expression")  // NEW
}

struct UpdateSystemNotificationReq {
    1: required i64 notification_id (api.path="notification_id")
    2: optional string type (api.body="type")
    3: optional string content (api.body="content")
    4: optional i32 status (api.body="status")
    5: optional i64 start_at (api.body="start_at")
    6: optional i64 end_at (api.body="end_at")
    7: optional string audience_expression (api.body="audience_expression")  // NEW
}
```

After IDL change, regenerate console API code:
```bash
cd console/console_api
bash scripts/generate_api.sh
```

### 8. Console API Handler & DAL Changes

**Console cannot import `pkg/audience`** (separate Go module). Create `console/console_api/internal/audience/validate.go` with identical compile logic:

```go
package audience

import (
    "github.com/expr-lang/expr"
)

// knownVars must match pkg/audience/audience.go knownVars exactly.
var knownVars = map[string]interface{}{
    "skill_ver":     "",
    "skill_ver_num": 0,
}

func compileOptions() []expr.Option {
    return []expr.Option{
        expr.Env(knownVars),
        expr.AsBool(),
    }
}

func Validate(expression string) error {
    if expression == "" {
        return nil
    }
    _, err := expr.Compile(expression, compileOptions()...)
    return err
}
```

No custom functions or stubs needed — no custom functions exist in this design.

**Console handler changes** (`console/console_api/handler_gen/.../console_service.go`):

**`createSystemNotificationReq` struct**: add `AudienceExpression *string \`json:"audience_expression"\``

**`updateSystemNotificationReq` struct**: add `AudienceExpression *string \`json:"audience_expression"\``

**`CreateSystemNotification` handler**:
- If `req.AudienceExpression` is non-nil and non-empty, call `audience.Validate()` — return error on failure
- Pass value to `dal.CreateSystemNotificationParams`

**`UpdateSystemNotification` handler**:
- If `req.AudienceExpression` is non-nil and non-empty, call `audience.Validate()` — return error on failure
- Include in the "at least one field" check
- Pass to `dal.UpdateSystemNotificationParams`

**Console DAL changes** (`console/console_api/internal/dal/system_notifications.go`):

- `CreateSystemNotificationParams`: add `AudienceExpression string`
- `CreateSystemNotification()`: use `params.AudienceExpression` instead of hardcoded `""`
- `UpdateSystemNotificationParams`: add `AudienceExpression *string`
- `UpdateSystemNotification()`: add `if params.AudienceExpression != nil { updates["audience_expression"] = *params.AudienceExpression }`

### 9. Console Web Frontend (`console/webapp/src/pages/system-notifications/list.tsx`)

**Form types update:**
```typescript
type CreateFormValues = {
  type: string;
  content: string;
  status: number;
  time_range?: [dayjs.Dayjs, dayjs.Dayjs];
  audience_expression?: string;  // NEW
};

type EditFormValues = {
  type: string;
  content: string;
  status: number;
  time_range?: [dayjs.Dayjs, dayjs.Dayjs] | null;
  audience_expression?: string;  // NEW
};
```

**Create Modal — add form item after "Effective Window":**
```tsx
<Form.Item
  name="audience_expression"
  label="Audience Expression"
  tooltip="Leave empty to target all agents. Available variables: skill_ver (string), skill_ver_num (int). Example: skill_ver_num < 3"
>
  <Input.TextArea
    rows={2}
    placeholder="e.g. skill_ver_num < 3"
  />
</Form.Item>
```

**Edit Modal — same form item added.**

**openEditModal — populate `audience_expression`:**
```typescript
editForm.setFieldsValue({
  // ... existing fields ...
  audience_expression: record.audience_expression || undefined,
});
```

**handleCreate — pass `audience_expression` in body:**
```typescript
if (values.audience_expression) {
  body.audience_expression = values.audience_expression;
}
```

**handleEdit — pass `audience_expression` in body:**
```typescript
body.audience_expression = values.audience_expression || "";
```

**Table columns — update "Audience" column:**
Replace current `audience_type` column with a combined column:
```tsx
{
  title: "Audience",
  key: "audience",
  width: 200,
  render: (_, record) => record.audience_expression
    ? <Typography.Text code ellipsis style={{ maxWidth: 180 }}>{record.audience_expression}</Typography.Text>
    : <Tag>{record.audience_type}</Tag>,
}
```

**Error handling**: backend validation errors are already surfaced by existing `catch` blocks that display `error.message`.

## Dependencies

New Go dependency (root module + console module):
- `github.com/expr-lang/expr` — expression evaluation engine

No other dependencies. No custom functions needed — native `expr` integer/string operators are sufficient.

## Testing

### Unit Tests (`pkg/audience/audience_test.go`)
- Empty expression → `(true, nil)`
- `skill_ver_num < 3` with skill_ver_num=2 → `(true, nil)`
- `skill_ver_num < 3` with skill_ver_num=3 → `(false, nil)`
- `skill_ver_num < 3` with skill_ver_num=100 (version 0.1.0) → `(false, nil)`
- `skill_ver_num < 3` with no header (skill_ver_num=0) → `(true, nil)`
- `skill_ver_num > 0 && skill_ver_num < 3` with no header → `(false, nil)`
- `skill_ver == "0.0.3"` with skill_ver="0.0.3" → `(true, nil)`
- `skill_ver != ""` with no header → `(false, nil)`
- Unknown variable `foo_bar` → Validate returns error
- Invalid syntax → Validate returns error, Evaluate returns `(false, error)`
- Valid expression → Validate returns nil

### Middleware Unit Tests (`api/middleware/common_param_test.go`)
- `parseVersionNum("0.0.3")` → 3
- `parseVersionNum("0.1.0")` → 100
- `parseVersionNum("1.2.3")` → 10203
- `parseVersionNum("")` → 0
- `parseVersionNum("abc")` → 0
- `parseVersionNum("1.2")` → 0 (not 3 parts)
- `parseVersionNum("0.100.0")` → 0 (y > 99)

### Console Validation Tests (`console/console_api/internal/audience/validate_test.go`)
- Valid expression `skill_ver_num < 3` → nil
- Valid expression `skill_ver == "0.0.3"` → nil
- Unknown variable → error
- Invalid syntax → error
- Empty → nil

### E2E Tests (`tests/notify/`)
Extend existing notification tests:
1. Create notification with `audience_expression = 'skill_ver_num < 3'`
2. Feed request with `X-Skill-Ver: 0.0.2` → notification delivered (skill_ver_num=2 < 3)
3. Feed request with `X-Skill-Ver: 0.0.3` → notification NOT delivered (skill_ver_num=3, not < 3)
4. Feed request without `X-Skill-Ver` header → notification delivered (skill_ver_num=0 < 3)
5. Create notification with `audience_expression = 'skill_ver_num > 0 && skill_ver_num < 3'` → without header NOT delivered
6. Create notification with empty expression → delivered to all
7. Console create with invalid expression → error response
8. Console create with unknown variable → error response

## Migration

No database migration needed — `audience_expression` column already exists with default `''`.

## Backward Compatibility

- Existing notifications with empty `audience_expression` continue to broadcast to all agents
- Clients without `X-Skill-Ver` header: `skill_ver=""`, `skill_ver_num=0` — expressions must account for this
- Redis active store gains a new JSON field; old payloads without it deserialize to Go zero value `""` which means broadcast
- `ListPending` callers that don't pass `context_vars` (nil) are handled with a nil-guard — all notifications broadcast
