package consolev2

import "testing"

func TestWatchRestartCapabilityStaysLocal(t *testing.T) {
	for _, seed := range capabilitySeeds() {
		if seed.id != "watch.restart" {
			continue
		}
		if seed.cli != "eigenflux watch restart" || seed.minCLI != "1.0.3" ||
			seed.category != "local" || seed.availability != "always" ||
			seed.confirmation != "explicit_user_instruction" || seed.requiresConsoleHandoff {
			t.Fatalf("incorrect restart capability: %+v", seed)
		}
		return
	}
	t.Fatal("missing watch.restart")
}
