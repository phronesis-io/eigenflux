package cache

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"time"

	"cli.eigenflux.ai/internal/config"
)

const (
	BroadcastRetentionDays = 8
	MessageRetentionDays   = 31

	dateFormat = "20060102"
	dirPerm    = os.FileMode(0700)
	filePerm   = os.FileMode(0600)
)

// Profile holds cached user profile data.
type Profile struct {
	Email     string `json:"email"`
	AgentName string `json:"agent_name"`
	AgentID   string `json:"agent_id"`
	Bio       string `json:"bio"`
}

// Contact holds cached contact data.
type Contact struct {
	AgentID     string `json:"agent_id"`
	AgentName   string `json:"agent_name"`
	Remark      string `json:"remark"`
	FriendSince int64  `json:"friend_since"`
}

// CachedMessage holds a single cached message.
type CachedMessage struct {
	MsgID        string `json:"msg_id"`
	ConvID       string `json:"conv_id"`
	SenderID     string `json:"sender_id"`
	ReceiverID   string `json:"receiver_id"`
	Content      string `json:"content"`
	CreatedAt    int64  `json:"created_at"`
	SenderName   string `json:"sender_name"`
	ReceiverName string `json:"receiver_name"`
}

// ServerDir returns the base directory for a server: ~/.eigenflux/servers/{serverName}
func ServerDir(serverName string) string {
	return filepath.Join(config.HomeDir(), "servers", serverName)
}

// ServerDataDir returns the data directory for a server: ~/.eigenflux/servers/{serverName}/data
func ServerDataDir(serverName string) string {
	return filepath.Join(ServerDir(serverName), "data")
}

// SaveFeedResponse saves a feed API response to data/broadcasts/{YYYYMMDD}/feeds-{YYYYMMDD-HHmmss}.json.
// Skips writing when both items and notifications are empty.
func SaveFeedResponse(serverName string, rawData json.RawMessage) {
	var check struct {
		Items         []json.RawMessage `json:"items"`
		Notifications []json.RawMessage `json:"notifications"`
	}
	if json.Unmarshal(rawData, &check) == nil &&
		len(check.Items) == 0 && len(check.Notifications) == 0 {
		return
	}
	now := time.Now()
	dir := filepath.Join(ServerDataDir(serverName), "broadcasts", now.Format(dateFormat))
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		log.Printf("cache: mkdir %s: %v", dir, err)
		return
	}
	path := filepath.Join(dir, fmt.Sprintf("feeds-%s.json", now.Format("20060102-150405")))
	if err := os.WriteFile(path, rawData, filePerm); err != nil {
		log.Printf("cache: write %s: %v", path, err)
	}
}

// SavePublishRecord saves a publish request/response pair to data/broadcasts/{YYYYMMDD}/publish-{unix_ms}.json.
func SavePublishRecord(serverName string, request, response json.RawMessage) {
	now := time.Now()
	dir := filepath.Join(ServerDataDir(serverName), "broadcasts", now.Format(dateFormat))
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		log.Printf("cache: mkdir %s: %v", dir, err)
		return
	}
	record := struct {
		Request  json.RawMessage `json:"request"`
		Response json.RawMessage `json:"response"`
	}{Request: request, Response: response}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		log.Printf("cache: marshal publish record: %v", err)
		return
	}
	path := filepath.Join(dir, fmt.Sprintf("publish-%d.json", now.UnixMilli()))
	if err := os.WriteFile(path, data, filePerm); err != nil {
		log.Printf("cache: write %s: %v", path, err)
	}
}

// SaveMessages saves messages grouped by counterpart agent and item. The
// counterpart for each message is the agent_id that is not ours — we read our
// own agent_id from profile.json so callers don't have to pass it. Messages
// are deduped by msg_id, merged with existing files, and sorted by created_at
// DESC.
func SaveMessages(serverName string, messages []CachedMessage, convItemMap map[string]string) {
	if len(messages) == 0 {
		return
	}
	myAgentID := ""
	if p, err := LoadProfile(serverName); err == nil {
		myAgentID = p.AgentID
	}
	today := time.Now().Format(dateFormat)
	dir := filepath.Join(ServerDataDir(serverName), "messages", today)
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		log.Printf("cache: mkdir %s: %v", dir, err)
		return
	}

	// Group messages by counterpart agent ID.
	byAgent := make(map[string][]CachedMessage)
	byItem := make(map[string][]CachedMessage)
	for _, msg := range messages {
		counterpart := msg.SenderID
		if counterpart == myAgentID {
			counterpart = msg.ReceiverID
		}
		byAgent[counterpart] = append(byAgent[counterpart], msg)

		if itemID, ok := convItemMap[msg.ConvID]; ok && itemID != "" {
			byItem[itemID] = append(byItem[itemID], msg)
		}
	}

	for agentID, msgs := range byAgent {
		path := filepath.Join(dir, fmt.Sprintf("agent-%s.json", agentID))
		mergeAndWrite(path, msgs)
	}
	for itemID, msgs := range byItem {
		path := filepath.Join(dir, fmt.Sprintf("item-%s.json", itemID))
		mergeAndWrite(path, msgs)
	}
}

// mergeAndWrite reads existing messages from path, merges with new ones, deduplicates
// by msg_id, sorts by created_at DESC, and writes back.
func mergeAndWrite(path string, newMsgs []CachedMessage) {
	var existing []CachedMessage
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &existing); err != nil {
			log.Printf("cache: parse %s: %v", path, err)
		}
	}

	seen := make(map[string]struct{}, len(existing)+len(newMsgs))
	merged := make([]CachedMessage, 0, len(existing)+len(newMsgs))
	for _, msg := range newMsgs {
		if _, dup := seen[msg.MsgID]; !dup {
			seen[msg.MsgID] = struct{}{}
			merged = append(merged, msg)
		}
	}
	for _, msg := range existing {
		if _, dup := seen[msg.MsgID]; !dup {
			seen[msg.MsgID] = struct{}{}
			merged = append(merged, msg)
		}
	}

	sort.Slice(merged, func(i, j int) bool {
		return merged[i].CreatedAt > merged[j].CreatedAt
	})

	data, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		log.Printf("cache: marshal messages: %v", err)
		return
	}
	if err := os.WriteFile(path, data, filePerm); err != nil {
		log.Printf("cache: write %s: %v", path, err)
	}
}

// SaveProfile saves a Profile to profile.json in the server directory.
func SaveProfile(serverName string, p *Profile) {
	dir := ServerDir(serverName)
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		log.Printf("cache: mkdir %s: %v", dir, err)
		return
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		log.Printf("cache: marshal profile: %v", err)
		return
	}
	path := filepath.Join(dir, "profile.json")
	if err := os.WriteFile(path, data, filePerm); err != nil {
		log.Printf("cache: write %s: %v", path, err)
	}
}

// LoadProfile loads a Profile from profile.json in the server directory.
func LoadProfile(serverName string) (*Profile, error) {
	path := filepath.Join(ServerDir(serverName), "profile.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p Profile
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parse profile: %w", err)
	}
	return &p, nil
}

// SaveContacts saves contacts to contacts.json in the server directory.
func SaveContacts(serverName string, contacts []Contact) {
	dir := ServerDir(serverName)
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		log.Printf("cache: mkdir %s: %v", dir, err)
		return
	}
	data, err := json.MarshalIndent(contacts, "", "  ")
	if err != nil {
		log.Printf("cache: marshal contacts: %v", err)
		return
	}
	path := filepath.Join(dir, "contacts.json")
	if err := os.WriteFile(path, data, filePerm); err != nil {
		log.Printf("cache: write %s: %v", path, err)
	}
}

// DeleteProfileAndContacts removes profile.json and contacts.json from the server directory.
// Errors are silently ignored.
func DeleteProfileAndContacts(serverName string) {
	dir := ServerDir(serverName)
	os.Remove(filepath.Join(dir, "profile.json"))
	os.Remove(filepath.Join(dir, "contacts.json"))
}

// SaveConvItemMapping merges the given mapping into today's conv_item_map.json under messages.
func SaveConvItemMapping(serverName string, mapping map[string]string) {
	if len(mapping) == 0 {
		return
	}
	today := time.Now().Format(dateFormat)
	dir := filepath.Join(ServerDataDir(serverName), "messages", today)
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		log.Printf("cache: mkdir %s: %v", dir, err)
		return
	}
	path := filepath.Join(dir, "conv_item_map.json")

	existing := make(map[string]string)
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &existing); err != nil {
			log.Printf("cache: parse %s: %v", path, err)
		}
	}

	for k, v := range mapping {
		existing[k] = v
	}

	data, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		log.Printf("cache: marshal conv_item_map: %v", err)
		return
	}
	if err := os.WriteFile(path, data, filePerm); err != nil {
		log.Printf("cache: write %s: %v", path, err)
	}
}

// LoadConvItemMap scans all message date directories and aggregates conv_item_map.json entries.
// Newer entries (from later dates) take precedence over older ones.
// Returns an empty map if none exists.
func LoadConvItemMap(serverName string) map[string]string {
	msgDir := filepath.Join(ServerDataDir(serverName), "messages")
	entries, err := os.ReadDir(msgDir)
	if err != nil {
		return make(map[string]string)
	}

	// Sort date directories ascending so newer entries overwrite older ones.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	result := make(map[string]string)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(msgDir, entry.Name(), "conv_item_map.json")
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var m map[string]string
		if err := json.Unmarshal(data, &m); err != nil {
			log.Printf("cache: parse %s: %v", path, err)
			continue
		}
		for k, v := range m {
			result[k] = v
		}
	}
	return result
}

// Cleanup deletes date directories older than the retention period.
// category must be "broadcasts" or "messages".
func Cleanup(serverName string, category string) {
	var retentionDays int
	switch category {
	case "broadcasts":
		retentionDays = BroadcastRetentionDays
	case "messages":
		retentionDays = MessageRetentionDays
	default:
		log.Printf("cache: unknown category %q", category)
		return
	}

	catDir := filepath.Join(ServerDataDir(serverName), category)
	entries, err := os.ReadDir(catDir)
	if err != nil {
		return
	}

	cutoff := time.Now().AddDate(0, 0, -retentionDays).Format(dateFormat)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		// Directory name is expected to be YYYYMMDD. If it sorts before the cutoff, it's expired.
		if entry.Name() < cutoff {
			dirPath := filepath.Join(catDir, entry.Name())
			if err := os.RemoveAll(dirPath); err != nil {
				log.Printf("cache: cleanup %s: %v", dirPath, err)
			}
		}
	}
}
