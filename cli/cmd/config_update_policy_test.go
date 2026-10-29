package cmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestConfigCannotSetCLIUpdatePolicy(t *testing.T) {
	cfg, server := runtimeTestConfig(t, "http://127.0.0.1:1", true)
	for _, scope := range []string{"", server} {
		serverFlag = scope
		for _, value := range []string{"false", "true", ""} {
			cmd := &cobra.Command{}
			cmd.Flags().String("key", "auto_cli_update", "")
			cmd.Flags().String("value", value, "")
			if err := configSetCmd.RunE(cmd, nil); err == nil || !strings.Contains(err.Error(), "not configurable") {
				t.Fatalf("scope=%q value=%q: %v", scope, value, err)
			}
		}
	}
	if cfg.GetKV("auto_cli_update") != "" {
		t.Fatal("rejected writes changed the local policy")
	}
}
