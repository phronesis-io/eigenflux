package cmd

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"cli.eigenflux.ai/internal/cache"
)

const feedInstructionReference = `In Feed items, recommended_actions[].instruction_ref names an intent above; its instruction is that intent's "then".`

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
func feedBatchContract(server, contract string) string {
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(contract)))[:16]
	dir, err := filepath.Abs(filepath.Join(cache.ServerDataDir(server), "feed-contracts"))
	if err != nil {
		return contract
	}
	path := filepath.Join(dir, digest+".md")
	if err := writeFileAtomic(path, []byte(contract)); err != nil {
		return contract
	}
	entries, _ := os.ReadDir(dir)
	cutoff := time.Now().AddDate(0, 0, -cache.BroadcastRetentionDays)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" || entry.Name() == digest+".md" {
			continue
		}
		if info, err := entry.Info(); err == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
	return fmt.Sprintf("[EIGENFLUX OUTPUT CONTRACT] Read %s (sha256 %s, %d bytes) completely before processing this batch. It binds this Feed exactly as if printed here. Read it once per cycle; reread when the sha256 changes.", path, digest, len(contract))
}
