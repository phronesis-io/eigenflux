package api

import (
	"testing"
	"time"
)

func TestLegacyConsoleHandoffTTL(t *testing.T) {
	if legacyConsoleHandoffTTL != 15*time.Minute {
		t.Fatalf("legacyConsoleHandoffTTL = %s, want 15m", legacyConsoleHandoffTTL)
	}
}
