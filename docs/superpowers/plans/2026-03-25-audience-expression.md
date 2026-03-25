# Audience Expression Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enable targeted system notification delivery via `audience_expression` evaluated with `expr-lang/expr`, using context variables (`skill_ver`, `skill_ver_num`, `agent_id`) extracted from request headers and auth context.

**Architecture:** A new `CommonParamMiddleware` extracts `X-Skill-Ver` into hertz context. The feed handler builds a `context_vars` map and passes it via an extended `ListPendingReq` to the notification service. The notification service evaluates each notification's `audience_expression` against the vars using `pkg/audience`. Console API validates expressions before saving. Console webapp adds an expression input field.

**Tech Stack:** Go 1.25, expr-lang/expr, Hertz, Kitex, React (Ant Design), PostgreSQL, Redis

**Spec:** `docs/superpowers/specs/2026-03-24-audience-expression-design.md`

---

## File Map

| Action | Path | Responsibility |
|--------|------|----------------|
| Create | `pkg/audience/audience.go` | Expression compile/evaluate/validate + buildEnv |
| Create | `pkg/audience/audience_test.go` | Unit tests for audience engine |
| Create | `api/middleware/common_param.go` | Parse X-Skill-Ver → skill_ver + skill_ver_num |
| Create | `api/middleware/common_param_test.go` | Unit tests for parseVersionNum |
| Modify | `api/router_gen/eigenflux/api/middleware.go:11-14` | Register CommonParamMiddleware in rootMw() |
| Modify | `idl/notification.thrift:18-20` | Add context_vars to ListPendingReq |
| Regen  | `kitex_gen/eigenflux/notification/` | Regenerate after IDL change |
| Modify | `api/handler_gen/eigenflux/api/api_service.go:44-69,565` | Pass context_vars in fetchPendingNotifications |
| Modify | `rpc/notification/dal/active_store.go:22-32,34-46,60-70` | Add AudienceExpression to activePayload |
| Modify | `rpc/notification/handler.go:32-95,157-227` | Filter by audience expression in ListPending |
| Create | `console/console_api/internal/audience/validate.go` | Console-side expression validation |
| Create | `console/console_api/internal/audience/validate_test.go` | Unit tests for console validation |
| Modify | `console/console_api/internal/notification/notification.go:19-29,40-56,62-84` | Add AudienceExpression to console activePayload |
| Modify | `console/console_api/idl/console.thrift:212-227` | Add audience_expression to Create/Update req |
| Regen  | `console/console_api/model/` | Regenerate after console IDL change |
| Modify | `console/console_api/handler_gen/eigenflux/console/console_service.go:163-177,564-667` | Handle audience_expression in CRUD |
| Modify | `console/console_api/internal/dal/system_notifications.go:42-77` | Add AudienceExpression to params |
| Modify | `console/webapp/src/pages/system-notifications/list.tsx` | Add expression field to forms and table |
| Create | `tests/notify/audience_expr_test.go` | E2E tests for audience expression filtering |

---

### Task 1: Audience Expression Engine (`pkg/audience/`)

**Files:**
- Create: `pkg/audience/audience.go`
- Create: `pkg/audience/audience_test.go`

- [ ] **Step 1: Add expr-lang/expr dependency**

```bash
go get github.com/expr-lang/expr
```

- [ ] **Step 2: Write failing tests**

Create `pkg/audience/audience_test.go`:

```go
package audience

import (
	"testing"
)

func TestEvaluate_EmptyExpression(t *testing.T) {
	ok, err := Evaluate("", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("empty expression should return true")
	}
}

func TestEvaluate_SkillVerNum(t *testing.T) {
	tests := []struct {
		name string
		expr string
		vars map[string]string
		want bool
	}{
		{"less than match", "skill_ver_num < 3", map[string]string{"skill_ver_num": "2"}, true},
		{"less than no match", "skill_ver_num < 3", map[string]string{"skill_ver_num": "3"}, false},
		{"less than large", "skill_ver_num < 3", map[string]string{"skill_ver_num": "100"}, false},
		{"no header defaults 0", "skill_ver_num < 3", map[string]string{}, true},
		{"compound no header", "skill_ver_num > 0 && skill_ver_num < 3", map[string]string{}, false},
		{"gte match", "skill_ver_num >= 3", map[string]string{"skill_ver_num": "3"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Evaluate(tt.expr, tt.vars)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("Evaluate(%q) = %v, want %v", tt.expr, got, tt.want)
			}
		})
	}
}

func TestEvaluate_SkillVerString(t *testing.T) {
	ok, err := Evaluate(`skill_ver == "0.0.3"`, map[string]string{"skill_ver": "0.0.3"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected true")
	}
}

func TestEvaluate_AgentID(t *testing.T) {
	tests := []struct {
		name string
		expr string
		vars map[string]string
		want bool
	}{
		{"match", "agent_id == 123", map[string]string{"agent_id": "123"}, true},
		{"no match", "agent_id == 123", map[string]string{"agent_id": "456"}, false},
		{"missing defaults 0", "agent_id == 0", map[string]string{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Evaluate(tt.expr, tt.vars)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("Evaluate(%q) = %v, want %v", tt.expr, got, tt.want)
			}
		})
	}
}

func TestEvaluate_InvalidExpression(t *testing.T) {
	ok, err := Evaluate("invalid !!!", map[string]string{})
	if err == nil {
		t.Fatal("expected error for invalid expression")
	}
	if ok {
		t.Fatal("expected false for invalid expression")
	}
}

func TestValidate_Valid(t *testing.T) {
	for _, expr := range []string{"", "skill_ver_num < 3", `skill_ver == "0.0.3"`, "agent_id == 123"} {
		if err := Validate(expr); err != nil {
			t.Fatalf("Validate(%q) unexpected error: %v", expr, err)
		}
	}
}

func TestValidate_UnknownVariable(t *testing.T) {
	if err := Validate("foo_bar == 1"); err == nil {
		t.Fatal("expected error for unknown variable")
	}
}

func TestValidate_InvalidSyntax(t *testing.T) {
	if err := Validate("skill_ver_num <><> 3"); err == nil {
		t.Fatal("expected error for invalid syntax")
	}
}

func TestEvaluate_SkillVerNoHeader(t *testing.T) {
	ok, err := Evaluate(`skill_ver != ""`, map[string]string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected false for empty skill_ver")
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test -v ./pkg/audience/`
Expected: FAIL (package does not exist yet)

- [ ] **Step 4: Write implementation**

Create `pkg/audience/audience.go`:

```go
package audience

import (
	"strconv"

	"github.com/expr-lang/expr"
)

var knownVars = map[string]interface{}{
	"skill_ver":     "",
	"skill_ver_num": 0,
	"agent_id":      int64(0),
}

func compileOptions() []expr.Option {
	return []expr.Option{
		expr.Env(knownVars),
		expr.AsBool(),
	}
}

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

func Validate(expression string) error {
	if expression == "" {
		return nil
	}
	_, err := expr.Compile(expression, compileOptions()...)
	return err
}

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
		case int64:
			n, err := strconv.ParseInt(raw, 10, 64)
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

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test -v ./pkg/audience/`
Expected: PASS (all tests)

- [ ] **Step 6: Commit**

```bash
git add pkg/audience/ go.mod go.sum
git commit -m "feat: add pkg/audience expression engine with expr-lang/expr"
```

---

<!-- PLAN_CONTINUATION -->

### Task 2: CommonParam Middleware

**Files:**
- Create: `api/middleware/common_param.go`
- Create: `api/middleware/common_param_test.go`
- Modify: `api/router_gen/eigenflux/api/middleware.go:11-14`

- [ ] **Step 1: Write failing tests**

Create `api/middleware/common_param_test.go`:

```go
package middleware

import "testing"

func TestParseVersionNum(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"0.0.3", 3},
		{"0.1.0", 100},
		{"1.0.0", 10000},
		{"1.2.3", 10203},
		{"", 0},
		{"abc", 0},
		{"1.2", 0},
		{"0.100.0", 0},
		{"0.0.100", 0},
		{"0.-1.0", 0},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := parseVersionNum(tt.input)
			if got != tt.want {
				t.Fatalf("parseVersionNum(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -v ./api/middleware/ -run TestParseVersionNum`
Expected: FAIL (function not defined)

- [ ] **Step 3: Write implementation**

Create `api/middleware/common_param.go`:

```go
package middleware

import (
	"context"
	"strconv"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
)

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

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -v ./api/middleware/ -run TestParseVersionNum`
Expected: PASS

- [ ] **Step 5: Register middleware in rootMw()**

Edit `api/router_gen/eigenflux/api/middleware.go:11-14`. Change:

```go
func rootMw() []app.HandlerFunc {
	// your code...
	return nil
}
```

To:

```go
func rootMw() []app.HandlerFunc {
	return []app.HandlerFunc{middleware.CommonParamMiddleware()}
}
```

- [ ] **Step 6: Build check**

Run: `go build ./api/...`
Expected: success

- [ ] **Step 7: Commit**

```bash
git add api/middleware/common_param.go api/middleware/common_param_test.go api/router_gen/eigenflux/api/middleware.go
git commit -m "feat: add CommonParamMiddleware for X-Skill-Ver parsing"
```

---

### Task 3: Notification IDL + Kitex Regen

**Files:**
- Modify: `idl/notification.thrift:18-20`
- Regen: `kitex_gen/eigenflux/notification/`

- [ ] **Step 1: Update IDL**

Edit `idl/notification.thrift`. Change `ListPendingReq`:

```thrift
struct ListPendingReq {
    1: required i64 agent_id
    2: optional map<string, string> context_vars
}
```

- [ ] **Step 2: Regenerate kitex code**

```bash
export PATH=$PATH:$(go env GOPATH)/bin
kitex -module eigenflux_server idl/notification.thrift
```

- [ ] **Step 3: Build check**

Run: `go build ./...`
Expected: success (existing callers pass nil for optional field)

- [ ] **Step 4: Commit**

```bash
git add idl/notification.thrift kitex_gen/
git commit -m "feat: add context_vars to ListPendingReq IDL"
```

---

### Task 4: Redis Active Store — Add AudienceExpression

**Files:**
- Modify: `rpc/notification/dal/active_store.go:22-32,34-46,60-70`

- [ ] **Step 1: Add AudienceExpression to activePayload struct** (line 22-32)

Add after `CreatedAt` field:

```go
AudienceExpression string `json:"audience_expression"`
```

- [ ] **Step 2: Update payloadFromNotification()** (line 34-46)

Add to the returned struct literal:

```go
AudienceExpression: n.AudienceExpression,
```

- [ ] **Step 3: Update List() deserialization** (line 60-70)

Add to the `SystemNotification` struct literal in the loop:

```go
AudienceExpression: p.AudienceExpression,
```

- [ ] **Step 4: Build check**

Run: `go build ./rpc/notification/...`
Expected: success

- [ ] **Step 5: Commit**

```bash
git add rpc/notification/dal/active_store.go
git commit -m "feat: add AudienceExpression to notification active store payload"
```

---

### Task 5: NotificationService — Filter by Audience Expression

**Files:**
- Modify: `rpc/notification/handler.go:32-95,157-227`

- [ ] **Step 1: Update ListPending to pass context_vars** (line 63-64)

Change:

```go
sysNotifs, err := s.listPendingSystemNotifications(ctx, req.AgentId)
```

To:

```go
contextVars := req.ContextVars
if contextVars == nil {
    contextVars = map[string]string{}
}
sysNotifs, err := s.listPendingSystemNotifications(ctx, req.AgentId, contextVars)
```

- [ ] **Step 2: Update listPendingSystemNotifications signature** (line 157)

Change:

```go
func (s *NotificationServiceImpl) listPendingSystemNotifications(ctx context.Context, agentID int64) ([]pendingSystem, error) {
```

To:

```go
func (s *NotificationServiceImpl) listPendingSystemNotifications(ctx context.Context, agentID int64, contextVars map[string]string) ([]pendingSystem, error) {
```

- [ ] **Step 3: Add audience expression filter in the loop** (after `IsActive` check, line ~171-173)

After `if !active[i].IsActive(nowMS) { continue }`, add:

```go
if active[i].AudienceExpression != "" {
    match, err := audience.Evaluate(active[i].AudienceExpression, contextVars)
    if err != nil {
        log.Printf("[Notification] audience expression error for %d: %v", active[i].NotificationID, err)
        continue
    }
    if !match {
        continue
    }
}
```

Add import: `"eigenflux_server/pkg/audience"`

- [ ] **Step 4: Build check**

Run: `go build ./rpc/notification/...`
Expected: success

- [ ] **Step 5: Commit**

```bash
git add rpc/notification/handler.go
git commit -m "feat: filter system notifications by audience expression in ListPending"
```

---

### Task 6: API Gateway — Pass context_vars to ListPending

**Files:**
- Modify: `api/handler_gen/eigenflux/api/api_service.go:44-69,565`

- [ ] **Step 1: Update fetchPendingNotifications signature and body** (line 44-69)

Change:

```go
func fetchPendingNotifications(ctx context.Context, agentID int64) ([]*notificationrpc.PendingNotification, []map[string]interface{}) {
	pendingResp, err := clients.NotificationClient.ListPending(ctx, &notificationrpc.ListPendingReq{
		AgentId: agentID,
	})
```

To:

```go
func fetchPendingNotifications(ctx context.Context, agentID int64, c *app.RequestContext) ([]*notificationrpc.PendingNotification, []map[string]interface{}) {
	contextVars := make(map[string]string)
	if v, ok := c.Get("skill_ver"); ok {
		contextVars["skill_ver"] = v.(string)
	}
	if v, ok := c.Get("skill_ver_num"); ok {
		contextVars["skill_ver_num"] = strconv.Itoa(v.(int))
	}
	contextVars["agent_id"] = strconv.FormatInt(agentID, 10)
	pendingResp, err := clients.NotificationClient.ListPending(ctx, &notificationrpc.ListPendingReq{
		AgentId:     agentID,
		ContextVars: contextVars,
	})
```

- [ ] **Step 2: Update call site in Feed()** (line 565)

Change:

```go
pendingNotifications, notifications = fetchPendingNotifications(ctx, agentID)
```

To:

```go
pendingNotifications, notifications = fetchPendingNotifications(ctx, agentID, c)
```

- [ ] **Step 3: Build check**

Run: `go build ./api/...`
Expected: success

- [ ] **Step 4: Commit**

```bash
git add api/handler_gen/eigenflux/api/api_service.go
git commit -m "feat: pass context_vars (skill_ver, skill_ver_num, agent_id) to ListPending"
```

---

<!-- PLAN_CONTINUATION_2 -->

### Task 7: Console — Active Store AudienceExpression

**Files:**
- Modify: `console/console_api/internal/notification/notification.go:19-29,40-56,62-84`

- [ ] **Step 1: Add AudienceExpression to activePayload struct** (line 19-29)

Add after `CreatedAt` field:

```go
AudienceExpression string `json:"audience_expression"`
```

- [ ] **Step 2: Update Put() method** (line 40-56)

Add to the `activePayload` literal inside `json.Marshal`:

```go
AudienceExpression: n.AudienceExpression,
```

- [ ] **Step 3: Update ReplaceAll() method** (line 62-84)

Add to the `activePayload` literal inside the loop:

```go
AudienceExpression: notifications[i].AudienceExpression,
```

- [ ] **Step 4: Build check**

Run: `cd console/console_api && go build .`
Expected: success

- [ ] **Step 5: Commit**

```bash
git add console/console_api/internal/notification/notification.go
git commit -m "feat: add AudienceExpression to console active store payload"
```

---

### Task 8: Console — Audience Validation Package

**Files:**
- Create: `console/console_api/internal/audience/validate.go`
- Create: `console/console_api/internal/audience/validate_test.go`

- [ ] **Step 1: Add expr-lang/expr to console module**

```bash
cd console/console_api && go get github.com/expr-lang/expr
```

- [ ] **Step 2: Write failing tests**

Create `console/console_api/internal/audience/validate_test.go`:

```go
package audience

import "testing"

func TestValidate_Valid(t *testing.T) {
	for _, expr := range []string{"", "skill_ver_num < 3", `skill_ver == "0.0.3"`, "agent_id == 123"} {
		if err := Validate(expr); err != nil {
			t.Fatalf("Validate(%q) unexpected error: %v", expr, err)
		}
	}
}

func TestValidate_UnknownVariable(t *testing.T) {
	if err := Validate("foo_bar == 1"); err == nil {
		t.Fatal("expected error for unknown variable")
	}
}

func TestValidate_InvalidSyntax(t *testing.T) {
	if err := Validate("skill_ver_num <><> 3"); err == nil {
		t.Fatal("expected error for invalid syntax")
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd console/console_api && go test -v ./internal/audience/`
Expected: FAIL

- [ ] **Step 4: Write implementation**

Create `console/console_api/internal/audience/validate.go`:

```go
package audience

import (
	"github.com/expr-lang/expr"
)

var knownVars = map[string]interface{}{
	"skill_ver":     "",
	"skill_ver_num": 0,
	"agent_id":      int64(0),
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

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd console/console_api && go test -v ./internal/audience/`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add console/console_api/internal/audience/ console/console_api/go.mod console/console_api/go.sum
git commit -m "feat: add console audience expression validation package"
```

---

### Task 9: Console IDL + Codegen

**Files:**
- Modify: `console/console_api/idl/console.thrift:212-227`
- Regen: `console/console_api/model/`

- [ ] **Step 1: Update CreateSystemNotificationReq** (line 212-218)

Add field 6:

```thrift
    6: optional string audience_expression (api.body="audience_expression")
```

- [ ] **Step 2: Update UpdateSystemNotificationReq** (line 220-227)

Add field 7:

```thrift
    7: optional string audience_expression (api.body="audience_expression")
```

- [ ] **Step 3: Regenerate console API code**

```bash
cd console/console_api
bash scripts/generate_api.sh
```

- [ ] **Step 4: Build check**

Run: `cd console/console_api && go build .`
Expected: success

- [ ] **Step 5: Commit**

```bash
git add console/console_api/idl/console.thrift console/console_api/model/ console/console_api/router_gen/
git commit -m "feat: add audience_expression to console system notification IDL"
```

---

### Task 10: Console DAL — AudienceExpression in Params

**Files:**
- Modify: `console/console_api/internal/dal/system_notifications.go:42-77`

- [ ] **Step 1: Update CreateSystemNotificationParams** (line 42-49)

Add field:

```go
AudienceExpression string
```

- [ ] **Step 2: Update CreateSystemNotification()** (line 58-59)

Change:

```go
AudienceExpression: "",
```

To:

```go
AudienceExpression: params.AudienceExpression,
```

- [ ] **Step 3: Update UpdateSystemNotificationParams** (line 71-77)

Add field:

```go
AudienceExpression *string
```

- [ ] **Step 4: Update UpdateSystemNotification()** (line 88-105)

Add after the `EndAt` block:

```go
if params.AudienceExpression != nil {
    updates["audience_expression"] = *params.AudienceExpression
}
```

- [ ] **Step 5: Build check**

Run: `cd console/console_api && go build .`
Expected: success

- [ ] **Step 6: Commit**

```bash
git add console/console_api/internal/dal/system_notifications.go
git commit -m "feat: add AudienceExpression to console DAL create/update params"
```

---

### Task 11: Console Handler — Audience Expression CRUD

**Files:**
- Modify: `console/console_api/handler_gen/eigenflux/console/console_service.go:163-177,564-667`

- [ ] **Step 1: Update request structs** (line 163-177)

Add to `createSystemNotificationReq`:

```go
AudienceExpression *string `json:"audience_expression"`
```

Add to `updateSystemNotificationReq`:

```go
AudienceExpression *string `json:"audience_expression"`
```

- [ ] **Step 2: Update CreateSystemNotification handler** (line 564-626)

After the `end_at > start_at` validation (line ~581), add:

```go
var audienceExpr string
if req.AudienceExpression != nil && strings.TrimSpace(*req.AudienceExpression) != "" {
    audienceExpr = strings.TrimSpace(*req.AudienceExpression)
    if err := audience.Validate(audienceExpr); err != nil {
        writeConsoleError(c, "invalid audience_expression: "+err.Error())
        return
    }
}
```

Update the `dal.CreateSystemNotificationParams` call (line ~605-608) to include:

```go
AudienceExpression: audienceExpr,
```

Add import: `"console.eigenflux.ai/internal/audience"`

- [ ] **Step 3: Update UpdateSystemNotification handler** (line 637-667)

Update the "at least one field" check (line ~648) to include `req.AudienceExpression`:

```go
if req.Type == nil && req.Content == nil && req.Status == nil && req.StartAt == nil && req.EndAt == nil && req.AudienceExpression == nil {
```

After the check, add validation:

```go
if req.AudienceExpression != nil && strings.TrimSpace(*req.AudienceExpression) != "" {
    if err := audience.Validate(strings.TrimSpace(*req.AudienceExpression)); err != nil {
        writeConsoleError(c, "invalid audience_expression: "+err.Error())
        return
    }
}
```

Update the `dal.UpdateSystemNotificationParams` call to include:

```go
AudienceExpression: req.AudienceExpression,
```

- [ ] **Step 4: Build check**

Run: `cd console/console_api && go build .`
Expected: success

- [ ] **Step 5: Commit**

```bash
git add console/console_api/handler_gen/eigenflux/console/console_service.go
git commit -m "feat: handle audience_expression in console create/update handlers"
```

---

### Task 12: Console Web Frontend

**Files:**
- Modify: `console/webapp/src/pages/system-notifications/list.tsx`

- [ ] **Step 1: Update form types** (line 45-57)

Add `audience_expression?: string;` to both `CreateFormValues` and `EditFormValues`.

- [ ] **Step 2: Update handleCreate** (line 98-125)

After the `time_range` block (line ~110), add:

```typescript
if (values.audience_expression) {
  body.audience_expression = values.audience_expression;
}
```

- [ ] **Step 3: Update handleEdit** (line 127-158)

After the `time_range` block (line ~143), add:

```typescript
body.audience_expression = values.audience_expression || "";
```

- [ ] **Step 4: Update openEditModal** (line 173-185)

Add to `editForm.setFieldsValue`:

```typescript
audience_expression: record.audience_expression || undefined,
```

- [ ] **Step 5: Update table columns** (line 187-279)

Replace the `audience_type` column (line ~226-232) with:

```tsx
{
  title: "Audience",
  key: "audience",
  width: 200,
  render: (_, record) =>
    record.audience_expression ? (
      <Typography.Text code ellipsis style={{ maxWidth: 180 }}>
        {record.audience_expression}
      </Typography.Text>
    ) : (
      <Tag>{record.audience_type}</Tag>
    ),
},
```

- [ ] **Step 6: Add form item to Create Modal** (after "Effective Window", line ~369)

```tsx
<Form.Item
  name="audience_expression"
  label="Audience Expression"
  tooltip='Leave empty to target all agents. Variables: skill_ver (string), skill_ver_num (int), agent_id (int64). Example: skill_ver_num < 3'
>
  <Input.TextArea rows={2} placeholder="e.g. skill_ver_num < 3" />
</Form.Item>
```

- [ ] **Step 7: Add same form item to Edit Modal** (after "Effective Window", line ~406)

Same `<Form.Item>` as above.

- [ ] **Step 8: Commit**

```bash
git add console/webapp/src/pages/system-notifications/list.tsx
git commit -m "feat: add audience_expression field to system notification forms and table"
```

---

### Task 13: E2E Tests

**Files:**
- Create: `tests/notify/audience_expr_test.go`
- Modify: `tests/testutil/http.go` (add DoGetWithHeaders helper)

- [ ] **Step 1: Add DoGetWithHeaders to test helpers**

Add to `tests/testutil/http.go`:

```go
func DoGetWithHeaders(t *testing.T, path string, token string, headers map[string]string) map[string]interface{} {
	t.Helper()
	req, _ := http.NewRequest("GET", BaseURL+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s failed: %v", path, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	var result map[string]interface{}
	if err := json.Unmarshal(respBody, &result); err != nil {
		t.Fatalf("failed to parse response from GET %s: %v, body: %s", path, err, string(respBody))
	}
	return result
}
```

- [ ] **Step 2: Write E2E test file**

Create `tests/notify/audience_expr_test.go`:

```go
package notify_test

import (
	"net/http"
	"testing"
	"time"

	"eigenflux_server/tests/testutil"
)

func TestAudienceExpressionFiltering(t *testing.T) {
	testutil.WaitForAPI(t)
	token := testutil.LoginAndGetToken(t)

	// Create notification targeting skill_ver_num < 3
	notif := createSystemNotificationWithExpr(t, "announcement", "upgrade notice", 1, 0, 0, `skill_ver_num < 3`)
	defer offlineSystemNotification(t, notif.NotificationID)

	// Wait for Redis sync
	time.Sleep(200 * time.Millisecond)

	// Feed with X-Skill-Ver: 0.0.2 (skill_ver_num=2) → should see notification
	feedData := testutil.DoGetWithHeaders(t, "/api/v1/items/feed?action=refresh", token,
		map[string]string{"X-Skill-Ver": "0.0.2"})
	notifications := feedNotifications(t, feedData["data"].(map[string]interface{}))
	found := false
	for _, n := range notifications {
		if n["notification_id"] == notif.NotificationID {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected notification for skill_ver_num=2 < 3")
	}

	// Feed with X-Skill-Ver: 0.0.3 (skill_ver_num=3) → should NOT see notification
	feedData2 := testutil.DoGetWithHeaders(t, "/api/v1/items/feed?action=refresh", token,
		map[string]string{"X-Skill-Ver": "0.0.3"})
	notifications2 := feedNotifications(t, feedData2["data"].(map[string]interface{}))
	for _, n := range notifications2 {
		if n["notification_id"] == notif.NotificationID {
			t.Fatal("should NOT see notification for skill_ver_num=3")
		}
	}
}

func TestAudienceExpressionNoHeader(t *testing.T) {
	testutil.WaitForAPI(t)
	token := testutil.LoginAndGetToken(t)

	// skill_ver_num < 3 with no header → skill_ver_num=0 → delivered
	notif := createSystemNotificationWithExpr(t, "announcement", "no header test", 1, 0, 0, `skill_ver_num < 3`)
	defer offlineSystemNotification(t, notif.NotificationID)
	time.Sleep(200 * time.Millisecond)

	feedData := testutil.DoGet(t, "/api/v1/items/feed?action=refresh", token)
	notifications := feedNotifications(t, feedData["data"].(map[string]interface{}))
	found := false
	for _, n := range notifications {
		if n["notification_id"] == notif.NotificationID {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected notification when no X-Skill-Ver header (skill_ver_num=0 < 3)")
	}
}

func TestAudienceExpressionCompound(t *testing.T) {
	testutil.WaitForAPI(t)
	token := testutil.LoginAndGetToken(t)

	// skill_ver_num > 0 && skill_ver_num < 3 → no header means NOT delivered
	notif := createSystemNotificationWithExpr(t, "announcement", "compound test", 1, 0, 0, `skill_ver_num > 0 && skill_ver_num < 3`)
	defer offlineSystemNotification(t, notif.NotificationID)
	time.Sleep(200 * time.Millisecond)

	feedData := testutil.DoGet(t, "/api/v1/items/feed?action=refresh", token)
	notifications := feedNotifications(t, feedData["data"].(map[string]interface{}))
	for _, n := range notifications {
		if n["notification_id"] == notif.NotificationID {
			t.Fatal("should NOT see notification when no header with compound expression")
		}
	}
}

func TestAudienceExpressionEmpty(t *testing.T) {
	testutil.WaitForAPI(t)
	token := testutil.LoginAndGetToken(t)

	// Empty expression → broadcast to all
	notif := createSystemNotificationWithExpr(t, "announcement", "broadcast test", 1, 0, 0, "")
	defer offlineSystemNotification(t, notif.NotificationID)
	time.Sleep(200 * time.Millisecond)

	feedData := testutil.DoGet(t, "/api/v1/items/feed?action=refresh", token)
	notifications := feedNotifications(t, feedData["data"].(map[string]interface{}))
	found := false
	for _, n := range notifications {
		if n["notification_id"] == notif.NotificationID {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected notification with empty expression (broadcast)")
	}
}

func TestConsoleAudienceExpressionValidation(t *testing.T) {
	// Unknown variable → error
	body := map[string]interface{}{
		"type":                "announcement",
		"content":             "bad expr test",
		"status":              1,
		"audience_expression": "invalid_var_xyz == 1",
	}
	payload := doConsoleJSONRequest(t, http.MethodPost, "/console/api/v1/system-notifications", body)
	var resp SystemNotificationResp
	mustDecodeResp(t, payload, &resp)
	if resp.Code == 0 {
		t.Fatal("expected error for invalid audience_expression (unknown variable)")
	}

	// Invalid syntax → error
	body2 := map[string]interface{}{
		"type":                "announcement",
		"content":             "bad syntax test",
		"status":              1,
		"audience_expression": "skill_ver_num <><> 3",
	}
	payload2 := doConsoleJSONRequest(t, http.MethodPost, "/console/api/v1/system-notifications", body2)
	var resp2 SystemNotificationResp
	mustDecodeResp(t, payload2, &resp2)
	if resp2.Code == 0 {
		t.Fatal("expected error for invalid audience_expression (bad syntax)")
	}
}

// createSystemNotificationWithExpr extends createSystemNotification with audience_expression.
func createSystemNotificationWithExpr(t *testing.T, notifType, content string, status int32, startAt, endAt int64, audienceExpr string) SystemNotificationInfo {
	t.Helper()
	body := map[string]interface{}{
		"type":    notifType,
		"content": content,
	}
	if status != 0 {
		body["status"] = status
	}
	if startAt != 0 {
		body["start_at"] = startAt
	}
	if endAt != 0 {
		body["end_at"] = endAt
	}
	if audienceExpr != "" {
		body["audience_expression"] = audienceExpr
	}
	payload := doConsoleJSONRequest(t, http.MethodPost, "/console/api/v1/system-notifications", body)
	var resp SystemNotificationResp
	mustDecodeResp(t, payload, &resp)
	if resp.Code != 0 || resp.Data == nil {
		t.Fatalf("create system notification with expr failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return resp.Data.Notification
}
```

- [ ] **Step 3: Build check**

Run: `go build ./tests/notify/`
Expected: success

- [ ] **Step 4: Commit**

```bash
git add tests/testutil/http.go tests/notify/audience_expr_test.go
git commit -m "feat: add E2E tests for audience expression notification filtering"
```

---

### Task 14: Build All + Documentation

- [ ] **Step 1: Full build**

```bash
go build ./...
cd console/console_api && go build .
```

Expected: success for both

- [ ] **Step 2: Run unit tests**

```bash
go test -v ./pkg/audience/
go test -v ./api/middleware/ -run TestParseVersionNum
cd console/console_api && go test -v ./internal/audience/
```

Expected: all PASS

- [ ] **Step 3: Update CLAUDE.md**

Add `audience_expression` context to the Notification Service section. Mention:
- `pkg/audience/` package for expression evaluation
- `CommonParamMiddleware` in `api/middleware/common_param.go`
- `context_vars` in `ListPendingReq`
- Available expression variables: `skill_ver` (string), `skill_ver_num` (int), `agent_id` (int64)

- [ ] **Step 4: Commit**

```bash
git add CLAUDE.md
git commit -m "docs: update CLAUDE.md with audience expression feature"
```
