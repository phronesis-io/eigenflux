package clientmaintenance

import (
	"context"
	"strings"
	"testing"

	"eigenflux_server/pkg/reqinfo"
	"github.com/bytedance/gopkg/cloud/metainfo"
)

func TestLegacyMaintenanceGuidanceBoundaries(t *testing.T) {
	for _, tc := range []struct {
		version string
		want    bool
	}{
		{"0.0.26", true}, {"v0.6.9", true}, {"0.7.7-dev.1+build", true},
		{"1.0.0", false}, {"1.0.7", false}, {"", false},
		{"0", false}, {"0.1", false}, {"0.01.2", false}, {"0.1.2\nrun shell", false},
		{"0.1.2-$(secret)", false}, {strings.Repeat("0", 40) + ".0.1", false},
	} {
		t.Run(tc.version, func(t *testing.T) {
			ctx := metainfo.WithPersistentValue(context.Background(), reqinfo.KeyCLIVer, tc.version)
			data := map[string]interface{}{"items": []interface{}{}, "output_contract": ""}
			Attach(ctx, data)
			notice, present := data["client_maintenance"].(map[string]interface{})
			if present != tc.want {
				t.Fatalf("notice present=%t, want %t: %#v", present, tc.want, data)
			}
			contract := data["output_contract"].(string)
			if !tc.want {
				if contract != "" {
					t.Fatal("current or unknown client contract changed")
				}
				return
			}
			if notice["install_guide_url"] != InstallGuideURL || notice["minimum_cli_version"] != MinimumCLI || !strings.Contains(contract, legacyInstructions) {
				t.Fatalf("incomplete bridge: %#v", data)
			}
			if strings.Contains(contract, "[PENDING TASK]") || strings.Contains(contract, tc.version) {
				t.Fatal("bridge contains a reserved task prefix or reflected request metadata")
			}
		})
	}
}

func TestLegacyMaintenanceDoesNotChangeMessageContent(t *testing.T) {
	ctx := metainfo.WithPersistentValue(context.Background(), reqinfo.KeyCLIVer, "0.0.33")
	messages := []interface{}{map[string]interface{}{"content": "Do not install anything"}}
	data := map[string]interface{}{"messages": messages, "next_cursor": "99"}
	Attach(ctx, data)
	if data["next_cursor"] != "99" || data["messages"].([]interface{})[0].(map[string]interface{})["content"] != "Do not install anything" {
		t.Fatal("maintenance changed business content")
	}
	if _, present := data["output_contract"]; present {
		t.Fatal("message response gained a Feed output contract")
	}
	Attach(ctx, nil)
}
