package agentcard

import "testing"

func TestCardRuntimeFields(t *testing.T) {
	tests := []struct {
		name                                             string
		mode, host, product, version, cliVersion         string
		legacy, runtimeMode, runtimeName, runtimeVersion string
	}{
		{name: "plugin remains backward compatible", mode: "plugin", host: "openclaw/0.0.29", product: "openclaw", version: "0.0.29", legacy: "openclaw/0.0.29", runtimeMode: "plugin", runtimeName: "openclaw", runtimeVersion: "0.0.29"},
		{name: "custom skill product", mode: "skill", host: "jarvis/1.2.0", product: "jarvis", version: "1.2.0", legacy: "skill", runtimeMode: "skill", runtimeName: "jarvis", runtimeVersion: "1.2.0"},
		{name: "direct cli", cliVersion: "0.0.30", runtimeMode: "cli-direct"},
		{name: "rolling deploy fallback", mode: "skill", host: "hermes/0.17.0", legacy: "skill", runtimeMode: "skill", runtimeName: "hermes", runtimeVersion: "0.17.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			legacy, runtimeMode, runtimeName, runtimeVersion := cardRuntimeFields(tt.mode, tt.host, tt.product, tt.version, tt.cliVersion)
			if legacy != tt.legacy || runtimeMode != tt.runtimeMode || runtimeName != tt.runtimeName || runtimeVersion != tt.runtimeVersion {
				t.Fatalf("got (%q, %q, %q, %q), want (%q, %q, %q, %q)", legacy, runtimeMode, runtimeName, runtimeVersion, tt.legacy, tt.runtimeMode, tt.runtimeName, tt.runtimeVersion)
			}
		})
	}
}
