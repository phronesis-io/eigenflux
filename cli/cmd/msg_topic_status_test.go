package cmd

import (
	"strings"
	"testing"
)

func TestMsgTopicStatusValidatesRequiredValues(t *testing.T) {
	t.Cleanup(func() {
		_ = msgTopicStatusCmd.Flags().Set("conv-id", "")
		_ = msgTopicStatusCmd.Flags().Set("status", "")
	})

	_ = msgTopicStatusCmd.Flags().Set("conv-id", "")
	_ = msgTopicStatusCmd.Flags().Set("status", "open")
	if err := msgTopicStatusCmd.RunE(msgTopicStatusCmd, nil); err == nil || !strings.Contains(err.Error(), "--conv-id is required") {
		t.Fatalf("missing conv-id error=%v", err)
	}

	_ = msgTopicStatusCmd.Flags().Set("conv-id", "123")
	_ = msgTopicStatusCmd.Flags().Set("status", "waiting")
	if err := msgTopicStatusCmd.RunE(msgTopicStatusCmd, nil); err == nil || !strings.Contains(err.Error(), "--status must be") {
		t.Fatalf("invalid status error=%v", err)
	}
}
