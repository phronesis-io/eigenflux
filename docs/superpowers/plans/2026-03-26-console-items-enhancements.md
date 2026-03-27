# Console Items Enhancements Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add three features to the console items page: agent detail popup, exclude-email-suffix filter, and inline status update dropdown.

**Architecture:** All backend changes live in `console/console_api/` (IDL, DAL, handler). Two new endpoints (`GET /agents/:agent_id`, `PUT /items/:item_id`) plus one filter addition to the existing `GET /items` endpoint. Frontend changes are isolated to `console/webapp/src/pages/items/list.tsx` and `console/webapp/src/dataProvider.ts`.

**Tech Stack:** Go (Hertz + GORM), Thrift IDL (hz codegen), React (Refine + Ant Design), TypeScript

---

### Task 1: IDL — Add GetAgent, UpdateItem, and exclude_email_suffixes

**Files:**
- Modify: `console/console_api/idl/console.thrift`

- [ ] **Step 1: Add GetAgent structs and ListItemsReq filter to IDL**

Add the following to `console/console_api/idl/console.thrift`:

After the `ListAgentsResp` struct (after line 38), add:

```thrift
struct GetAgentReq {
    1: required i64 agent_id (api.path="agent_id")
}

struct GetAgentData {
    1: ConsoleAgentInfo agent
}

struct GetAgentResp {
    1: i32 code
    2: string msg
    3: GetAgentData data
}
```

Add field 6 to `ListItemsReq` (after line 47, before the closing `}`):

```thrift
    6: optional string exclude_email_suffixes (api.query="exclude_email_suffixes")
```

- [ ] **Step 2: Add UpdateItem structs to IDL**

After the `ListItemsResp` struct (after line 81), add:

```thrift
struct UpdateItemReq {
    1: required i64 item_id (api.path="item_id")
    2: optional i32 status (api.body="status")
}

struct UpdateItemData {
    1: ConsoleItemInfo item
}

struct UpdateItemResp {
    1: i32 code
    2: string msg
    3: UpdateItemData data
}
```

- [ ] **Step 3: Add new service methods to ConsoleService**

Add these two lines inside the `service ConsoleService` block (after the `ListAgents` line):

```thrift
    GetAgentResp GetAgent(1: GetAgentReq req) (api.get="/console/api/v1/agents/:agent_id")
    UpdateItemResp UpdateItem(1: UpdateItemReq req) (api.put="/console/api/v1/items/:item_id")
```

- [ ] **Step 4: Run hz codegen**

```bash
cd console/console_api && bash scripts/generate_api.sh
```

Expected: hz regenerates `router_gen/eigenflux/console/console.go` with new routes (`GET /agents/:agent_id`, `PUT /items/:item_id`), generates model stubs in `model/`, and adds empty handler function stubs in `handler_gen/`. The handler file (`console_service.go`) already has our manual implementations, so hz will add stub functions `GetAgent` and `UpdateItem` that we need to replace with actual logic.

- [ ] **Step 5: Check hz output and fix handler stubs**

After hz runs, it will add empty stub functions for `GetAgent` and `UpdateItem` to `handler_gen/eigenflux/console/console_service.go`. These stubs look like:

```go
func GetAgent(ctx context.Context, c *app.RequestContext) {
	// ...
}

func UpdateItem(ctx context.Context, c *app.RequestContext) {
	// ...
}
```

**Do not implement them yet** — we'll fill them in Task 3 after the DAL is ready. For now just verify the file compiles by checking the stubs exist.

Also check `router_gen/eigenflux/console/middleware.go` — hz may add new middleware stubs (`_getagentMw`, `_updateitemMw`, `_itemsMw`). If so, make sure they return `nil` like the existing ones.

- [ ] **Step 6: Verify build compiles**

```bash
cd console/console_api && go build .
```

Expected: Build succeeds (the stub handlers may have unused imports — that's fine at this stage, hz-generated stubs may reference the model package).

- [ ] **Step 7: Commit**

```bash
git add console/console_api/idl/ console/console_api/model/ console/console_api/router_gen/ console/console_api/handler_gen/
git commit -m "feat(console): add IDL for GetAgent, UpdateItem, exclude_email_suffixes filter"
```

---

### Task 2: DAL — GetAgentByID, UpdateItem, ListItems email suffix filter

**Files:**
- Modify: `console/console_api/internal/dal/query.go`

- [ ] **Step 1: Add GetAgentByID function**

Add after the `UpdateAgent` function (after line 97) in `console/console_api/internal/dal/query.go`:

```go
func GetAgentByID(db *gorm.DB, agentID int64) (*AgentWithProfile, error) {
	var agent AgentWithProfile
	err := db.Table("agents").
		Select("agents.*, agent_profiles.status as profile_status, agent_profiles.keywords as profile_keywords").
		Joins("LEFT JOIN agent_profiles ON agents.agent_id = agent_profiles.agent_id").
		Where("agents.agent_id = ?", agentID).
		First(&agent).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrAgentNotFound
		}
		return nil, err
	}
	return &agent, nil
}
```

- [ ] **Step 2: Add ExcludeEmailSuffixes to ListItemsParams and update ListItems**

In `console/console_api/internal/dal/query.go`, add the field to `ListItemsParams`:

```go
type ListItemsParams struct {
	Page                 int32
	PageSize             int32
	Status               *int32
	Keyword              *string
	Title                *string
	ExcludeEmailSuffixes []string
}
```

Add the filtering logic inside `ListItems`, after the existing `Title` filter block (after the line `"%"+*params.Title+"%", "%"+*params.Title+"%")`) and before the `Count` call:

```go
	if len(params.ExcludeEmailSuffixes) > 0 {
		subQuery := db.Table("agents").Select("agent_id")
		conditions := make([]string, 0, len(params.ExcludeEmailSuffixes))
		args := make([]interface{}, 0, len(params.ExcludeEmailSuffixes))
		for _, suffix := range params.ExcludeEmailSuffixes {
			conditions = append(conditions, "agents.email ILIKE ?")
			args = append(args, "%"+suffix)
		}
		subQuery = subQuery.Where(strings.Join(conditions, " OR "), args...)
		query = query.Where("raw_items.author_agent_id NOT IN (?)", subQuery)
	}
```

Make sure `"strings"` is already in the import block (it is).

- [ ] **Step 3: Add ErrItemNotFound and UpdateItem function**

Add after `ErrAgentNotFound` at the top of the file:

```go
var ErrItemNotFound = errors.New("item not found")
```

Add `UpdateItemParams` struct and `UpdateItem` function at the end of the file:

```go
type UpdateItemParams struct {
	Status *int32
}

func UpdateItem(db *gorm.DB, itemID int64, params UpdateItemParams) (*ItemWithProcessed, error) {
	var count int64
	if err := db.Table("raw_items").Where("item_id = ?", itemID).Count(&count).Error; err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, ErrItemNotFound
	}

	updates := map[string]interface{}{
		"updated_at": time.Now().UnixMilli(),
	}
	if params.Status != nil {
		updates["status"] = int16(*params.Status)
	}

	if err := db.Table("processed_items").Where("item_id = ?", itemID).Updates(updates).Error; err != nil {
		return nil, err
	}

	var item ItemWithProcessed
	err := db.Table("raw_items").
		Select("raw_items.*, processed_items.status, processed_items.summary, processed_items.broadcast_type, processed_items.domains, processed_items.keywords, processed_items.expire_time, processed_items.geo, processed_items.source_type, processed_items.expected_response, processed_items.group_id, processed_items.updated_at").
		Joins("LEFT JOIN processed_items ON raw_items.item_id = processed_items.item_id").
		Where("raw_items.item_id = ?", itemID).
		First(&item).Error
	if err != nil {
		return nil, err
	}
	return &item, nil
}
```

- [ ] **Step 4: Verify build compiles**

```bash
cd console/console_api && go build .
```

Expected: Build succeeds.

- [ ] **Step 5: Commit**

```bash
git add console/console_api/internal/dal/query.go
git commit -m "feat(console): add GetAgentByID, UpdateItem DAL and exclude email suffix filter"
```

---

### Task 3: Handlers — GetAgent, UpdateItem, ListItems update

**Files:**
- Modify: `console/console_api/handler_gen/eigenflux/console/console_service.go`

- [ ] **Step 1: Add GetAgent response types and handler**

Add to the response types section (after `UpdateAgentResp`, around line 72):

```go
type GetAgentData struct {
	Agent map[string]interface{} `json:"agent"`
}

type GetAgentResp struct {
	Code int32         `json:"code"`
	Msg  string        `json:"msg"`
	Data *GetAgentData `json:"data,omitempty"`
}
```

Replace the hz-generated `GetAgent` stub with:

```go
// GetAgent godoc
// @Summary      Get agent by ID
// @Description  Returns a single agent with profile data
// @Tags         console
// @Produce      json
// @Param        agent_id  path  integer  true  "Agent ID"
// @Success      200  {object}  GetAgentResp
// @Router /console/api/v1/agents/:agent_id [GET]
func GetAgent(ctx context.Context, c *app.RequestContext) {
	agentID, err := strconv.ParseInt(strings.TrimSpace(c.Param("agent_id")), 10, 64)
	if err != nil || agentID <= 0 {
		writeConsoleError(c, "invalid agent_id")
		return
	}

	agent, err := dal.GetAgentByID(db.DB, agentID)
	if err != nil {
		if errors.Is(err, dal.ErrAgentNotFound) {
			writeConsoleError(c, "agent not found")
			return
		}
		writeConsoleError(c, "database query failed: "+err.Error())
		return
	}

	c.JSON(consts.StatusOK, &GetAgentResp{
		Code: 0, Msg: "success",
		Data: &GetAgentData{Agent: toConsoleAgentInfo(*agent)},
	})
}
```

- [ ] **Step 2: Add UpdateItem response types and handler**

Add to the response types section:

```go
type updateItemReq struct {
	Status *int32 `json:"status"`
}

type UpdateItemData struct {
	Item map[string]interface{} `json:"item"`
}

type UpdateItemResp struct {
	Code int32           `json:"code"`
	Msg  string          `json:"msg"`
	Data *UpdateItemData `json:"data,omitempty"`
}
```

Replace the hz-generated `UpdateItem` stub with:

```go
// UpdateItem godoc
// @Summary      Update item
// @Description  Partially update an item's fields (currently status)
// @Tags         console
// @Accept       json
// @Produce      json
// @Param        item_id  path  integer  true  "Item ID"
// @Param        body     body  updateItemReq  true  "Update request (all fields optional)"
// @Success      200  {object}  UpdateItemResp
// @Router /console/api/v1/items/:item_id [PUT]
func UpdateItem(ctx context.Context, c *app.RequestContext) {
	itemID, err := strconv.ParseInt(strings.TrimSpace(c.Param("item_id")), 10, 64)
	if err != nil || itemID <= 0 {
		writeConsoleError(c, "invalid item_id")
		return
	}

	var req updateItemReq
	if err := c.BindAndValidate(&req); err != nil {
		writeConsoleError(c, "invalid request: "+err.Error())
		return
	}

	if req.Status == nil {
		writeConsoleError(c, "at least one field must be provided")
		return
	}

	item, err := dal.UpdateItem(db.DB, itemID, dal.UpdateItemParams{
		Status: req.Status,
	})
	if err != nil {
		if errors.Is(err, dal.ErrItemNotFound) {
			writeConsoleError(c, "item not found")
			return
		}
		writeConsoleError(c, "update failed: "+err.Error())
		return
	}

	c.JSON(consts.StatusOK, &UpdateItemResp{
		Code: 0, Msg: "success",
		Data: &UpdateItemData{Item: toConsoleItemInfo(*item)},
	})
}
```

- [ ] **Step 3: Update ListItems handler to parse exclude_email_suffixes**

In the `ListItems` handler, add parsing of the new param after the `title` line (after `title := strPtr(strings.TrimSpace(c.Query("title")))`):

```go
	var excludeSuffixes []string
	if raw := strings.TrimSpace(c.Query("exclude_email_suffixes")); raw != "" {
		for _, s := range strings.Split(raw, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				excludeSuffixes = append(excludeSuffixes, s)
			}
		}
	}
```

Update the `dal.ListItems` call to include the new field:

```go
	items, total, err := dal.ListItems(db.DB, dal.ListItemsParams{
		Page:                 page,
		PageSize:             pageSize,
		Status:               statusFilter,
		Keyword:              keyword,
		Title:                title,
		ExcludeEmailSuffixes: excludeSuffixes,
	})
```

- [ ] **Step 4: Verify build compiles**

```bash
cd console/console_api && go build .
```

Expected: Build succeeds.

- [ ] **Step 5: Commit**

```bash
git add console/console_api/handler_gen/eigenflux/console/console_service.go
git commit -m "feat(console): add GetAgent, UpdateItem handlers and exclude_email_suffixes filter"
```

---

### Task 4: Backend tests

**Files:**
- Modify: `tests/console/console_test.go`

- [ ] **Step 1: Add response types for new endpoints**

Add after the existing `ListAgentImprItemsResp` struct (after line 52):

```go
type GetAgentData struct {
	Agent map[string]interface{} `json:"agent"`
}

type GetAgentResp struct {
	Code int32         `json:"code"`
	Msg  string        `json:"msg"`
	Data *GetAgentData `json:"data"`
}

type UpdateItemData struct {
	Item map[string]interface{} `json:"item"`
}

type UpdateItemResp struct {
	Code int32           `json:"code"`
	Msg  string          `json:"msg"`
	Data *UpdateItemData `json:"data"`
}
```

- [ ] **Step 2: Add TestConsoleGetAgent test**

Add after the existing `TestConsoleListAgentsWithFilters` function:

```go
func TestConsoleGetAgent(t *testing.T) {
	// First, get an agent_id from the list endpoint
	listResp, err := http.Get(fmt.Sprintf("%s/console/api/v1/agents?page=1&page_size=1", testutil.ConsoleBaseURL))
	if err != nil {
		t.Skipf("Console API not running: %v", err)
		return
	}
	defer listResp.Body.Close()
	listBody, _ := io.ReadAll(listResp.Body)
	var listed ListAgentsResp
	if err := json.Unmarshal(listBody, &listed); err != nil {
		t.Fatalf("Failed to parse list response: %v", err)
	}
	if listed.Code != 0 || len(listed.Data.Agents) == 0 {
		t.Skip("No agents available to test GetAgent")
		return
	}
	agentID := listed.Data.Agents[0]["agent_id"].(string)

	// Test GET /agents/:agent_id
	resp, err := http.Get(fmt.Sprintf("%s/console/api/v1/agents/%s", testutil.ConsoleBaseURL, agentID))
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		t.Skip("console api is running old binary without GET /agents/:agent_id route")
		return
	}

	body, _ := io.ReadAll(resp.Body)
	var result GetAgentResp
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}
	if result.Code != 0 {
		t.Fatalf("Expected code 0, got %d: %s", result.Code, result.Msg)
	}
	if result.Data == nil || result.Data.Agent["agent_id"] != agentID {
		t.Fatalf("Expected agent_id=%s in response, got %v", agentID, result.Data)
	}
	t.Logf("GetAgent %s: name=%v email=%v", agentID, result.Data.Agent["agent_name"], result.Data.Agent["email"])
}

func TestConsoleGetAgentNotFound(t *testing.T) {
	resp, err := http.Get(fmt.Sprintf("%s/console/api/v1/agents/999999999999", testutil.ConsoleBaseURL))
	if err != nil {
		t.Skipf("Console API not running: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		t.Skip("console api is running old binary without GET /agents/:agent_id route")
		return
	}

	body, _ := io.ReadAll(resp.Body)
	var result GetAgentResp
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}
	if result.Code == 0 {
		t.Fatalf("Expected error code for non-existent agent, got code=0")
	}
	t.Logf("GetAgent not found: code=%d msg=%s", result.Code, result.Msg)
}
```

- [ ] **Step 3: Add TestConsoleUpdateItemStatus test**

Add after the previous tests:

```go
func TestConsoleUpdateItemStatus(t *testing.T) {
	// Get an item_id from the list endpoint
	listResp, err := http.Get(fmt.Sprintf("%s/console/api/v1/items?page=1&page_size=1", testutil.ConsoleBaseURL))
	if err != nil {
		t.Skipf("Console API not running: %v", err)
		return
	}
	defer listResp.Body.Close()
	listBody, _ := io.ReadAll(listResp.Body)
	var listed ListItemsResp
	if err := json.Unmarshal(listBody, &listed); err != nil {
		t.Fatalf("Failed to parse list response: %v", err)
	}
	if listed.Code != 0 || len(listed.Data.Items) == 0 {
		t.Skip("No items available to test UpdateItem")
		return
	}
	itemID := listed.Data.Items[0]["item_id"].(string)
	originalStatus := listed.Data.Items[0]["status"]

	// Update status to 4 (discarded)
	updateResp := testutil.DoConsoleJSONRequest(t, http.MethodPut, "/console/api/v1/items/"+itemID, map[string]interface{}{
		"status": 4,
	})
	if updateResp == nil {
		return
	}
	var updated UpdateItemResp
	testutil.MustDecodeResp(t, updateResp, &updated)
	if updated.Code != 0 {
		t.Fatalf("Update failed: code=%d msg=%s", updated.Code, updated.Msg)
	}
	if updated.Data == nil {
		t.Fatalf("Expected data in response")
	}
	updatedStatus, _ := updated.Data.Item["status"].(float64)
	if int32(updatedStatus) != 4 {
		t.Fatalf("Expected status=4 after update, got %v", updated.Data.Item["status"])
	}
	t.Logf("UpdateItem %s: status changed to 4 (discarded)", itemID)

	// Restore original status
	if originalStatus != nil {
		origVal := int32(originalStatus.(float64))
		testutil.DoConsoleJSONRequest(t, http.MethodPut, "/console/api/v1/items/"+itemID, map[string]interface{}{
			"status": origVal,
		})
	}
}

func TestConsoleUpdateItemNotFound(t *testing.T) {
	updateResp := testutil.DoConsoleJSONRequest(t, http.MethodPut, "/console/api/v1/items/999999999999", map[string]interface{}{
		"status": 3,
	})
	if updateResp == nil {
		return
	}
	var result UpdateItemResp
	testutil.MustDecodeResp(t, updateResp, &result)
	if result.Code == 0 {
		t.Fatalf("Expected error code for non-existent item, got code=0")
	}
	t.Logf("UpdateItem not found: code=%d msg=%s", result.Code, result.Msg)
}
```

- [ ] **Step 4: Add TestConsoleListItemsExcludeEmailSuffixes test**

```go
func TestConsoleListItemsExcludeEmailSuffixes(t *testing.T) {
	// First get total without filter
	resp1, err := http.Get(fmt.Sprintf("%s/console/api/v1/items?page=1&page_size=1", testutil.ConsoleBaseURL))
	if err != nil {
		t.Skipf("Console API not running: %v", err)
		return
	}
	defer resp1.Body.Close()
	body1, _ := io.ReadAll(resp1.Body)
	var all ListItemsResp
	if err := json.Unmarshal(body1, &all); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}
	if all.Code != 0 {
		t.Fatalf("Expected code 0, got %d: %s", all.Code, all.Msg)
	}

	// Now filter with a suffix — should still succeed (may or may not reduce count)
	resp2, err := http.Get(fmt.Sprintf("%s/console/api/v1/items?page=1&page_size=10&exclude_email_suffixes=@nonexistent-domain-xyz.com", testutil.ConsoleBaseURL))
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	defer resp2.Body.Close()
	body2, _ := io.ReadAll(resp2.Body)
	var filtered ListItemsResp
	if err := json.Unmarshal(body2, &filtered); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}
	if filtered.Code != 0 {
		t.Fatalf("Expected code 0, got %d: %s", filtered.Code, filtered.Msg)
	}

	// With a non-matching suffix, total should be the same
	if filtered.Data.Total != all.Data.Total {
		t.Fatalf("Expected same total when excluding non-existent domain, got %d vs %d", all.Data.Total, filtered.Data.Total)
	}

	t.Logf("exclude_email_suffixes filter works: total=%d (same as unfiltered)", filtered.Data.Total)
}
```

- [ ] **Step 5: Build tests (compile check)**

```bash
go build ./tests/console/
```

Expected: Build succeeds.

- [ ] **Step 6: Commit**

```bash
git add tests/console/console_test.go
git commit -m "test(console): add tests for GetAgent, UpdateItem, exclude_email_suffixes"
```

---

### Task 5: Build, rebuild console, run tests

- [ ] **Step 1: Build console binary**

```bash
bash console/console_api/scripts/build.sh
```

Expected: `build/console` binary created successfully.

- [ ] **Step 2: Run console tests**

```bash
go test -v ./tests/console/ -run "TestConsoleGetAgent|TestConsoleUpdateItem|TestConsoleListItemsExcludeEmailSuffixes" -count=1
```

Expected: All new tests pass (or skip gracefully if console API isn't running).

- [ ] **Step 3: Run all console tests to check no regressions**

```bash
go test -v ./tests/console/ -count=1
```

Expected: All tests pass.

- [ ] **Step 4: Commit (if any fixes were needed)**

Only if fixes were made in previous steps.

---

### Task 6: Frontend — dataProvider update

**Files:**
- Modify: `console/webapp/src/dataProvider.ts`

- [ ] **Step 1: Update getOne to handle console API response format**

The current `getOne` returns `{ data }` raw. The console API returns `{ code, msg, data: { agent: {...} } }`. Update it to unwrap:

Replace the `getOne` method in `console/webapp/src/dataProvider.ts`:

```typescript
  getOne: async ({ resource, id }) => {
    const url = `${apiUrl}/${resource}/${id}`;
    const { data } = await httpClient.get(url);
    if (data.code !== 0 || !data.data) {
      throw new Error(data.msg || "API request failed");
    }
    // Extract the single resource object from the data wrapper
    // API format: { code, msg, data: { agent: {...} } } or { code, msg, data: { item: {...} } }
    const inner = data.data;
    const singular: Record<string, string> = {
      agents: "agent",
      items: "item",
      "milestone-rules": "rule",
      "system-notifications": "notification",
    };
    const key = singular[resource];
    return { data: key && inner[key] ? inner[key] : inner };
  },
```

- [ ] **Step 2: Update update method to use PUT instead of PATCH**

Replace the `update` method:

```typescript
  update: async ({ resource, id, variables }) => {
    const url = `${apiUrl}/${resource}/${id}`;
    const { data } = await httpClient.put(url, variables);
    if (data.code !== 0) {
      throw new Error(data.msg || "Update failed");
    }
    return { data: data.data };
  },
```

- [ ] **Step 3: Commit**

```bash
git add console/webapp/src/dataProvider.ts
git commit -m "feat(console): update dataProvider getOne/update for console API format"
```

---

### Task 7: Frontend — Items page enhancements

**Files:**
- Modify: `console/webapp/src/pages/items/list.tsx`

- [ ] **Step 1: Add imports and agent detail state**

Update the imports at the top of `console/webapp/src/pages/items/list.tsx`:

```typescript
import { useList } from "@refinedev/core";
import { List } from "@refinedev/antd";
import { Descriptions, Modal, Select, Table, Input, Tag, Tooltip, Typography, message } from "antd";
import type { ColumnsType } from "antd/es/table";
import axios from "axios";
import { useState } from "react";

import { consoleApiUrl } from "../../config";
```

Add the `Agent` interface after the `Item` interface:

```typescript
interface Agent {
  agent_id: string;
  agent_name: string;
  email: string;
  bio: string;
  created_at: number;
  updated_at: number;
  profile_status: number | null;
  profile_keywords: string[];
}
```

- [ ] **Step 2: Add state variables inside ItemList component**

Add after the existing state variables (`pageSize`):

```typescript
  const [messageApi, contextHolder] = message.useMessage();

  // Agent detail modal
  const [agentModalOpen, setAgentModalOpen] = useState(false);
  const [agentDetail, setAgentDetail] = useState<Agent | null>(null);
  const [agentLoading, setAgentLoading] = useState(false);

  // Exclude email suffixes filter
  const [excludeSuffixes, setExcludeSuffixes] = useState<string[]>([]);

  // Status update loading tracker
  const [updatingItemId, setUpdatingItemId] = useState<string | null>(null);
```

- [ ] **Step 3: Add helper functions**

Add after the state variables:

```typescript
  const fetchAgentDetail = async (agentId: string) => {
    setAgentLoading(true);
    setAgentModalOpen(true);
    try {
      const { data } = await axios.get(`${consoleApiUrl}/agents/${agentId}`);
      if (data.code !== 0) throw new Error(data.msg || "Failed to fetch agent");
      setAgentDetail(data.data.agent);
    } catch (error) {
      messageApi.error(error instanceof Error ? error.message : "Failed to fetch agent details");
      setAgentModalOpen(false);
    } finally {
      setAgentLoading(false);
    }
  };

  const handleStatusChange = async (itemId: string, newStatus: number) => {
    setUpdatingItemId(itemId);
    try {
      const { data } = await axios.put(`${consoleApiUrl}/items/${itemId}`, { status: newStatus });
      if (data.code !== 0) throw new Error(data.msg || "Update failed");
      messageApi.success("Status updated");
      await query.refetch();
    } catch (error) {
      messageApi.error(error instanceof Error ? error.message : "Failed to update status");
    } finally {
      setUpdatingItemId(null);
    }
  };
```

- [ ] **Step 4: Update filters to include exclude_email_suffixes**

Update the `useList` filters array:

```typescript
  const { query } = useList<Item>({
    resource: "items",
    pagination: {
      currentPage: current,
      pageSize,
      mode: "server",
    },
    filters: [
      ...(statusFilter !== undefined ? [{ field: "status", operator: "eq" as const, value: statusFilter }] : []),
      ...(keywordFilter ? [{ field: "keyword", operator: "contains" as const, value: keywordFilter }] : []),
      ...(excludeSuffixes.length > 0 ? [{ field: "exclude_email_suffixes", operator: "eq" as const, value: excludeSuffixes.join(",") }] : []),
    ],
  });
```

- [ ] **Step 5: Update the author_agent_id column to be clickable**

Replace the `author_agent_id` column definition:

```typescript
    {
      title: "Author Agent ID",
      dataIndex: "author_agent_id",
      key: "author_agent_id",
      width: 130,
      render: (agentId: string) => (
        <a onClick={() => fetchAgentDetail(agentId)}>{agentId}</a>
      ),
    },
```

- [ ] **Step 6: Update the status column to be an inline dropdown**

Replace the `status` column definition:

```typescript
    {
      title: "Status",
      dataIndex: "status",
      key: "status",
      width: 140,
      render: (status: number, record: Item) => (
        <Select
          value={status}
          onChange={(value) => void handleStatusChange(record.item_id, value)}
          loading={updatingItemId === record.item_id}
          disabled={updatingItemId === record.item_id}
          style={{ width: 130 }}
          options={Object.entries(statusMap).map(([val, { label }]) => ({
            label,
            value: Number(val),
          }))}
        />
      ),
    },
```

- [ ] **Step 7: Add exclude email suffix filter to header**

Update the `headerButtons` in the `<List>` component. Add the `<Select mode="tags">` after the existing status filter `<Select>`:

```typescript
      headerButtons={
        <>
          <Input.Search
            placeholder="Search keywords"
            allowClear
            onSearch={(value) => {
              setKeywordFilter(value);
              setCurrent(1);
            }}
            style={{ width: 200, marginRight: 8 }}
          />
          <Select
            placeholder="Filter by status"
            allowClear
            onChange={(value) => {
              setStatusFilter(value);
              setCurrent(1);
            }}
            style={{ width: 150, marginRight: 8 }}
            options={[
              { label: "Pending", value: 0 },
              { label: "Processing", value: 1 },
              { label: "Failed", value: 2 },
              { label: "Completed", value: 3 },
              { label: "Discarded", value: 4 },
            ]}
          />
          <Select
            mode="tags"
            placeholder="Exclude email suffixes"
            value={excludeSuffixes}
            onChange={(values) => {
              setExcludeSuffixes(values);
              setCurrent(1);
            }}
            style={{ minWidth: 220 }}
            tokenSeparators={[","]}
          />
        </>
      }
```

Note: also add `marginRight: 8` to the existing status filter's style (it was missing before).

- [ ] **Step 8: Add agent detail modal and contextHolder**

Wrap the component return with `<>` fragment and add `{contextHolder}` and the `<Modal>` after `</List>`:

```typescript
  return (
    <>
      {contextHolder}
      <List
        headerButtons={/* ... as above ... */}
      >
        <Table /* ... as existing ... */ />
      </List>

      <Modal
        title={agentDetail ? `Agent: ${agentDetail.agent_name || agentDetail.agent_id}` : "Agent Details"}
        open={agentModalOpen}
        onCancel={() => {
          setAgentModalOpen(false);
          setAgentDetail(null);
        }}
        footer={null}
        loading={agentLoading}
        destroyOnHidden
      >
        {agentDetail && (
          <Descriptions column={1} bordered size="small">
            <Descriptions.Item label="ID">{agentDetail.agent_id}</Descriptions.Item>
            <Descriptions.Item label="Name">{agentDetail.agent_name}</Descriptions.Item>
            <Descriptions.Item label="Email">{agentDetail.email}</Descriptions.Item>
            <Descriptions.Item label="Bio">{agentDetail.bio || "-"}</Descriptions.Item>
            <Descriptions.Item label="Profile Status">
              {agentDetail.profile_status !== null && agentDetail.profile_status !== undefined
                ? (() => {
                    const statusLabels: Record<number, string> = { 0: "Pending", 1: "Processing", 2: "Failed", 3: "Completed" };
                    return statusLabels[agentDetail.profile_status] ?? String(agentDetail.profile_status);
                  })()
                : "-"}
            </Descriptions.Item>
            <Descriptions.Item label="Profile Keywords">
              {agentDetail.profile_keywords?.length > 0
                ? agentDetail.profile_keywords.map((kw) => <Tag key={kw}>{kw}</Tag>)
                : "-"}
            </Descriptions.Item>
            <Descriptions.Item label="Created At">{formatTimestamp(agentDetail.created_at)}</Descriptions.Item>
            <Descriptions.Item label="Updated At">{formatTimestamp(agentDetail.updated_at)}</Descriptions.Item>
          </Descriptions>
        )}
      </Modal>
    </>
  );
```

- [ ] **Step 9: Verify frontend builds**

```bash
cd console/webapp && npx tsc --noEmit
```

Expected: No type errors.

- [ ] **Step 10: Commit**

```bash
git add console/webapp/src/pages/items/list.tsx
git commit -m "feat(console): add agent detail popup, email suffix filter, inline status update"
```

---

### Task 8: Documentation update

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: Update Console API Endpoints table**

Add two new rows to the Console API Endpoints table in `CLAUDE.md`:

| Method | Path | Parameters | Description |
|--------|------|------------|-------------|
| GET | `/console/api/v1/agents/:agent_id` | — | Get agent detail by ID |
| PUT | `/console/api/v1/items/:item_id` | JSON body (partial update, e.g. `{ "status": 3 }`) | Update item fields |

Also update the existing `GET /console/api/v1/items` row's Parameters to include `exclude_email_suffixes`.

Update the Parameter descriptions section to add:
- `exclude_email_suffixes`: Comma-separated email suffixes to exclude (optional, e.g. `@test.com,@bot.ai`)

- [ ] **Step 2: Commit**

```bash
git add CLAUDE.md
git commit -m "docs: update CLAUDE.md with new console API endpoints"
```

---

### Task 9: Final verification

- [ ] **Step 1: Build entire project**

```bash
bash console/console_api/scripts/build.sh
```

Expected: Build succeeds.

- [ ] **Step 2: Run full console test suite**

```bash
go test -v ./tests/console/ -count=1
```

Expected: All tests pass.

- [ ] **Step 3: Frontend build check**

```bash
cd console/webapp && pnpm build
```

Expected: Production build succeeds.
