package cmd

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cli.eigenflux.ai/internal/cache"
)

const feedInstructionReference = `In Feed items, recommended_actions[].instruction_ref names an intent above; its instruction is that intent's "then".`
const feedContractDetailsMarker = "<!-- EIGENFLUX FEED CONTRACT DETAILS -->"

func referenceFeedInstructions(feed map[string]any) {
	context, _ := feed["control_context_snapshot"].(map[string]any)
	intents, _ := context["intent_actions"].([]any)
	instructions := map[string]string{}
	for _, value := range intents {
		intent, _ := value.(map[string]any)
		id, _ := intent["intent_id"].(string)
		instruction, ok := intent["then"].(string)
		if id != "" && ok {
			instructions[id] = instruction
		}
	}
	items, _ := feed["items"].([]any)
	for _, value := range items {
		item, _ := value.(map[string]any)
		match, _ := item["intent_match"].(map[string]any)
		matched, _ := match["matched_intent_ids"].([]any)
		actions, _ := item["recommended_actions"].([]any)
		for _, value := range actions {
			action, _ := value.(map[string]any)
			instruction, ok := action["instruction"].(string)
			if !ok {
				continue
			}
			for _, value := range matched {
				id, _ := value.(string)
				if text, ok := instructions[id]; ok && text == instruction {
					action["instruction_ref"] = id
					delete(action, "instruction")
					break
				}
			}
		}
	}
}

// A failed local write keeps the served contract inline.
func feedBatchContractText(server, contract string, readOnly bool) string {
	core, _, found := strings.Cut(contract, "\n"+feedContractDetailsMarker+"\n")
	if !found || strings.TrimSpace(core) == "" {
		return contract
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(contract)))[:16]
	dir, err := filepath.Abs(filepath.Join(cache.ServerDataDir(server), "feed-contracts"))
	if err != nil {
		return contract
	}
	path := filepath.Join(dir, digest+".md")
	// Budget retries render this repeatedly under the report lock; rewrite only on change.
	if data, err := os.ReadFile(path); err != nil || string(data) != contract {
		if readOnly || writeFileAtomic(path, []byte(contract)) != nil {
			return contract
		}
	}
	entries, _ := os.ReadDir(dir)
	cutoff := time.Now().AddDate(0, 0, -cache.BroadcastRetentionDays)
	for _, entry := range entries {
		if readOnly {
			break
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" || entry.Name() == digest+".md" {
			continue
		}
		if info, err := entry.Info(); err == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
	return core + "\n" + fmt.Sprintf("[EIGENFLUX OUTPUT CONTRACT] Read %s (sha256 %s, %d bytes) completely before actions whose procedures are in the details. If unread, skip those actions. Read once per cycle; reread when the sha256 changes.", path, digest, len(contract))
}
