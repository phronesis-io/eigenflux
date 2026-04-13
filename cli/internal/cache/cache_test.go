package cache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// setHomeDir sets EIGENFLUX_HOME to dir/.eigenflux so HomeDir() returns it as-is
// (the suffix check sees ".eigenflux" and skips appending another layer).
// The caller's dir variable is updated to point to the actual home directory.
func setHomeDir(t *testing.T, dir string) string {
	t.Helper()
	efDir := filepath.Join(dir, ".eigenflux")
	old := os.Getenv("EIGENFLUX_HOME")
	os.Setenv("EIGENFLUX_HOME", efDir)
	t.Cleanup(func() {
		if old == "" {
			os.Unsetenv("EIGENFLUX_HOME")
		} else {
			os.Setenv("EIGENFLUX_HOME", old)
		}
	})
	return efDir
}

func TestSaveFeedResponse(t *testing.T) {
	dir := t.TempDir()
	dir = setHomeDir(t, dir)

	rawData := json.RawMessage(`{"items":[{"id":"1","title":"test"}]}`)
	SaveFeedResponse("testserver", rawData)

	today := time.Now().Format(dateFormat)
	broadcastDir := filepath.Join(dir, "servers", "testserver", "data", "broadcasts", today)
	entries, err := os.ReadDir(broadcastDir)
	if err != nil {
		t.Fatalf("expected broadcast dir to exist: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 file in broadcast dir, got %d", len(entries))
	}
	if name := entries[0].Name(); len(name) < 6 || name[:6] != "feeds-" {
		t.Fatalf("expected file name starting with 'feeds-', got %q", name)
	}

	data, err := os.ReadFile(filepath.Join(broadcastDir, entries[0].Name()))
	if err != nil {
		t.Fatalf("failed to read feed file: %v", err)
	}
	if string(data) != string(rawData) {
		t.Fatalf("feed content mismatch:\n  got:  %s\n  want: %s", data, rawData)
	}
}

func TestSavePublishRecord(t *testing.T) {
	dir := t.TempDir()
	dir = setHomeDir(t, dir)

	request := json.RawMessage(`{"content":"hello"}`)
	response := json.RawMessage(`{"item_id":"42"}`)
	SavePublishRecord("testserver", request, response)

	today := time.Now().Format(dateFormat)
	broadcastDir := filepath.Join(dir, "servers", "testserver", "data", "broadcasts", today)
	entries, err := os.ReadDir(broadcastDir)
	if err != nil {
		t.Fatalf("expected broadcast dir to exist: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 file, got %d", len(entries))
	}
	if name := entries[0].Name(); len(name) < 8 || name[:8] != "publish-" {
		t.Fatalf("expected file name starting with 'publish-', got %q", name)
	}

	data, err := os.ReadFile(filepath.Join(broadcastDir, entries[0].Name()))
	if err != nil {
		t.Fatalf("failed to read publish file: %v", err)
	}
	var record struct {
		Request  json.RawMessage `json:"request"`
		Response json.RawMessage `json:"response"`
	}
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatalf("failed to parse publish record: %v", err)
	}
	// Compare JSON values (the record may be pretty-printed).
	var gotReq, wantReq interface{}
	json.Unmarshal(record.Request, &gotReq)
	json.Unmarshal(request, &wantReq)
	gotReqBytes, _ := json.Marshal(gotReq)
	wantReqBytes, _ := json.Marshal(wantReq)
	if string(gotReqBytes) != string(wantReqBytes) {
		t.Fatalf("request mismatch: got %s, want %s", gotReqBytes, wantReqBytes)
	}

	var gotResp, wantResp interface{}
	json.Unmarshal(record.Response, &gotResp)
	json.Unmarshal(response, &wantResp)
	gotRespBytes, _ := json.Marshal(gotResp)
	wantRespBytes, _ := json.Marshal(wantResp)
	if string(gotRespBytes) != string(wantRespBytes) {
		t.Fatalf("response mismatch: got %s, want %s", gotRespBytes, wantRespBytes)
	}
}

func TestSaveLoadProfile_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	dir = setHomeDir(t, dir)

	p := &Profile{
		Email:     "test@example.com",
		AgentName: "TestBot",
		AgentID:   "12345",
		Bio:       "A test agent",
	}
	SaveProfile("testserver", p)

	loaded, err := LoadProfile("testserver")
	if err != nil {
		t.Fatalf("LoadProfile failed: %v", err)
	}
	if loaded.Email != p.Email {
		t.Errorf("email: got %q, want %q", loaded.Email, p.Email)
	}
	if loaded.AgentName != p.AgentName {
		t.Errorf("agent_name: got %q, want %q", loaded.AgentName, p.AgentName)
	}
	if loaded.AgentID != p.AgentID {
		t.Errorf("agent_id: got %q, want %q", loaded.AgentID, p.AgentID)
	}
	if loaded.Bio != p.Bio {
		t.Errorf("bio: got %q, want %q", loaded.Bio, p.Bio)
	}
}

func TestLoadProfile_NotFound(t *testing.T) {
	dir := t.TempDir()
	dir = setHomeDir(t, dir)

	_, err := LoadProfile("nonexistent")
	if err == nil {
		t.Fatal("expected error for non-existent profile, got nil")
	}
}

func TestSaveContacts(t *testing.T) {
	dir := t.TempDir()
	dir = setHomeDir(t, dir)

	contacts := []Contact{
		{AgentID: "100", AgentName: "Alice", Remark: "friend", FriendSince: 1000},
		{AgentID: "200", AgentName: "Bob", Remark: "", FriendSince: 2000},
	}
	SaveContacts("testserver", contacts)

	path := filepath.Join(dir, "servers", "testserver", "contacts.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected contacts.json to exist: %v", err)
	}
	var loaded []Contact
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("failed to parse contacts: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("expected 2 contacts, got %d", len(loaded))
	}
	if loaded[0].AgentName != "Alice" || loaded[1].AgentName != "Bob" {
		t.Errorf("contacts mismatch: %+v", loaded)
	}
}

func TestDeleteProfileAndContacts(t *testing.T) {
	dir := t.TempDir()
	dir = setHomeDir(t, dir)

	SaveProfile("testserver", &Profile{Email: "a@b.com", AgentName: "A", AgentID: "1"})
	SaveContacts("testserver", []Contact{{AgentID: "2", AgentName: "B"}})

	profilePath := filepath.Join(dir, "servers", "testserver", "profile.json")
	contactsPath := filepath.Join(dir, "servers", "testserver", "contacts.json")

	if _, err := os.Stat(profilePath); err != nil {
		t.Fatalf("profile.json should exist before delete: %v", err)
	}
	if _, err := os.Stat(contactsPath); err != nil {
		t.Fatalf("contacts.json should exist before delete: %v", err)
	}

	DeleteProfileAndContacts("testserver")

	if _, err := os.Stat(profilePath); !os.IsNotExist(err) {
		t.Error("profile.json should be removed after DeleteProfileAndContacts")
	}
	if _, err := os.Stat(contactsPath); !os.IsNotExist(err) {
		t.Error("contacts.json should be removed after DeleteProfileAndContacts")
	}
}

func TestSaveMessages_Dedup(t *testing.T) {
	dir := t.TempDir()
	dir = setHomeDir(t, dir)

	myID := "me"
	msgs := []CachedMessage{
		{MsgID: "m1", ConvID: "c1", SenderID: "other", ReceiverID: myID, Content: "hello", CreatedAt: 1000},
		{MsgID: "m1", ConvID: "c1", SenderID: "other", ReceiverID: myID, Content: "hello dup", CreatedAt: 1000},
		{MsgID: "m2", ConvID: "c1", SenderID: myID, ReceiverID: "other", Content: "reply", CreatedAt: 2000},
	}
	SaveMessages("testserver", myID, msgs, nil)

	today := time.Now().Format(dateFormat)
	path := filepath.Join(dir, "servers", "testserver", "data", "messages", today, "agent-other.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected agent-other.json to exist: %v", err)
	}
	var loaded []CachedMessage
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("failed to parse messages: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("expected 2 messages after dedup, got %d", len(loaded))
	}
	// Should be sorted by created_at DESC.
	if loaded[0].MsgID != "m2" || loaded[1].MsgID != "m1" {
		t.Errorf("expected messages sorted by created_at DESC, got %v, %v", loaded[0].MsgID, loaded[1].MsgID)
	}
}

func TestSaveMessages_Dedup_AcrossCalls(t *testing.T) {
	dir := t.TempDir()
	dir = setHomeDir(t, dir)

	myID := "me"
	msgs1 := []CachedMessage{
		{MsgID: "m1", ConvID: "c1", SenderID: "other", ReceiverID: myID, Content: "hello", CreatedAt: 1000},
	}
	SaveMessages("testserver", myID, msgs1, nil)

	// Second call with same msg_id should not create duplicates.
	msgs2 := []CachedMessage{
		{MsgID: "m1", ConvID: "c1", SenderID: "other", ReceiverID: myID, Content: "hello", CreatedAt: 1000},
		{MsgID: "m2", ConvID: "c1", SenderID: myID, ReceiverID: "other", Content: "reply", CreatedAt: 2000},
	}
	SaveMessages("testserver", myID, msgs2, nil)

	today := time.Now().Format(dateFormat)
	path := filepath.Join(dir, "servers", "testserver", "data", "messages", today, "agent-other.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected agent-other.json to exist: %v", err)
	}
	var loaded []CachedMessage
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("failed to parse messages: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("expected 2 messages after cross-call dedup, got %d", len(loaded))
	}
}

func TestSaveMessages_GroupByAgent(t *testing.T) {
	dir := t.TempDir()
	dir = setHomeDir(t, dir)

	myID := "me"
	msgs := []CachedMessage{
		{MsgID: "m1", ConvID: "c1", SenderID: "alice", ReceiverID: myID, Content: "from alice", CreatedAt: 1000},
		{MsgID: "m2", ConvID: "c2", SenderID: "bob", ReceiverID: myID, Content: "from bob", CreatedAt: 2000},
		{MsgID: "m3", ConvID: "c1", SenderID: myID, ReceiverID: "alice", Content: "to alice", CreatedAt: 3000},
	}
	SaveMessages("testserver", myID, msgs, nil)

	today := time.Now().Format(dateFormat)
	msgDir := filepath.Join(dir, "servers", "testserver", "data", "messages", today)

	// Should have agent-alice.json and agent-bob.json.
	alicePath := filepath.Join(msgDir, "agent-alice.json")
	bobPath := filepath.Join(msgDir, "agent-bob.json")

	aliceData, err := os.ReadFile(alicePath)
	if err != nil {
		t.Fatalf("expected agent-alice.json: %v", err)
	}
	bobData, err := os.ReadFile(bobPath)
	if err != nil {
		t.Fatalf("expected agent-bob.json: %v", err)
	}

	var aliceMsgs, bobMsgs []CachedMessage
	json.Unmarshal(aliceData, &aliceMsgs)
	json.Unmarshal(bobData, &bobMsgs)

	if len(aliceMsgs) != 2 {
		t.Errorf("expected 2 alice messages, got %d", len(aliceMsgs))
	}
	if len(bobMsgs) != 1 {
		t.Errorf("expected 1 bob message, got %d", len(bobMsgs))
	}
}

func TestSaveMessages_GroupByItem(t *testing.T) {
	dir := t.TempDir()
	dir = setHomeDir(t, dir)

	myID := "me"
	convItemMap := map[string]string{
		"c1": "100",
		"c2": "200",
	}
	msgs := []CachedMessage{
		{MsgID: "m1", ConvID: "c1", SenderID: "other", ReceiverID: myID, Content: "about item 100", CreatedAt: 1000},
		{MsgID: "m2", ConvID: "c2", SenderID: "other2", ReceiverID: myID, Content: "about item 200", CreatedAt: 2000},
		{MsgID: "m3", ConvID: "c1", SenderID: myID, ReceiverID: "other", Content: "reply about item 100", CreatedAt: 3000},
	}
	SaveMessages("testserver", myID, msgs, convItemMap)

	today := time.Now().Format(dateFormat)
	msgDir := filepath.Join(dir, "servers", "testserver", "data", "messages", today)

	item100Path := filepath.Join(msgDir, "item-100.json")
	item200Path := filepath.Join(msgDir, "item-200.json")

	item100Data, err := os.ReadFile(item100Path)
	if err != nil {
		t.Fatalf("expected item-100.json: %v", err)
	}
	item200Data, err := os.ReadFile(item200Path)
	if err != nil {
		t.Fatalf("expected item-200.json: %v", err)
	}

	var item100Msgs, item200Msgs []CachedMessage
	json.Unmarshal(item100Data, &item100Msgs)
	json.Unmarshal(item200Data, &item200Msgs)

	if len(item100Msgs) != 2 {
		t.Errorf("expected 2 item-100 messages, got %d", len(item100Msgs))
	}
	if len(item200Msgs) != 1 {
		t.Errorf("expected 1 item-200 message, got %d", len(item200Msgs))
	}
}

func TestSaveMessages_Empty(t *testing.T) {
	dir := t.TempDir()
	dir = setHomeDir(t, dir)

	// Saving empty messages should be a no-op.
	SaveMessages("testserver", "me", nil, nil)
	SaveMessages("testserver", "me", []CachedMessage{}, nil)

	msgDir := filepath.Join(dir, "servers", "testserver", "data", "messages")
	if _, err := os.Stat(msgDir); !os.IsNotExist(err) {
		t.Error("expected no messages directory for empty messages")
	}
}

func TestSaveLoadConvItemMap_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	dir = setHomeDir(t, dir)

	mapping := map[string]string{
		"conv_1": "item_10",
		"conv_2": "item_20",
	}
	SaveConvItemMapping("testserver", mapping)

	loaded := LoadConvItemMap("testserver")
	if len(loaded) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(loaded))
	}
	if loaded["conv_1"] != "item_10" {
		t.Errorf("conv_1: got %q, want %q", loaded["conv_1"], "item_10")
	}
	if loaded["conv_2"] != "item_20" {
		t.Errorf("conv_2: got %q, want %q", loaded["conv_2"], "item_20")
	}
}

func TestSaveConvItemMapping_Merge(t *testing.T) {
	dir := t.TempDir()
	dir = setHomeDir(t, dir)

	SaveConvItemMapping("testserver", map[string]string{"c1": "i1"})
	SaveConvItemMapping("testserver", map[string]string{"c2": "i2"})

	loaded := LoadConvItemMap("testserver")
	if len(loaded) != 2 {
		t.Fatalf("expected 2 entries after merge, got %d", len(loaded))
	}
	if loaded["c1"] != "i1" || loaded["c2"] != "i2" {
		t.Errorf("merge mismatch: %v", loaded)
	}
}

func TestSaveConvItemMapping_Empty(t *testing.T) {
	dir := t.TempDir()
	dir = setHomeDir(t, dir)

	// Empty mapping should be a no-op.
	SaveConvItemMapping("testserver", nil)
	SaveConvItemMapping("testserver", map[string]string{})

	loaded := LoadConvItemMap("testserver")
	if len(loaded) != 0 {
		t.Fatalf("expected empty map, got %d entries", len(loaded))
	}
}

func TestLoadConvItemMap_AcrossDays(t *testing.T) {
	dir := t.TempDir()
	dir = setHomeDir(t, dir)

	msgDir := filepath.Join(dir, "servers", "testserver", "data", "messages")

	// Day 1 mapping.
	day1Dir := filepath.Join(msgDir, "20260401")
	os.MkdirAll(day1Dir, 0700)
	day1Data, _ := json.Marshal(map[string]string{"c1": "i1", "c2": "i2_old"})
	os.WriteFile(filepath.Join(day1Dir, "conv_item_map.json"), day1Data, 0600)

	// Day 2 mapping — overrides c2, adds c3.
	day2Dir := filepath.Join(msgDir, "20260402")
	os.MkdirAll(day2Dir, 0700)
	day2Data, _ := json.Marshal(map[string]string{"c2": "i2_new", "c3": "i3"})
	os.WriteFile(filepath.Join(day2Dir, "conv_item_map.json"), day2Data, 0600)

	loaded := LoadConvItemMap("testserver")
	if len(loaded) != 3 {
		t.Fatalf("expected 3 entries aggregated across days, got %d", len(loaded))
	}
	if loaded["c1"] != "i1" {
		t.Errorf("c1: got %q, want %q", loaded["c1"], "i1")
	}
	if loaded["c2"] != "i2_new" {
		t.Errorf("c2: got %q, want %q (newer day should win)", loaded["c2"], "i2_new")
	}
	if loaded["c3"] != "i3" {
		t.Errorf("c3: got %q, want %q", loaded["c3"], "i3")
	}
}

func TestLoadConvItemMap_NoData(t *testing.T) {
	dir := t.TempDir()
	dir = setHomeDir(t, dir)

	loaded := LoadConvItemMap("testserver")
	if len(loaded) != 0 {
		t.Fatalf("expected empty map when no data exists, got %d entries", len(loaded))
	}
}

func TestCleanup_Broadcasts(t *testing.T) {
	dir := t.TempDir()
	dir = setHomeDir(t, dir)

	broadcastDir := filepath.Join(dir, "servers", "testserver", "data", "broadcasts")

	// Create an old directory (beyond retention).
	oldDate := time.Now().AddDate(0, 0, -(BroadcastRetentionDays + 5)).Format(dateFormat)
	oldDir := filepath.Join(broadcastDir, oldDate)
	if err := os.MkdirAll(oldDir, 0700); err != nil {
		t.Fatalf("failed to create old dir: %v", err)
	}
	os.WriteFile(filepath.Join(oldDir, "test.json"), []byte("old"), 0600)

	// Create a recent directory (within retention).
	recentDate := time.Now().Format(dateFormat)
	recentDir := filepath.Join(broadcastDir, recentDate)
	if err := os.MkdirAll(recentDir, 0700); err != nil {
		t.Fatalf("failed to create recent dir: %v", err)
	}
	os.WriteFile(filepath.Join(recentDir, "test.json"), []byte("recent"), 0600)

	Cleanup("testserver", "broadcasts")

	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Error("old broadcast directory should have been cleaned up")
	}
	if _, err := os.Stat(recentDir); err != nil {
		t.Error("recent broadcast directory should still exist")
	}
}

func TestCleanup_Messages(t *testing.T) {
	dir := t.TempDir()
	dir = setHomeDir(t, dir)

	msgDir := filepath.Join(dir, "servers", "testserver", "data", "messages")

	// Create an old directory (beyond retention).
	oldDate := time.Now().AddDate(0, 0, -(MessageRetentionDays + 5)).Format(dateFormat)
	oldDir := filepath.Join(msgDir, oldDate)
	if err := os.MkdirAll(oldDir, 0700); err != nil {
		t.Fatalf("failed to create old dir: %v", err)
	}
	os.WriteFile(filepath.Join(oldDir, "test.json"), []byte("old"), 0600)

	// Create a recent directory (within retention).
	recentDate := time.Now().Format(dateFormat)
	recentDir := filepath.Join(msgDir, recentDate)
	if err := os.MkdirAll(recentDir, 0700); err != nil {
		t.Fatalf("failed to create recent dir: %v", err)
	}
	os.WriteFile(filepath.Join(recentDir, "test.json"), []byte("recent"), 0600)

	Cleanup("testserver", "messages")

	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Error("old message directory should have been cleaned up")
	}
	if _, err := os.Stat(recentDir); err != nil {
		t.Error("recent message directory should still exist")
	}
}

func TestCleanup_InvalidCategory(t *testing.T) {
	dir := t.TempDir()
	dir = setHomeDir(t, dir)

	// Should not panic on invalid category.
	Cleanup("testserver", "invalid")
}

func TestCleanup_NoDir(t *testing.T) {
	dir := t.TempDir()
	dir = setHomeDir(t, dir)

	// Should not panic when directory does not exist.
	Cleanup("testserver", "broadcasts")
	Cleanup("testserver", "messages")
}

func TestServerDir(t *testing.T) {
	dir := t.TempDir()
	dir = setHomeDir(t, dir)

	got := ServerDir("myserver")
	want := filepath.Join(dir, "servers", "myserver")
	if got != want {
		t.Errorf("ServerDir: got %q, want %q", got, want)
	}
}

func TestServerDataDir(t *testing.T) {
	dir := t.TempDir()
	dir = setHomeDir(t, dir)

	got := ServerDataDir("myserver")
	want := filepath.Join(dir, "servers", "myserver", "data")
	if got != want {
		t.Errorf("ServerDataDir: got %q, want %q", got, want)
	}
}
