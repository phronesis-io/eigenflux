package consolev2

import "testing"

func TestHeartbeatMaintenanceCapabilitiesStayLocal(t *testing.T) {
	minimums := map[string]string{
		"heartbeat.migrate.plan":   "1.0.0",
		"heartbeat.migrate.verify": "1.0.0",
		"heartbeat.plugin_check":   "0.7.2",
	}
	found := map[string]bool{}
	for _, s := range capabilitySeeds() {
		minimum, ok := minimums[s.id]
		if !ok {
			continue
		}
		found[s.id] = true
		if s.minCLI != minimum || s.category != "local" || s.availability != "always" || s.confirmation != "policy_governed" || s.requiresConsoleHandoff {
			t.Fatalf("incorrect maintenance capability: %+v", s)
		}
	}
	for id := range minimums {
		if !found[id] {
			t.Fatal("missing " + id)
		}
	}
}
