// Package feedpoll owns the effective feed polling cadence shared by the
// settings API and Agent Card projection.
package feedpoll

const (
	RampWindowMs  int64 = 3 * 24 * 60 * 60 * 1000
	RampNewSec    int32 = 3600
	RampSteadySec int32 = 300
)

// EffectiveInterval returns the interval currently applied to feed polling.
// An explicit user setting always wins; otherwise new agents use the slower
// onboarding cadence for three days before moving to the steady cadence.
func EffectiveInterval(stored int32, userSet bool, createdAtMs, nowMs int64) int32 {
	if userSet {
		return stored
	}
	if createdAtMs > 0 && nowMs-createdAtMs < RampWindowMs {
		return RampNewSec
	}
	return RampSteadySec
}
