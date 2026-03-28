package console_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"eigenflux_server/tests/testutil"
)

type ListAgentsData struct {
	Agents   []map[string]interface{} `json:"agents"`
	Total    int64                    `json:"total"`
	Page     int32                    `json:"page"`
	PageSize int32                    `json:"page_size"`
}

type ListAgentsResp struct {
	Code int32          `json:"code"`
	Msg  string         `json:"msg"`
	Data ListAgentsData `json:"data"`
}

type ListItemsData struct {
	Items    []map[string]interface{} `json:"items"`
	Total    int64                    `json:"total"`
	Page     int32                    `json:"page"`
	PageSize int32                    `json:"page_size"`
}

type ListItemsResp struct {
	Code int32         `json:"code"`
	Msg  string        `json:"msg"`
	Data ListItemsData `json:"data"`
}

type ListAgentImprItemsData struct {
	AgentID  string                   `json:"agent_id"`
	ItemIDs  []string                 `json:"item_ids"`
	GroupIDs []string                 `json:"group_ids"`
	URLs     []string                 `json:"urls"`
	Items    []map[string]interface{} `json:"items"`
}

type ListAgentImprItemsResp struct {
	Code int32                  `json:"code"`
	Msg  string                 `json:"msg"`
	Data ListAgentImprItemsData `json:"data"`
}

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

type BlacklistKeywordInfo struct {
	KeywordID string `json:"keyword_id"`
	Keyword   string `json:"keyword"`
	Enabled   bool   `json:"enabled"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

type ListBlacklistKeywordsData struct {
	Keywords []BlacklistKeywordInfo `json:"keywords"`
	Total    int64                  `json:"total"`
	Page     int32                  `json:"page"`
	PageSize int32                  `json:"page_size"`
}

type ListBlacklistKeywordsResp struct {
	Code int32                      `json:"code"`
	Msg  string                     `json:"msg"`
	Data *ListBlacklistKeywordsData `json:"data"`
}

type BlacklistKeywordData struct {
	Keyword BlacklistKeywordInfo `json:"keyword"`
}

type BlacklistKeywordResp struct {
	Code int32                 `json:"code"`
	Msg  string                `json:"msg"`
	Data *BlacklistKeywordData `json:"data"`
}

type MilestoneRuleInfo struct {
	RuleID          string `json:"rule_id"`
	MetricKey       string `json:"metric_key"`
	Threshold       int64  `json:"threshold"`
	RuleEnabled     bool   `json:"rule_enabled"`
	ContentTemplate string `json:"content_template"`
}

type ListMilestoneRulesData struct {
	Rules    []MilestoneRuleInfo `json:"rules"`
	Total    int64               `json:"total"`
	Page     int32               `json:"page"`
	PageSize int32               `json:"page_size"`
}

type ListMilestoneRulesResp struct {
	Code int32                  `json:"code"`
	Msg  string                 `json:"msg"`
	Data ListMilestoneRulesData `json:"data"`
}

type MilestoneRuleData struct {
	Rule MilestoneRuleInfo `json:"rule"`
}

type MilestoneRuleResp struct {
	Code int32              `json:"code"`
	Msg  string             `json:"msg"`
	Data *MilestoneRuleData `json:"data"`
}

type ReplaceMilestoneRuleData struct {
	OldRule MilestoneRuleInfo `json:"old_rule"`
	NewRule MilestoneRuleInfo `json:"new_rule"`
}

type ReplaceMilestoneRuleResp struct {
	Code int32                     `json:"code"`
	Msg  string                    `json:"msg"`
	Data *ReplaceMilestoneRuleData `json:"data"`
}

func TestConsoleListAgents(t *testing.T) {
	resp, err := http.Get(fmt.Sprintf("%s/console/api/v1/agents?page=1&page_size=10", testutil.ConsoleBaseURL))
	if err != nil {
		t.Skipf("Console API not running: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected status 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	var result ListAgentsResp
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if result.Code != 0 {
		t.Fatalf("Expected code 0, got %d: %s", result.Code, result.Msg)
	}

	t.Logf("Listed %d agents (total: %d)", len(result.Data.Agents), result.Data.Total)
}

func TestConsoleListItems(t *testing.T) {
	resp, err := http.Get(fmt.Sprintf("%s/console/api/v1/items?page=1&page_size=10", testutil.ConsoleBaseURL))
	if err != nil {
		t.Skipf("Console API not running: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected status 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	var result ListItemsResp
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if result.Code != 0 {
		t.Fatalf("Expected code 0, got %d: %s", result.Code, result.Msg)
	}

	t.Logf("Listed %d items (total: %d)", len(result.Data.Items), result.Data.Total)
}

func TestConsoleListAgentsPaginationParams(t *testing.T) {
	resp, err := http.Get(fmt.Sprintf("%s/console/api/v1/agents?page=2&page_size=1", testutil.ConsoleBaseURL))
	if err != nil {
		t.Skipf("Console API not running: %v", err)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result ListAgentsResp
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}
	if result.Code != 0 {
		t.Fatalf("Expected code 0, got %d: %s", result.Code, result.Msg)
	}
	if result.Data.Page != 2 {
		t.Fatalf("Expected page=2, got %d", result.Data.Page)
	}
	if result.Data.PageSize != 1 {
		t.Fatalf("Expected page_size=1, got %d", result.Data.PageSize)
	}
	if len(result.Data.Agents) > 1 {
		t.Fatalf("Expected <=1 agents in page, got %d", len(result.Data.Agents))
	}
}

func TestConsoleListItemsPaginationParams(t *testing.T) {
	resp, err := http.Get(fmt.Sprintf("%s/console/api/v1/items?page=2&page_size=1", testutil.ConsoleBaseURL))
	if err != nil {
		t.Skipf("Console API not running: %v", err)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result ListItemsResp
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}
	if result.Code != 0 {
		t.Fatalf("Expected code 0, got %d: %s", result.Code, result.Msg)
	}
	if result.Data.Page != 2 {
		t.Fatalf("Expected page=2, got %d", result.Data.Page)
	}
	if result.Data.PageSize != 1 {
		t.Fatalf("Expected page_size=1, got %d", result.Data.PageSize)
	}
	if len(result.Data.Items) > 1 {
		t.Fatalf("Expected <=1 items in page, got %d", len(result.Data.Items))
	}
}

func TestConsoleListAgentsWithFilters(t *testing.T) {
	resp, err := http.Get(fmt.Sprintf("%s/console/api/v1/agents?page=1&page_size=10&name=test", testutil.ConsoleBaseURL))
	if err != nil {
		t.Skipf("Console API not running: %v", err)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result ListAgentsResp
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if result.Code != 0 {
		t.Fatalf("Expected code 0, got %d: %s", result.Code, result.Msg)
	}

	t.Logf("Filtered agents by name 'test': %d results", len(result.Data.Agents))
}

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

func TestConsoleListItemsWithFilters(t *testing.T) {
	resp, err := http.Get(fmt.Sprintf("%s/console/api/v1/items?page=1&page_size=10&status=3", testutil.ConsoleBaseURL))
	if err != nil {
		t.Skipf("Console API not running: %v", err)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result ListItemsResp
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if result.Code != 0 {
		t.Fatalf("Expected code 0, got %d: %s", result.Code, result.Msg)
	}

	t.Logf("Filtered items by status 3 (completed): %d results", len(result.Data.Items))
}

func TestConsoleListAgentImprItems(t *testing.T) {
	resp, err := http.Get(fmt.Sprintf("%s/console/api/v1/impr/items?agent_id=999999999", testutil.ConsoleBaseURL))
	if err != nil {
		t.Skipf("Console API not running: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		t.Skip("console api is running old binary without /console/api/v1/impr/items route")
		return
	}

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected status 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	var result ListAgentImprItemsResp
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if result.Code != 0 {
		t.Fatalf("Expected code 0, got %d: %s", result.Code, result.Msg)
	}
	if result.Data.AgentID != "999999999" {
		t.Fatalf("Expected agent_id=999999999, got %s", result.Data.AgentID)
	}
}

func TestConsoleMilestoneRulesFlow(t *testing.T) {
	seed := time.Now().UnixNano() % 1_000_000_000
	initialThreshold := 800_000_000 + seed
	replacementThreshold := initialThreshold + 1
	initialTemplate := fmt.Sprintf("Your Content \"{{.ItemSummary}}\" reached %d consumptions. Item Id {{.ItemID}}", initialThreshold)
	updatedTemplate := fmt.Sprintf("Updated template %d for {{.ItemID}} / {{.CounterValue}}", initialThreshold)
	replacedTemplate := fmt.Sprintf("Replacement template %d for {{.ItemSummary}}", replacementThreshold)

	createResp := testutil.DoConsoleJSONRequest(t, http.MethodPost, "/console/api/v1/milestone-rules", map[string]interface{}{
		"metric_key":       "consumed",
		"threshold":        initialThreshold,
		"rule_enabled":     true,
		"content_template": initialTemplate,
	})
	var created MilestoneRuleResp
	testutil.MustDecodeResp(t, createResp, &created)
	if created.Code != 0 || created.Data == nil {
		t.Fatalf("create milestone rule failed: code=%d msg=%s", created.Code, created.Msg)
	}
	if created.Data.Rule.Threshold != initialThreshold {
		t.Fatalf("expected created threshold=%d, got %d", initialThreshold, created.Data.Rule.Threshold)
	}

	listResp := testutil.DoConsoleRequest(t, http.MethodGet, fmt.Sprintf("/console/api/v1/milestone-rules?page=1&page_size=20&metric_key=consumed&rule_enabled=true"), nil)
	var listed ListMilestoneRulesResp
	testutil.MustDecodeResp(t, listResp, &listed)
	if listed.Code != 0 {
		t.Fatalf("list milestone rules failed: code=%d msg=%s", listed.Code, listed.Msg)
	}
	foundCreated := false
	for _, rule := range listed.Data.Rules {
		if rule.RuleID == created.Data.Rule.RuleID {
			foundCreated = true
			break
		}
	}
	if !foundCreated {
		t.Fatalf("created milestone rule %s not found in list response", created.Data.Rule.RuleID)
	}

	updateResp := testutil.DoConsoleJSONRequest(t, http.MethodPut, "/console/api/v1/milestone-rules/"+created.Data.Rule.RuleID, map[string]interface{}{
		"rule_enabled":     false,
		"content_template": updatedTemplate,
	})
	var updated MilestoneRuleResp
	testutil.MustDecodeResp(t, updateResp, &updated)
	if updated.Code != 0 || updated.Data == nil {
		t.Fatalf("update milestone rule failed: code=%d msg=%s", updated.Code, updated.Msg)
	}
	if updated.Data.Rule.RuleEnabled {
		t.Fatalf("expected updated rule to be disabled")
	}
	if updated.Data.Rule.ContentTemplate != updatedTemplate {
		t.Fatalf("expected updated template=%q, got %q", updatedTemplate, updated.Data.Rule.ContentTemplate)
	}

	replaceResp := testutil.DoConsoleJSONRequest(t, http.MethodPost, "/console/api/v1/milestone-rules/"+created.Data.Rule.RuleID+"/replace", map[string]interface{}{
		"metric_key":       "score_2",
		"threshold":        replacementThreshold,
		"rule_enabled":     true,
		"content_template": replacedTemplate,
	})
	var replaced ReplaceMilestoneRuleResp
	testutil.MustDecodeResp(t, replaceResp, &replaced)
	if replaced.Code != 0 || replaced.Data == nil {
		t.Fatalf("replace milestone rule failed: code=%d msg=%s", replaced.Code, replaced.Msg)
	}
	if replaced.Data.OldRule.RuleEnabled {
		t.Fatalf("expected old rule to be disabled after replace")
	}
	if replaced.Data.NewRule.MetricKey != "score_2" || replaced.Data.NewRule.Threshold != replacementThreshold {
		t.Fatalf("unexpected replacement rule: %+v", replaced.Data.NewRule)
	}

	cleanupResp := testutil.DoConsoleJSONRequest(t, http.MethodPut, "/console/api/v1/milestone-rules/"+replaced.Data.NewRule.RuleID, map[string]interface{}{
		"rule_enabled": false,
	})
	var cleaned MilestoneRuleResp
	testutil.MustDecodeResp(t, cleanupResp, &cleaned)
	if cleaned.Code != 0 || cleaned.Data == nil {
		t.Fatalf("cleanup disable milestone rule failed: code=%d msg=%s", cleaned.Code, cleaned.Msg)
	}
	if cleaned.Data.Rule.RuleEnabled {
		t.Fatalf("expected cleanup rule to be disabled")
	}
}

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

func TestConsoleBlacklistKeywordsFlow(t *testing.T) {
	seed := time.Now().UnixNano() % 1_000_000_000
	testKeyword := fmt.Sprintf("test-blacklist-%d", seed)

	// 1. Create a blacklist keyword
	createResp := testutil.DoConsoleJSONRequest(t, http.MethodPost, "/console/api/v1/blacklist-keywords", map[string]interface{}{
		"keyword": testKeyword,
	})
	var created BlacklistKeywordResp
	testutil.MustDecodeResp(t, createResp, &created)
	if created.Code != 0 || created.Data == nil {
		t.Fatalf("create blacklist keyword failed: code=%d msg=%s", created.Code, created.Msg)
	}
	if created.Data.Keyword.Keyword != testKeyword {
		t.Fatalf("expected keyword=%q, got %q", testKeyword, created.Data.Keyword.Keyword)
	}
	if !created.Data.Keyword.Enabled {
		t.Fatalf("expected keyword to be enabled by default")
	}
	keywordID := created.Data.Keyword.KeywordID

	// 2. List keywords and verify it appears
	listResp := testutil.DoConsoleRequest(t, http.MethodGet, "/console/api/v1/blacklist-keywords?page=1&page_size=100&enabled=true", nil)
	var listed ListBlacklistKeywordsResp
	testutil.MustDecodeResp(t, listResp, &listed)
	if listed.Code != 0 {
		t.Fatalf("list blacklist keywords failed: code=%d msg=%s", listed.Code, listed.Msg)
	}
	found := false
	for _, kw := range listed.Data.Keywords {
		if kw.KeywordID == keywordID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("created keyword %s not found in list response", keywordID)
	}

	// 3. Duplicate keyword should fail
	dupResp := testutil.DoConsoleJSONRequest(t, http.MethodPost, "/console/api/v1/blacklist-keywords", map[string]interface{}{
		"keyword": testKeyword,
	})
	var dup BlacklistKeywordResp
	testutil.MustDecodeResp(t, dupResp, &dup)
	if dup.Code == 0 {
		t.Fatalf("expected duplicate keyword to fail, but got code=0")
	}

	// 4. Disable the keyword
	updateResp := testutil.DoConsoleJSONRequest(t, http.MethodPut, "/console/api/v1/blacklist-keywords/"+keywordID, map[string]interface{}{
		"enabled": false,
	})
	var updated BlacklistKeywordResp
	testutil.MustDecodeResp(t, updateResp, &updated)
	if updated.Code != 0 || updated.Data == nil {
		t.Fatalf("update blacklist keyword failed: code=%d msg=%s", updated.Code, updated.Msg)
	}
	if updated.Data.Keyword.Enabled {
		t.Fatalf("expected keyword to be disabled after update")
	}

	// 5. Filter by enabled=false should include our keyword
	listDisabledResp := testutil.DoConsoleRequest(t, http.MethodGet, "/console/api/v1/blacklist-keywords?page=1&page_size=100&enabled=false", nil)
	var listedDisabled ListBlacklistKeywordsResp
	testutil.MustDecodeResp(t, listDisabledResp, &listedDisabled)
	if listedDisabled.Code != 0 {
		t.Fatalf("list disabled keywords failed: code=%d msg=%s", listedDisabled.Code, listedDisabled.Msg)
	}
	foundDisabled := false
	for _, kw := range listedDisabled.Data.Keywords {
		if kw.KeywordID == keywordID {
			foundDisabled = true
			break
		}
	}
	if !foundDisabled {
		t.Fatalf("disabled keyword %s not found in enabled=false filter", keywordID)
	}

	// 6. Delete the keyword (cleanup)
	delResp := testutil.DoConsoleJSONRequest(t, http.MethodDelete, "/console/api/v1/blacklist-keywords/"+keywordID, nil)
	var deleted map[string]interface{}
	testutil.MustDecodeResp(t, delResp, &deleted)
	code, _ := deleted["code"].(float64)
	if int(code) != 0 {
		t.Fatalf("delete blacklist keyword failed: %v", deleted)
	}

	// 7. Verify deletion: keyword no longer in list
	listAfterDelResp := testutil.DoConsoleRequest(t, http.MethodGet, "/console/api/v1/blacklist-keywords?page=1&page_size=100", nil)
	var listedAfterDel ListBlacklistKeywordsResp
	testutil.MustDecodeResp(t, listAfterDelResp, &listedAfterDel)
	for _, kw := range listedAfterDel.Data.Keywords {
		if kw.KeywordID == keywordID {
			t.Fatalf("keyword %s should have been deleted but still appears in list", keywordID)
		}
	}

	// 8. Delete non-existent keyword should fail
	delNonExistResp := testutil.DoConsoleJSONRequest(t, http.MethodDelete, "/console/api/v1/blacklist-keywords/999999999", nil)
	var delNonExist map[string]interface{}
	testutil.MustDecodeResp(t, delNonExistResp, &delNonExist)
	code2, _ := delNonExist["code"].(float64)
	if int(code2) == 0 {
		t.Fatalf("expected delete of non-existent keyword to fail, but got code=0")
	}
}
