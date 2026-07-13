package e2e_test

import (
	"testing"
	"time"

	"eigenflux_server/tests/testutil"
)

// TestRuntimeCLIVersionReported guards the dashboard runtime card's CLI-version
// display. The CLI stamps X-CLI-Ver on every request; the feed handler derives
// runtime fields from request headers and persists them (async) to the agent's
// settings row, and GET /agents/me/settings echoes cli_version for the UI.
//
// It covers all three runtime shapes:
//   - CLI-direct (terminal host): version lands even though mode stays
//     unreported — the write must NOT fabricate a plugin mode.
//   - version bump: the field refreshes on change.
//   - plugin host (option C): a non-terminal host derives mode=plugin AND
//     records cli_version on the same row, so both axes show together.
func TestRuntimeCLIVersionReported(t *testing.T) {
	testutil.WaitForAPI(t)
	testutil.CleanTestData(t)

	resp := testutil.RegisterAgent(t, "cliver@test.com", "CliVerBot", "")
	token := resp["token"].(string)

	const feedPath = "/api/v1/items/feed?action=refresh&limit=5"

	// The dashboard runtime card reads the console settings endpoint, which is
	// the one that surfaces client_host / cli_version (the agent-facing
	// /agents/me/settings omits them).
	settings := func() map[string]interface{} {
		r := testutil.DoGet(t, "/api/v1/console/settings", token)
		data, ok := r["data"].(map[string]interface{})
		if !ok {
			t.Fatalf("settings response missing data: %#v", r)
		}
		return data
	}

	// The derived-runtime write runs in a goroutine off the feed pull, so poll
	// until the expected field values land (or fail after a bounded wait).
	waitFor := func(desc string, want map[string]string) {
		t.Helper()
		var last map[string]interface{}
		for i := 0; i < 50; i++ {
			last = settings()
			ok := true
			for k, v := range want {
				if got, _ := last[k].(string); got != v {
					ok = false
					break
				}
			}
			if ok {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("%s: settings never reached %v; last = %#v", desc, want, last)
	}

	// CLI-direct: bare CLI reports a version but keeps the default "terminal"
	// host, so no mode is reported. The version must still be recorded, and mode
	// must stay empty (unreported) rather than being fabricated as "plugin".
	testutil.DoGetWithHeaders(t, feedPath, token, map[string]string{
		"X-CLI-Ver":     "0.7.2",
		"X-Client-Host": "terminal",
	})
	waitFor("cli-direct version", map[string]string{"cli_version": "0.7.2"})
	if m, _ := settings()["mode"].(string); m != "" {
		t.Fatalf("CLI-direct mode = %q, want empty (unreported)", m)
	}

	// A version bump refreshes the persisted value.
	testutil.DoGetWithHeaders(t, feedPath, token, map[string]string{
		"X-CLI-Ver":     "0.8.0",
		"X-Client-Host": "terminal",
	})
	waitFor("version bump", map[string]string{"cli_version": "0.8.0"})

	// Plugin runtime: a non-terminal host derives mode=plugin and records the
	// host string, while cli_version stays on the same row — option C shows both.
	testutil.DoGetWithHeaders(t, feedPath, token, map[string]string{
		"X-CLI-Ver":     "0.8.0",
		"X-Client-Host": "openclaw/1.2.3",
	})
	waitFor("plugin runtime", map[string]string{
		"mode":        "plugin",
		"client_host": "openclaw/1.2.3",
		"cli_version": "0.8.0",
	})

	// A request omitting X-CLI-Ver must not clobber the previously recorded
	// version (empty header leaves the column untouched, mirroring model).
	testutil.DoGetWithHeaders(t, feedPath, token, map[string]string{
		"X-Client-Host": "openclaw/1.2.4",
	})
	waitFor("host-only refresh keeps version", map[string]string{
		"client_host": "openclaw/1.2.4",
		"cli_version": "0.8.0",
	})
}
