package cmd

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/cache"
	"cli.eigenflux.ai/internal/controlcontext"
	"cli.eigenflux.ai/internal/feedevent"
	"cli.eigenflux.ai/internal/output"
)

// Model the large heartbeat page: ten authors, eight intents, five actions per item.
func largeFeedBatchFixture(t *testing.T) json.RawMessage {
	t.Helper()
	var page map[string]any
	if err := json.Unmarshal(batchTestPage("large", 10), &page); err != nil {
		t.Fatal(err)
	}
	intents := []any{}
	for i := 0; i < 8; i++ {
		intents = append(intents, map[string]any{"intent_id": fmt.Sprint(i), "then": fmt.Sprint(i) + strings.Repeat("结合用户确认的目标评估此信息，核查来源并提出具体可执行的下一步。", 12)})
	}
	page["control_context_snapshot"] = map[string]any{"context_revision": 1, "intent_actions": intents}
	page["output_contract"] = strings.Repeat("SERVED CONTRACT: score every item before presenting.\n", 200)
	cards := map[string]any{}
	discovery := []any{}
	for i, value := range page["items"].([]any) {
		item := value.(map[string]any)
		author := fmt.Sprintf("author-%d", i)
		item["author_identity"] = map[string]any{"agent_id": author}
		// Served shapes differ: Feed previews add a truncation flag, discovery previews carry text only.
		text := fmt.Sprint(i) + strings.Repeat("这是一条与当前工作有关的信息，需要核查来源后判断价值。", 10)
		item["preview"] = map[string]any{"text": text, "truncated": false}
		cards[author] = map[string]any{"bio": strings.Repeat("作者提供开发工具与研究进展。", 15)}
		discovery = append(discovery, map[string]any{"item_id": item["item_id"], "preview": map[string]any{"text": text}, "rank": i})
		matched, actions := []any{}, []any{}
		for j := 0; j < 5; j++ {
			intent := intents[(i+j)%8].(map[string]any)
			matched = append(matched, intent["intent_id"])
			actions = append(actions, map[string]any{"instruction": intent["then"], "type": "research", "requires_user_confirmation": true})
		}
		item["intent_match"] = map[string]any{"matched_intent_ids": matched}
		item["recommended_actions"] = actions
	}
	page["agent_card_updates"] = cards
	page["discovery"] = map[string]any{"items": discovery, "impression_id": "large", "input_origin": "feed", "effective_filters": map[string]any{"kind": "broadcast"}}
	raw, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestFeedBatchMetadataAndOutputSize(t *testing.T) {
	page := largeFeedBatchFixture(t)
	for _, source := range []string{"server", "local"} {
		t.Run(source, func(t *testing.T) {
			_, server := runtimeTestConfig(t, "http://127.0.0.1:1", true)
			var original map[string]json.RawMessage
			_ = json.Unmarshal(page, &original)
			if err := controlcontext.Save(server, controlcontext.Snapshot{OwnerAgentID: "agent-1", Revision: 1, Context: original["control_context_snapshot"]}); err != nil {
				t.Fatal(err)
			}
			if err := cache.SaveFeedResponse(server, page); err != nil {
				t.Fatal(err)
			}
			selected := []feedevent.Entry{}
			for _, position := range []int{4, 3, 2, 1, 0} {
				selected = append(selected, feedevent.Entry{ItemID: fmt.Sprint(position + 1), Position: position})
			}
			meta := &localFeedBatch{Source: source, ImpressionID: "large", Handed: 5}
			batch, err := makeFeedBatch(server, page, selected, feedevent.Reports{}, *meta)
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Cards     map[string]any `json:"agent_card_updates"`
				Discovery struct {
					Items        []map[string]any `json:"items"`
					ImpressionID string           `json:"impression_id"`
					Origin       string           `json:"input_origin"`
					Filters      map[string]any   `json:"effective_filters"`
				} `json:"discovery"`
			}
			if err := json.Unmarshal(batch, &got); err != nil {
				t.Fatal(err)
			}
			if len(got.Cards) != 5 || len(got.Discovery.Items) != 5 || got.Discovery.ImpressionID != "large" || got.Discovery.Origin != "feed" || got.Discovery.Filters["kind"] != "broadcast" {
				t.Fatal(got)
			}
			for i, entry := range got.Discovery.Items {
				if entry["item_id"] != selected[i].ItemID || entry["preview"] != nil || got.Cards[fmt.Sprintf("author-%d", selected[i].Position)] == nil {
					t.Fatal(entry, got.Cards)
				}
			}
			cached, err := feedLedger(server).Page("large")
			if err != nil || !bytes.Equal(cached, page) {
				t.Fatalf("cached page changed: %v", err)
			}
			old := formatFlag
			formatFlag = "agent"
			t.Cleanup(func() { formatFlag = old })
			cmd := batchTestCommand(5)
			var buf bytes.Buffer
			cmd.SetOut(&buf)
			if err := renderFeedV2Batch(cmd, server, batch, meta); err != nil {
				t.Fatal(err)
			}
			out := buf.String()
			if len(out) > 32*1024 || strings.Contains(out, "SERVED CONTRACT:") || strings.Contains(out, "author-5") || !strings.Contains(out, `"instruction_ref"`) {
				t.Fatalf("unexpected batch output (%d bytes): %s", len(out), out)
			}
			t.Logf("agent batch: %d bytes", len(out))
			if !strings.Contains(out, "\n"+feedInstructionReference+"\n[EIGENFLUX OUTPUT CONTRACT]") {
				t.Fatal("instruction reference is not directly after the trusted context")
			}
			formatFlag = "json"
			raw, err := captureHeartbeatStdout(t, func() error { return renderFeedV2Batch(cmd, server, batch, meta) })
			var want bytes.Buffer
			output.PrintDataTo(&want, batch, "json")
			if err != nil || raw != want.String() || strings.Contains(raw, `"instruction_ref"`) || !strings.Contains(raw, "SERVED CONTRACT:") {
				t.Fatal("JSON rendering changed", err)
			}
		})
	}
}

func TestFeedBatchMetadataKeepsDistinctPreviewAndDropsEmptyCards(t *testing.T) {
	for _, tc := range []struct {
		name, discovery string
		dropped         bool
	}{
		{"served shapes with equal text", `{"text":"original"}`, true},
		{"different text", `{"text":"different"}`, false},
		{"extra discovery field", `{"text":"original","source":"need"}`, false},
		{"different flag", `{"text":"original","truncated":true}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page := map[string]json.RawMessage{"agent_card_updates": json.RawMessage(`{"other":{}}`), "discovery": json.RawMessage(`{"items":[{"item_id":"1","preview":` + tc.discovery + `}],"other":true}`)}
			batch := []map[string]json.RawMessage{{"item_id": json.RawMessage(`"1"`), "preview": json.RawMessage(`{"text":"original","truncated":false}`)}}
			if err := filterFeedBatchMetadata(page, batch); err != nil {
				t.Fatal(err)
			}
			if _, ok := page["agent_card_updates"]; ok || strings.Contains(string(page["discovery"]), `"preview"`) == tc.dropped || !strings.Contains(string(page["discovery"]), `"other":true`) {
				t.Fatal(string(page["discovery"]))
			}
		})
	}
}

func TestFeedBatchInstructionReferencesRequireCurrentMatchedText(t *testing.T) {
	var feed map[string]any
	if err := json.Unmarshal([]byte(`{"control_context_snapshot":{"intent_actions":[{"intent_id":"1","then":"exact"},{"intent_id":"2","then":"unmatched"}]},"items":[{"intent_match":{"matched_intent_ids":["1","missing"]},"recommended_actions":[{"instruction":"exact","policy":"draft"},{"instruction":"changed"},{"instruction":"unmatched"},{"instruction":" exact"},{"type":"other"}]}]}`), &feed); err != nil {
		t.Fatal(err)
	}
	referenceFeedInstructions(feed)
	actions := feed["items"].([]any)[0].(map[string]any)["recommended_actions"]
	want := []any{map[string]any{"instruction_ref": "1", "policy": "draft"}, map[string]any{"instruction": "changed"}, map[string]any{"instruction": "unmatched"}, map[string]any{"instruction": " exact"}, map[string]any{"type": "other"}}
	if !reflect.DeepEqual(actions, want) {
		t.Fatal(actions)
	}
}

func TestFeedBatchContractFilesAndFallback(t *testing.T) {
	_, server := runtimeTestConfig(t, "http://127.0.0.1:1", true)
	dir := filepath.Join(cache.ServerDataDir(server), "feed-contracts")
	for _, contract := range []string{"first 契约", "second contract"} {
		line := feedBatchContract(server, contract)
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(contract)))[:16]
		path := filepath.Join(dir, hash+".md")
		body, err := os.ReadFile(path)
		if err != nil || string(body) != contract || !strings.Contains(line, fmt.Sprintf("Read %s (sha256 %s, %d bytes) completely", path, hash, len(contract))) {
			t.Fatal(line, string(body), err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatal(entries, err)
	}
	old := filepath.Join(dir, entries[0].Name())
	stamp := time.Now().AddDate(0, 0, -9)
	if err := os.Chtimes(old, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	feedBatchContract(server, "third contract")
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("expired contract remains", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	oldFormat := formatFlag
	formatFlag = "agent"
	t.Cleanup(func() { formatFlag = oldFormat })
	cmd := batchTestCommand(1)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := renderFeedV2Batch(cmd, server, batchTestPage("blocked", 1), &localFeedBatch{}); err != nil || !strings.Contains(buf.String(), "\ntest contract\n") || strings.Contains(buf.String(), "[EIGENFLUX OUTPUT CONTRACT]") {
		t.Fatal(buf.String(), err)
	}
}

func TestFeedBatchNextCommandPreservesAgentFormat(t *testing.T) {
	old := formatFlag
	t.Cleanup(func() { formatFlag = old })
	for _, format := range []string{"", "json", "agent", "table"} {
		formatFlag = format
		if got := strings.Contains(batchNextCommand("test", 2), " --format agent"); got != (resolveFormat() == "agent") {
			t.Fatal(format)
		}
	}
}

func TestFeedBatchUpgradeFetchesInsteadOfReplayingOldExposures(t *testing.T) {
	for _, version := range []string{"0.7.0", "0.7.1", "whole-page"} {
		t.Run(version, func(t *testing.T) {
			pulls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v2/agent-context":
					_, _ = fmt.Fprint(w, `{"data":{"context_revision":1,"control_context":{"context_revision":1}}}`)
				case "/api/v2/runtime/heartbeat":
					_, _ = fmt.Fprint(w, `{"data":{}}`)
				case "/api/v2/feed":
					pulls++
					_, _ = fmt.Fprintf(w, `{"data":%s}`, batchTestPage("fresh", 10))
				default:
					t.Errorf("unexpected %s", r.URL.Path)
				}
			}))
			defer srv.Close()
			_, server := runtimeTestConfig(t, srv.URL, true)
			now := time.Now().UnixMilli()
			for i := 0; i < 8; i++ {
				page := batchTestPage(fmt.Sprintf("old-%d", i), 20)
				if err := cache.SaveFeedResponse(server, page); err != nil {
					t.Fatal(err)
				}
				if err := feedReportStore(server).Update(now, func(state *feedevent.Reports) {
					_, err := recordFeedPage(state, page, now-1000, 0)
					if err != nil {
						t.Fatal(err)
					}
					if version == "0.7.1" {
						for key, r := range state.Entries {
							r.HandedAt, r.HandCount = &now, 1
							state.Entries[key] = r
						}
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			state, err := feedReportStore(server).Read()
			if err != nil {
				t.Fatal(err)
			}
			ledger := feedLedger(server)
			open, backlog := batchCandidates(ledger, state, now)
			if len(open)+len(backlog) != 0 || len(ledger.Pending(state, now)) != 160 {
				t.Fatal(len(open), len(backlog), len(ledger.Pending(state, now)))
			}
			if entry, _ := ledger.LookupHanded("1", state, now); entry.ImpressionID == "" {
				t.Fatal("old exposure lost feedback attribution")
			}
			if _, meta, err := localFeedBatchPayload(server, ledger, 5, false); err != nil || meta != nil {
				t.Fatal(meta, err)
			}
			old := formatFlag
			formatFlag = "json"
			t.Cleanup(func() { formatFlag = old })
			raw, err := captureHeartbeatStdout(t, func() error { return pollFeedV2(batchTestCommand(5), server, "20") })
			if err != nil || pulls != 1 || decodeBatch(t, []byte(raw)).LocalBatch.Source != "server" {
				t.Fatal(raw, pulls, err)
			}
		})
	}
}
