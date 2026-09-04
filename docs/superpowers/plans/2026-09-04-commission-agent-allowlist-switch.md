# Commission Agent Allowlist Switch Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Commission Agent allowlist enforcement opt-in through `ENABLE_COMMISSION_AGENT_ID_WHITELIST`, defaulting to disabled.

**Architecture:** `pkg/config` loads the boolean feature switch and raw allowlist independently. `commissionaccess.New(enabled, raw)` skips parsing when disabled and returns middleware that immediately continues; when enabled it preserves the existing strict parser and fail-closed authorization behavior. Commission route registration remains unchanged, so every Commission route retains an explicit gate regardless of configuration.

**Tech Stack:** Go 1.25, CloudWeGo Hertz middleware, existing configuration helpers and Go tests.

---

### Task 1: Load the Enable Switch

**Files:**
- Modify: `pkg/config/config.go:132-137,326-331`
- Modify: `pkg/config/config_test.go:280-288`

- [ ] **Step 1: Write failing configuration tests**

Add tests proving the flag defaults off and loads explicit true:

```go
func TestLoadCommissionAgentIDWhitelistDisabledByDefault(t *testing.T) {
	t.Setenv("ENABLE_COMMISSION_AGENT_ID_WHITELIST", "")

	cfg := Load()
	if cfg.EnableCommissionAgentIDWhitelist {
		t.Fatal("Commission Agent ID whitelist enabled by default")
	}
}

func TestLoadCommissionAgentIDWhitelistEnabled(t *testing.T) {
	t.Setenv("ENABLE_COMMISSION_AGENT_ID_WHITELIST", "true")

	cfg := Load()
	if !cfg.EnableCommissionAgentIDWhitelist {
		t.Fatal("Commission Agent ID whitelist was not enabled")
	}
}
```

- [ ] **Step 2: Verify RED**

Run:

```bash
go test ./pkg/config -run '^TestLoadCommissionAgentIDWhitelist'
```

Expected: compilation fails because `EnableCommissionAgentIDWhitelist` does not exist.

- [ ] **Step 3: Add the configuration field and loader**

Add next to the raw allowlist:

```go
EnableCommissionAgentIDWhitelist bool
CommissionAgentIDWhitelist       string
```

Load the switch with a false default:

```go
EnableCommissionAgentIDWhitelist: getEnvBool("ENABLE_COMMISSION_AGENT_ID_WHITELIST", false),
CommissionAgentIDWhitelist:       getEnv("COMMISSION_AGENT_ID_WHITELIST", ""),
```

- [ ] **Step 4: Verify GREEN**

Run:

```bash
gofmt -w pkg/config/config.go pkg/config/config_test.go
go test ./pkg/config -run '^TestLoadCommissionAgentIDWhitelist'
```

Expected: PASS.

### Task 2: Make the Access Gate Conditional

**Files:**
- Modify: `api/commissionaccess/access.go:14-74`
- Modify: `api/commissionaccess/access_test.go:14-121`
- Modify: `api/main.go:71-77`

- [ ] **Step 1: Change existing test constructors to explicit enabled mode**

Replace every existing test call `New(raw)` with `New(true, raw)` so existing parser and denial tests continue to specify enforcement.

- [ ] **Step 2: Write failing disabled-mode tests**

Add tests proving disabled mode neither parses nor authorizes:

```go
func TestNewDisabledSkipsInvalidAllowlist(t *testing.T) {
	access, err := New(false, "invalid,0,-1")
	if err != nil {
		t.Fatal(err)
	}
	if access.enabled {
		t.Fatal("disabled access reported enabled")
	}
}

func TestDisabledMiddlewareBypassesAllowlist(t *testing.T) {
	access, err := New(false, "invalid")
	if err != nil {
		t.Fatal(err)
	}
	for name, gate := range map[string]app.HandlerFunc{
		"v1":      access.V1Middleware(),
		"console": access.ConsoleMiddleware(),
	} {
		t.Run(name, func(t *testing.T) {
			status, _, called, _ := performGate(t, gate, 0)
			if status != http.StatusOK || !called {
				t.Fatalf("status=%d called=%v", status, called)
			}
		})
	}
}
```

- [ ] **Step 3: Verify RED**

Run:

```bash
go test ./api/commissionaccess
```

Expected: compilation fails because `New` does not accept the boolean and `Allowlist.enabled` does not exist.

- [ ] **Step 4: Implement conditional construction and middleware bypass**

Change the type and constructor:

```go
type Allowlist struct {
	enabled  bool
	agentIDs map[int64]struct{}
}

func New(enabled bool, raw string) (*Allowlist, error) {
	if !enabled {
		return &Allowlist{}, nil
	}
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
	return &Allowlist{enabled: true, agentIDs: agentIDs}, nil
}
```

At the beginning of each middleware closure, before reading `agent_id`, add:

```go
if a != nil && !a.enabled {
	c.Next(ctx)
	return
}
```

A nil `*Allowlist` remains fail-closed because only a non-nil disabled object bypasses.

Update API startup:

```go
commissionAccess, err := commissionaccess.New(
	cfg.EnableCommissionAgentIDWhitelist,
	cfg.CommissionAgentIDWhitelist,
)
```

- [ ] **Step 5: Verify GREEN across affected packages**

Run:

```bash
gofmt -w api/commissionaccess/access.go api/commissionaccess/access_test.go api/main.go
go test ./pkg/config ./api/commissionaccess ./api/commissiondiscovery ./api/tradebff ./api/consolev2 ./api
```

Expected: PASS.

- [ ] **Step 6: Commit implementation**

```bash
git add pkg/config/config.go pkg/config/config_test.go api/commissionaccess/access.go api/commissionaccess/access_test.go api/main.go
git commit -m "feat(api): make commission allowlist opt-in"
```

### Task 3: Update the Operational Contract

**Files:**
- Modify: `.env.example:27-32`
- Modify: `docs/dev/configuration.md:115-119`
- Modify: `docs/dev/api_endpoints.md:15-35`

- [ ] **Step 1: Add the switch to the environment example**

```env
# Enable to restrict Commission-backed APIs to the Agent IDs below.
ENABLE_COMMISSION_AGENT_ID_WHITELIST=false
# Comma-separated positive Agent IDs. Empty denies all only when enabled.
COMMISSION_AGENT_ID_WHITELIST=
```

- [ ] **Step 2: Update configuration documentation**

Add:

```markdown
| `ENABLE_COMMISSION_AGENT_ID_WHITELIST` | `false` | Enables API-layer Agent ID allowlist enforcement for Commission-backed routes. When false, the allowlist value is not parsed or enforced. |
```

Revise the raw allowlist row so empty and malformed behavior is explicitly conditional on the switch being enabled.

- [ ] **Step 3: Update API documentation**

State that enforcement occurs only when `ENABLE_COMMISSION_AGENT_ID_WHITELIST=true`; when false, authenticated Commission requests bypass the membership check. Preserve the documented enabled-mode `403` contracts.

- [ ] **Step 4: Commit documentation**

Run:

```bash
git diff --check
```

Expected: no output.

Then:

```bash
git add .env.example docs/dev/configuration.md docs/dev/api_endpoints.md
git commit -m "docs: document commission allowlist switch"
```

### Task 4: Verify Runtime Modes

**Files:**
- Review all files changed by Tasks 1-3.

- [ ] **Step 1: Run focused tests**

```bash
go test -v ./pkg/config ./api/commissionaccess ./api/commissiondiscovery ./api/tradebff ./api/consolev2 ./api
```

Expected: all packages PASS.

- [ ] **Step 2: Build core services**

```bash
bash scripts/common/build.sh
```

Expected: all services compile and artifacts remain under `build/`.

- [ ] **Step 3: Verify disabled startup ignores invalid allowlist**

```bash
ENABLE_COMMISSION_AGENT_ID_WHITELIST=false \
COMMISSION_AGENT_ID_WHITELIST=invalid \
./build/api
```

Expected: the API passes allowlist initialization and proceeds to normal dependency startup. Stop it after the startup log proves the configuration was accepted.

- [ ] **Step 4: Verify enabled startup rejects invalid allowlist**

```bash
ENABLE_COMMISSION_AGENT_ID_WHITELIST=true \
COMMISSION_AGENT_ID_WHITELIST=invalid \
./build/api
```

Expected: exit status 1 with `invalid COMMISSION_AGENT_ID_WHITELIST` before route serving.

- [ ] **Step 5: Final hygiene**

```bash
git diff --check
git status --short --branch
```

Expected: no whitespace errors and no uncommitted tracked changes.
