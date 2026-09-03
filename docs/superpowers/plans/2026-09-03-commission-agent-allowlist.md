# Commission Agent Allowlist Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deny every Commission-backed API request unless the authenticated Agent ID appears in a startup-configured allowlist.

**Architecture:** `pkg/config` loads the raw environment value, while a focused `api/commissionaccess` package validates it once and exposes immutable V1 and Console middleware adapters. Route registration places those adapters after existing authentication/session middleware and before discovery or Commission BFF handlers, so rejected requests never call Sort RPC or the Commission upstream.

**Tech Stack:** Go 1.25, CloudWeGo Hertz middleware, standard-library `strconv`/`strings`, existing Go test and Hertz `ut` request helpers.

---

## File Map

- Create `api/commissionaccess/access.go`: parse and own the immutable Agent ID set; expose V1 and Console authorization middleware.
- Create `api/commissionaccess/access_test.go`: validate parsing, fail-closed behavior, response envelopes, and downstream short-circuiting.
- Modify `pkg/config/config.go`: load `COMMISSION_AGENT_ID_WHITELIST` as raw startup configuration.
- Modify `pkg/config/config_test.go`: prove the environment value reaches the API configuration unchanged.
- Modify `api/commissiondiscovery/handlers.go`: attach the V1 Commission gate to both discovery routes.
- Modify `api/commissiondiscovery/handlers_test.go`: prove both registered discovery routes reject an authenticated unlisted Agent before Sort.
- Modify `api/consolev2/service.go`: let `ConsoleBFFHandlers` append more than one business middleware/handler while preserving existing ordering.
- Modify `api/main.go`: parse the allowlist at startup and register every Commission Console BFF route through the Console gate.
- Modify `api/main_test.go`: prove the complete ten-route Commission BFF inventory is registered through the gate.
- Modify `.env.example`: document the deployment variable.
- Modify `docs/dev/configuration.md`: document syntax, startup validation, and empty-value denial.
- Modify `docs/dev/api_endpoints.md`: document protected route families and `403` behavior.

### Task 1: Load Raw Commission Allowlist Configuration

**Files:**
- Modify: `pkg/config/config.go:131-148,324-341`
- Test: `pkg/config/config_test.go:263-278`

- [ ] **Step 1: Write the failing configuration test**

Add this test next to the existing Commission configuration tests:

```go
func TestLoadCommissionAgentIDWhitelist(t *testing.T) {
	t.Setenv("COMMISSION_AGENT_ID_WHITELIST", " 42,9223372036854775807 ")

	cfg := Load()
	if cfg.CommissionAgentIDWhitelist != " 42,9223372036854775807 " {
		t.Fatalf("CommissionAgentIDWhitelist=%q", cfg.CommissionAgentIDWhitelist)
	}
}
```

- [ ] **Step 2: Run the test and verify RED**

Run:

```bash
go test ./pkg/config -run '^TestLoadCommissionAgentIDWhitelist$'
```

Expected: compilation fails because `Config.CommissionAgentIDWhitelist` does not exist.

- [ ] **Step 3: Add the minimal configuration field and loader**

Add the field with the other Commission settings:

```go
CommissionAgentIDWhitelist string
```

Load it without normalization so the authorization owner performs strict validation:

```go
CommissionAgentIDWhitelist: getEnv("COMMISSION_AGENT_ID_WHITELIST", ""),
```

- [ ] **Step 4: Run the test and verify GREEN**

Run:

```bash
go test ./pkg/config -run '^TestLoadCommissionAgentIDWhitelist$'
```

Expected: PASS.

- [ ] **Step 5: Commit the configuration change**

```bash
git add pkg/config/config.go pkg/config/config_test.go
git commit -m "feat(config): load commission agent allowlist"
```

### Task 2: Implement the Fail-Closed Commission Access Gate

**Files:**
- Create: `api/commissionaccess/access.go`
- Create: `api/commissionaccess/access_test.go`

- [ ] **Step 1: Write failing parser tests**

Create table-driven tests that define the full configuration contract:

```go
func TestNewNormalizesValidAgentIDs(t *testing.T) {
	access, err := New(" 42, 42, ,9223372036854775807,")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{42, 9223372036854775807} {
		if !access.allowed(id) {
			t.Fatalf("Agent %d was not allowed", id)
		}
	}
	if access.allowed(41) {
		t.Fatal("unconfigured Agent was allowed")
	}
}

func TestNewRejectsInvalidAgentIDs(t *testing.T) {
	for _, raw := range []string{"0", "-1", "agent-42", "9223372036854775808"} {
		t.Run(raw, func(t *testing.T) {
			if access, err := New(raw); err == nil || access != nil {
				t.Fatalf("New(%q) access=%v error=%v", raw, access, err)
			}
		})
	}
}

func TestNewEmptyAllowlistDeniesEveryAgent(t *testing.T) {
	for _, raw := range []string{"", " , , "} {
		access, err := New(raw)
		if err != nil {
			t.Fatal(err)
		}
		if access.allowed(42) {
			t.Fatalf("New(%q) allowed Agent 42", raw)
		}
	}
}
```

- [ ] **Step 2: Write failing middleware behavior tests**

Use a real Hertz chain with a trusted-context setup handler, the middleware under test, and a downstream handler. Assert both the response and that downstream was not invoked:

```go
func performGate(t *testing.T, gate app.HandlerFunc, agentID int64) (int, map[string]any, bool, string) {
	t.Helper()
	h := server.New()
	called := false
	h.GET("/test",
		func(ctx context.Context, c *app.RequestContext) {
			if agentID > 0 {
				c.Set("agent_id", agentID)
			}
			c.Next(ctx)
		},
		gate,
		func(_ context.Context, c *app.RequestContext) {
			called = true
			c.JSON(http.StatusOK, map[string]any{"ok": true})
		},
	)
	recorder := ut.PerformRequest(h.Engine, http.MethodGet, "/test", nil)
	var payload map[string]any
	if err := json.Unmarshal(recorder.Result().Body(), &payload); err != nil {
		t.Fatal(err)
	}
	return recorder.Result().StatusCode(), payload, called, string(recorder.Result().Header.Peek("Cache-Control"))
}

func TestV1MiddlewareRejectsUnlistedAgent(t *testing.T) {
	access, _ := New("7")
	status, payload, called, _ := performGate(t, access.V1Middleware(), 8)
	if status != http.StatusForbidden || payload["code"] != float64(403) || payload["msg"] != "commission access is not allowed" || called {
		t.Fatalf("status=%d payload=%v called=%v", status, payload, called)
	}
}

func TestConsoleMiddlewareRejectsUnlistedAgent(t *testing.T) {
	access, _ := New("7")
	status, payload, called, cacheControl := performGate(t, access.ConsoleMiddleware(), 8)
	errorPayload := payload["error"].(map[string]any)
	if status != http.StatusForbidden || errorPayload["code"] != "COMMISSION_ACCESS_FORBIDDEN" || called {
		t.Fatalf("status=%d payload=%v called=%v", status, payload, called)
	}
	if cacheControl != "private, no-store" {
		t.Fatalf("Cache-Control=%q", cacheControl)
	}
}

func TestMiddlewareAllowsListedAgent(t *testing.T) {
	access, _ := New("7")
	for name, gate := range map[string]app.HandlerFunc{"v1": access.V1Middleware(), "console": access.ConsoleMiddleware()} {
		t.Run(name, func(t *testing.T) {
			status, _, called, _ := performGate(t, gate, 7)
			if status != http.StatusOK || !called {
				t.Fatalf("status=%d called=%v", status, called)
			}
		})
	}
}
```

- [ ] **Step 3: Run package tests and verify RED**

Run:

```bash
go test ./api/commissionaccess
```

Expected: compilation fails because `New`, `V1Middleware`, and `ConsoleMiddleware` do not exist.

- [ ] **Step 4: Implement the immutable parser and middleware**

Create `api/commissionaccess/access.go` with this API and behavior:

```go
package commissionaccess

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
)

type Allowlist struct {
	agentIDs map[int64]struct{}
}

func New(raw string) (*Allowlist, error) {
	agentIDs := make(map[int64]struct{})
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		agentID, err := strconv.ParseInt(entry, 10, 64)
		if err != nil || agentID <= 0 {
			return nil, fmt.Errorf("invalid COMMISSION_AGENT_ID_WHITELIST")
		}
		agentIDs[agentID] = struct{}{}
	}
	return &Allowlist{agentIDs: agentIDs}, nil
}

func (a *Allowlist) allowed(agentID int64) bool {
	if a == nil || agentID <= 0 {
		return false
	}
	_, ok := a.agentIDs[agentID]
	return ok
}

func contextAgentID(c *app.RequestContext) (int64, bool) {
	value, exists := c.Get("agent_id")
	agentID, ok := value.(int64)
	return agentID, exists && ok && agentID > 0
}

func (a *Allowlist) V1Middleware() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		agentID, ok := contextAgentID(c)
		if !ok || !a.allowed(agentID) {
			c.JSON(http.StatusForbidden, map[string]any{"code": 403, "msg": "commission access is not allowed"})
			c.Abort()
			return
		}
		c.Next(ctx)
	}
}

func (a *Allowlist) ConsoleMiddleware() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		agentID, ok := contextAgentID(c)
		if !ok || !a.allowed(agentID) {
			c.Header("Cache-Control", "private, no-store")
			c.JSON(http.StatusForbidden, map[string]any{"error": map[string]any{
				"code": "COMMISSION_ACCESS_FORBIDDEN", "message": "Commission access is not enabled for this Agent",
			}})
			c.Abort()
			return
		}
		c.Next(ctx)
	}
}
```

- [ ] **Step 5: Format and verify GREEN**

Run:

```bash
gofmt -w api/commissionaccess/access.go api/commissionaccess/access_test.go
go test ./api/commissionaccess
```

Expected: PASS.

- [ ] **Step 6: Commit the access gate**

```bash
git add api/commissionaccess/access.go api/commissionaccess/access_test.go
git commit -m "feat(api): add commission agent access gate"
```

### Task 3: Protect Both Discovery Routes

**Files:**
- Modify: `api/commissiondiscovery/handlers.go:19-55`
- Modify: `api/commissiondiscovery/handlers_test.go:19-129`
- Modify: `api/main.go:70-80,300-305`

- [ ] **Step 1: Write a failing route-registration test**

Add an internal `registerRoutes` seam test that injects a trusted authentication handler and the real allowlist gate. Exercise both production paths in a table and assert Sort is untouched:

```go
func TestRegisteredRoutesRejectUnlistedAgentBeforeSort(t *testing.T) {
	access, err := commissionaccess.New("7")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/commissions/search?query=go", "/api/v1/commissions/recommendations"} {
		t.Run(path, func(t *testing.T) {
			sortClient := &fakeSort{}
			h := server.New()
			trustedAuth := func(ctx context.Context, c *app.RequestContext) {
				c.Set("agent_id", int64(8))
				c.Next(ctx)
			}
			registerRoutes(h, New(sortClient, &fakeIDGen{}, nil), trustedAuth, access.V1Middleware())
			recorder := ut.PerformRequest(h.Engine, http.MethodGet, path, nil)
			if recorder.Result().StatusCode() != http.StatusForbidden {
				t.Fatalf("status=%d body=%s", recorder.Result().StatusCode(), recorder.Result().Body())
			}
			if sortClient.searchReq != nil || sortClient.recommendReq != nil {
				t.Fatalf("Sort called: search=%v recommend=%v", sortClient.searchReq, sortClient.recommendReq)
			}
		})
	}
}
```

- [ ] **Step 2: Run the test and verify RED**

Run:

```bash
go test ./api/commissiondiscovery -run '^TestRegisteredRoutesRejectUnlistedAgentBeforeSort$'
```

Expected: compilation fails because `registerRoutes` does not exist.

- [ ] **Step 3: Add the registration seam and required allowlist**

Change production registration to require an allowlist and delegate to the internal seam:

```go
func Register(h *server.Hertz, service *Service, access *commissionaccess.Allowlist) {
	registerRoutes(h, service, middleware.AuthMiddleware(), access.V1Middleware())
}

func registerRoutes(h *server.Hertz, service *Service, auth, access app.HandlerFunc) {
	h.GET("/api/v1/commissions/search", auth, access, service.Search)
	h.GET("/api/v1/commissions/recommendations", auth, access, service.Recommend)
}
```

In `api/main.go`, parse immediately after `config.Load()`:

```go
commissionAccess, err := commissionaccess.New(cfg.CommissionAgentIDWhitelist)
if err != nil {
	log.Fatal(err)
}
```

Pass it to discovery registration:

```go
commissiondiscovery.Register(h, commissionDiscoveryService, commissionAccess)
```

- [ ] **Step 4: Format and verify GREEN**

Run:

```bash
gofmt -w api/commissiondiscovery/handlers.go api/commissiondiscovery/handlers_test.go api/main.go
go test ./api/commissiondiscovery ./api/commissionaccess ./pkg/config
```

Expected: PASS.

- [ ] **Step 5: Commit discovery protection**

```bash
git add api/commissiondiscovery/handlers.go api/commissiondiscovery/handlers_test.go api/main.go
git commit -m "feat(api): gate commission discovery routes"
```

### Task 4: Protect the Complete Console Commission BFF Surface

**Files:**
- Modify: `api/consolev2/service.go:276-286`
- Modify: `api/main.go:347-439`
- Modify: `api/main_test.go:1-123`

- [ ] **Step 1: Inspect all `ConsoleBFFHandlers` references with LSP**

Use LSP references on `ConsoleBFFHandlers` before changing its exported signature. Confirm all call sites pass one final handler and therefore remain source-compatible with a variadic form.

- [ ] **Step 2: Write the failing route-inventory test**

Add a test for a new `registerCommissionConsoleBFFRoutes` helper. Capture every registration and assert the exact route/method set and two handlers per route (gate first, business handler second):

```go
func TestRegisterCommissionConsoleBFFRoutesUsesGateForEveryRoute(t *testing.T) {
	access, err := commissionaccess.New("7")
	if err != nil {
		t.Fatal(err)
	}
	type route struct{ method, path string }
	registered := map[route][]app.HandlerFunc{}
	read := func(path string, handlers ...app.HandlerFunc) {
		registered[route{http.MethodGet, path}] = handlers
	}
	write := func(method, path string, handlers ...app.HandlerFunc) {
		registered[route{method, path}] = handlers
	}
	registerCommissionConsoleBFFRoutes(read, write, access.ConsoleMiddleware(), tradebff.NewUnavailable("test"))

	expected := []route{
		{http.MethodGet, "trade/overview"},
		{http.MethodGet, "trade/commissions"},
		{http.MethodGet, "trade/orders"},
		{http.MethodGet, "trade/orders/:order_id"},
		{http.MethodGet, "earnings/summary"},
		{http.MethodGet, "earnings/records"},
		{http.MethodGet, "payout-method"},
		{http.MethodPost, "payout-method/authorization"},
		{http.MethodPost, "withdrawals"},
		{http.MethodGet, "withdrawals/:withdrawal_id"},
	}
	if len(registered) != len(expected) {
		t.Fatalf("registered %d routes, want %d", len(registered), len(expected))
	}
	for _, want := range expected {
		handlers, ok := registered[want]
		if !ok || len(handlers) != 2 {
			t.Fatalf("route=%v present=%v handlers=%d", want, ok, len(handlers))
		}
		h := server.New()
		h.GET("/test", func(ctx context.Context, c *app.RequestContext) {
			c.Set("agent_id", int64(8))
			c.Next(ctx)
		}, handlers[0], func(_ context.Context, c *app.RequestContext) {
			c.JSON(http.StatusOK, map[string]any{"unexpected": true})
		})
		recorder := ut.PerformRequest(h.Engine, http.MethodGet, "/test", nil)
		if recorder.Result().StatusCode() != http.StatusForbidden {
			t.Fatalf("route=%v gate status=%d body=%s", want, recorder.Result().StatusCode(), recorder.Result().Body())
		}
	}
}
```

- [ ] **Step 3: Run the test and verify RED**

Run:

```bash
go test ./api -run '^TestRegisterCommissionConsoleBFFRoutesUsesGateForEveryRoute$'
```

Expected: compilation fails because variadic BFF registration and `registerCommissionConsoleBFFRoutes` do not exist.

- [ ] **Step 4: Deepen the Console BFF chain without changing existing behavior**

Change the method signature and append all provided business handlers after the existing auth/completion/no-store prefix:

```go
func (s *Service) ConsoleBFFHandlers(mutation bool, handlers ...app.HandlerFunc) []app.HandlerFunc {
	noStore := func(ctx context.Context, c *app.RequestContext) {
		c.Header("Cache-Control", "private, no-store")
		c.Header("Pragma", "no-cache")
		c.Next(ctx)
	}
	chain := []app.HandlerFunc{s.consoleAuth(mutation), s.requireCompleted, noStore}
	return append(chain, handlers...)
}
```

Change the local `read` and `write` helpers in `registerConsoleV2BusinessBFF` to accept variadic handlers:

```go
read := func(path string, handlers ...app.HandlerFunc) {
	h.GET("/api/v2/console/bff/"+path, service.ConsoleBFFHandlers(false, handlers...)...)
}
write := func(method, path string, handlers ...app.HandlerFunc) {
	h.Handle(method, "/api/v2/console/bff/"+path, service.ConsoleBFFHandlers(true, handlers...)...)
}
```

Replace the ten direct Commission registrations with one helper call:

```go
registerCommissionConsoleBFFRoutes(read, write, commissionAccess.ConsoleMiddleware(), trade)
```

Add the helper with the explicit route inventory:

```go
type consoleBFFReadRegistrar func(string, ...app.HandlerFunc)
type consoleBFFWriteRegistrar func(string, string, ...app.HandlerFunc)

func registerCommissionConsoleBFFRoutes(read consoleBFFReadRegistrar, write consoleBFFWriteRegistrar, access app.HandlerFunc, trade *tradebff.Service) {
	read("trade/overview", access, trade.TradeOverview)
	read("trade/commissions", access, trade.TradeCommissions)
	read("trade/orders", access, trade.TradeOrders)
	read("trade/orders/:order_id", access, trade.TradeOrder)
	read("earnings/summary", access, trade.EarningsSummary)
	read("earnings/records", access, trade.EarningsRecords)
	read("payout-method", access, trade.PayoutMethod)
	write(http.MethodPost, "payout-method/authorization", access, trade.MutatePayoutMethod)
	write(http.MethodPost, "withdrawals", access, trade.CreateWithdrawal)
	read("withdrawals/:withdrawal_id", access, trade.Withdrawal)
}
```

Pass `commissionAccess` into `registerConsoleV2BusinessBFF` from `main` and add it to that function's parameters.

- [ ] **Step 5: Format and verify GREEN**

Run:

```bash
gofmt -w api/consolev2/service.go api/main.go api/main_test.go
go test ./api ./api/consolev2 ./api/tradebff ./api/commissionaccess
```

Expected: PASS, including all pre-existing BFF tests.

- [ ] **Step 6: Commit Console BFF protection**

```bash
git add api/consolev2/service.go api/main.go api/main_test.go
git commit -m "feat(api): gate commission console routes"
```

### Task 5: Document the Rollout Contract

**Files:**
- Modify: `.env.example:28-41,343-346`
- Modify: `docs/dev/configuration.md:115-122`
- Modify: `docs/dev/api_endpoints.md:5-22`

- [ ] **Step 1: Add the environment example**

Place this beside the other Commission variables:

```env
# Comma-separated Agent IDs allowed to use any Commission-backed API. Empty denies all.
COMMISSION_AGENT_ID_WHITELIST=
```

- [ ] **Step 2: Update configuration documentation**

Add a table row stating:

```markdown
| `COMMISSION_AGENT_ID_WHITELIST` | (empty) | Comma-separated positive Agent IDs allowed to use Commission discovery and Console trade/earnings/payout/withdrawal BFF routes. Empty denies all; malformed values prevent API startup. |
```

- [ ] **Step 3: Update API endpoint documentation**

Document that both discovery routes and all Commission-backed Console BFF routes require membership after authentication, return `403` outside the allowlist, and make no downstream Commission/Sort call when rejected.

- [ ] **Step 4: Check documentation and commit**

Run:

```bash
git diff --check
```

Expected: no output.

Then commit:

```bash
git add .env.example docs/dev/configuration.md docs/dev/api_endpoints.md
git commit -m "docs: document commission agent allowlist"
```

### Task 6: End-to-End Verification and Cleanup

**Files:**
- Review all files changed by Tasks 1-5.

- [ ] **Step 1: Run focused tests**

```bash
go test ./pkg/config ./api/commissionaccess ./api/commissiondiscovery ./api/tradebff ./api/consolev2 ./api
```

Expected: all packages PASS with no warnings.

- [ ] **Step 2: Run the core build**

```bash
bash scripts/common/build.sh
```

Expected: exit status 0; artifacts remain under `build/`.

- [ ] **Step 3: Start the actual local services**

```bash
./scripts/local/start_local.sh
```

Expected: core services become healthy. Use the project-managed process workflow if the script remains attached.

- [ ] **Step 4: Smoke-test both access outcomes**

Start the API once with a known authenticated Agent ID in `COMMISSION_AGENT_ID_WHITELIST` and once with that ID absent. Exercise one discovery endpoint and one Console Commission BFF endpoint.

Expected for the listed Agent: the request reaches its existing handler; any dependency error is the existing downstream error, not an allowlist `403`.

Expected for the unlisted Agent:

```json
{"code":403,"msg":"commission access is not allowed"}
```

and:

```json
{"error":{"code":"COMMISSION_ACCESS_FORBIDDEN","message":"Commission access is not enabled for this Agent"}}
```

Confirm logs show no Sort or Commission upstream invocation for denied requests.

- [ ] **Step 5: Run final hygiene checks**

```bash
git diff --check
git status --short
```

Expected: no whitespace errors; only intentional uncommitted verification artifacts, if any, are present. Remove generated or temporary artifacts that are not part of the feature.

- [ ] **Step 6: Commit any cleanup required by verification**

If verification required tracked fixes, stage the feature's tracked files and commit:

```bash
git add pkg/config/config.go pkg/config/config_test.go api/commissionaccess api/commissiondiscovery api/consolev2/service.go api/main.go api/main_test.go .env.example docs/dev/configuration.md docs/dev/api_endpoints.md
git commit -m "fix(api): complete commission allowlist verification"
```

If no tracked fixes were required, do not create an empty commit.
