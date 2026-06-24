package install_test

import (
	"regexp"
	"strings"
	"testing"

	"eigenflux_server/tests/testutil"
)

var tokenRe = regexp.MustCompile(`^EF-[0-9A-Za-z]{8}$`)

// End-to-end flow of install attribution (/api/v1/install/*): mint a token
// carrying UTM data, then report installs and assert the conversion flip is
// idempotent. Requires the local stack (scripts/local/start_local.sh) like the
// other e2e suites. Token minting is IP rate limited (20/min); this suite mints
// a couple of tokens, well within headroom on a fresh gateway start.
func TestInstallAttributionFlow(t *testing.T) {
	testutil.WaitForAPI(t)

	// --- mint a token for a Google CPC campaign ---
	mint := testutil.DoPost(t, "/api/v1/install/token", map[string]interface{}{
		"utm_source":   "google",
		"utm_medium":   "cpc",
		"utm_campaign": "launch_2026",
		"referrer":     "https://example.com/",
	}, "")
	if int(mint["code"].(float64)) != 0 {
		t.Fatalf("install/token failed: %v", mint)
	}
	md := mint["data"].(map[string]interface{})
	token := md["token"].(string)
	if !tokenRe.MatchString(token) {
		t.Fatalf("token %q does not match EF-xxxxxxxx", token)
	}
	if cmd := md["command"].(string); !strings.Contains(cmd, "--invite "+token) {
		t.Fatalf("command missing invite token: %s", cmd)
	}

	// --- first report is the conversion: pending -> installed ---
	rep1 := testutil.DoPost(t, "/api/v1/install/report", map[string]interface{}{
		"token":    token,
		"metadata": map[string]string{"os": "Linux", "arch": "x86_64"},
	}, "")
	if int(rep1["code"].(float64)) != 0 {
		t.Fatalf("first report failed: %v", rep1)
	}
	d1 := rep1["data"].(map[string]interface{})
	if d1["converted"] != true {
		t.Fatalf("first report should convert, got %v", d1)
	}
	attr := d1["attribution"].(map[string]interface{})
	if attr["channel"] != "google" || attr["utm_campaign"] != "launch_2026" {
		t.Fatalf("attribution recovered wrong UTM: %v", attr)
	}
	if int(attr["report_count"].(float64)) != 1 {
		t.Fatalf("report_count should be 1 after first report, got %v", attr["report_count"])
	}

	// --- replay: same token reports again; not a new conversion, count bumps ---
	rep2 := testutil.DoPost(t, "/api/v1/install/report", map[string]interface{}{"token": token}, "")
	d2 := rep2["data"].(map[string]interface{})
	if d2["converted"] != false {
		t.Fatalf("replay should not re-convert, got %v", d2)
	}
	if got := int(d2["attribution"].(map[string]interface{})["report_count"].(float64)); got != 2 {
		t.Fatalf("report_count should be 2 after replay, got %d", got)
	}
}

// TestInstallChannelFallback checks an unmapped utm_source is kept (lowercased)
// rather than discarded, so new campaigns still group under their own source.
func TestInstallChannelFallback(t *testing.T) {
	testutil.WaitForAPI(t)
	mint := testutil.DoPost(t, "/api/v1/install/token", map[string]interface{}{
		"utm_source": "ProductHunt",
	}, "")
	token := mint["data"].(map[string]interface{})["token"].(string)
	rep := testutil.DoPost(t, "/api/v1/install/report", map[string]interface{}{"token": token}, "")
	ch := rep["data"].(map[string]interface{})["attribution"].(map[string]interface{})["channel"]
	if ch != "producthunt" {
		t.Fatalf("unmapped source should normalize to lowercased self, got %v", ch)
	}
}

func TestInstallReportValidation(t *testing.T) {
	testutil.WaitForAPI(t)

	// malformed token -> 400
	bad := testutil.DoPost(t, "/api/v1/install/report", map[string]interface{}{"token": "not-a-token"}, "")
	if int(bad["code"].(float64)) != 400 {
		t.Fatalf("malformed token should be 400, got %v", bad)
	}

	// well-formed but unknown token -> 404
	miss := testutil.DoPost(t, "/api/v1/install/report", map[string]interface{}{"token": "EF-zzzzzzzz"}, "")
	if int(miss["code"].(float64)) != 404 {
		t.Fatalf("unknown token should be 404, got %v", miss)
	}
}
