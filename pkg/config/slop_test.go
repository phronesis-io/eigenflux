package config

import (
	"strconv"
	"testing"
)

func TestSlopEmergencySwitch(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Setenv("UGC_SLOP_ENABLED", strconv.FormatBool(enabled))
		if Load().UGCSlopEnabled != enabled {
			t.Fatalf("expected Slop enabled=%t", enabled)
		}
	}
}
