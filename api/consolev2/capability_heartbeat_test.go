package consolev2

import "testing"

func TestHeartbeatMaintenanceCapabilitiesStayLocal(t *testing.T) {
	want := map[string]bool{"heartbeat.migrate.plan": false, "heartbeat.migrate.verify": false, "heartbeat.plugin_check": false}
	for _, s := range capabilitySeeds() {
		if _, ok := want[s.id]; !ok {
			continue
		}
		want[s.id] = true
		if s.minCLI != "0.0.48" || s.category != "local" || s.availability != "always" || s.confirmation != "policy_governed" || s.requiresConsoleHandoff {
			t.Fatalf("incorrect maintenance capability: %+v", s)
		}
	}
	for id, found := range want {
		if !found {
			t.Fatal("missing " + id)
		}
	}
}
