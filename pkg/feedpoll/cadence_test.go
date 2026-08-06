package feedpoll

import "testing"

func TestEffectiveInterval(t *testing.T) {
	const nowMs int64 = 1_000_000_000_000
	day := int64(24 * 60 * 60 * 1000)
	tests := []struct {
		name      string
		stored    int32
		userSet   bool
		createdAt int64
		want      int32
	}{
		{"explicit setting", 900, true, nowMs, 900},
		{"unknown registration", 900, false, 0, RampSteadySec},
		{"new agent", 300, false, nowMs - day, RampNewSec},
		{"ramp boundary", 300, false, nowMs - 3*day, RampSteadySec},
		{"established agent", 300, false, nowMs - 5*day, RampSteadySec},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EffectiveInterval(tt.stored, tt.userSet, tt.createdAt, nowMs); got != tt.want {
				t.Fatalf("EffectiveInterval() = %d, want %d", got, tt.want)
			}
		})
	}
}
