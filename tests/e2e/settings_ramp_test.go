package e2e_test

import (
	"testing"

	"eigenflux_server/tests/testutil"
)

// TestSettingsRampUserSetSemantics guards the fix for the onboarding-ramp
// false-pin bug: a client pushing feed_poll_interval WITHOUT an explicit
// feed_poll_interval_user_set flag must not be treated as a user override, so
// the ramp keeps applying. Only an explicit flag pins the value. A brand-new
// agent registers inside the 3-day ramp window, so its default cadence is the
// ramp value (3600s), not the stored 300s.
func TestSettingsRampUserSetSemantics(t *testing.T) {
	testutil.WaitForAPI(t)
	testutil.CleanTestData(t)

	resp := testutil.RegisterAgent(t, "rampuser@test.com", "RampBot", "")
	token := resp["token"].(string)

	getInterval := func() int {
		r := testutil.DoGet(t, "/api/v1/agents/me/settings", token)
		data, ok := r["data"].(map[string]interface{})
		if !ok {
			t.Fatalf("settings response missing data: %#v", r)
		}
		return int(data["feed_poll_interval"].(float64))
	}

	// Fresh agent: no override yet, inside the ramp window -> 3600s.
	if got := getInterval(); got != 3600 {
		t.Fatalf("fresh agent feed_poll_interval = %d, want 3600 (ramp)", got)
	}

	// A client echoes its local default (300) with NO user_set flag. The stored
	// value updates but the ramp must stay in effect — this is exactly the path
	// that used to silently pin the row.
	if r := testutil.DoPut(t, "/api/v1/agents/me/settings",
		map[string]interface{}{"feed_poll_interval": 300}, token); int(r["code"].(float64)) != 0 {
		t.Fatalf("PUT without flag failed: %#v", r)
	}
	if got := getInterval(); got != 3600 {
		t.Fatalf("after value-only push, feed_poll_interval = %d, want 3600 (ramp must survive)", got)
	}

	// An explicit override (value + user_set=true) pins the value and stops the ramp.
	if r := testutil.DoPut(t, "/api/v1/agents/me/settings",
		map[string]interface{}{"feed_poll_interval": 600, "feed_poll_interval_user_set": true}, token); int(r["code"].(float64)) != 0 {
		t.Fatalf("PUT with flag failed: %#v", r)
	}
	if got := getInterval(); got != 600 {
		t.Fatalf("after explicit override, feed_poll_interval = %d, want 600 (pinned)", got)
	}
}
