package install

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
)

// --- Xiaohongshu 聚光 conversion callback (Loop B) ---
//
// Reports conversions back to 聚光's optimizer keyed by the original click_id so
// ocpx can optimize bidding. Server-to-server (no landing-page pixel). Two-stage
// funnel, each event exactly-once per ref (ClaimCallback on independent columns):
//
//	event_type 101 — fired on the copy click (POST /api/v1/install/copy)
//	event_type 102 — fired on the successful install (POST /api/v1/install/report)
//
// Switches are code defaults, overridable by env for ops without a rebuild:
//
//	XHS_CALLBACK_ENABLED  master switch; keep OFF until 直客 联调 is verified
//	XHS_AUTH_ENABLED      do the getAccessToken handshake (the 3.20 doc says new
//	                      clients may not need it — set false to skip the token)
//	XHS_ADVERTISER_ID     REQUIRED (no code default); callbacks skip if unset
//	XHS_API_BASE
const (
	EventCopy    = "101" // 表单提交 (shallow intent) — copy click
	EventInstall = "102" // deep conversion — install success
)

var (
	xhsCallbackEnabled bool
	xhsAuthEnabled     bool
	xhsAdvertiserID    string
	xhsAPIBase         string
)

// initXHSConfig reads the callback config from env. It MUST run from Register
// (after the app loads .env via config's godotenv.Load) — reading these at
// package-var init runs before main() and would miss .env values, so a
// XHS_CALLBACK_ENABLED=true in .env would be silently ignored.
func initXHSConfig() {
	xhsCallbackEnabled = envBool("XHS_CALLBACK_ENABLED", false)
	xhsAuthEnabled = envBool("XHS_AUTH_ENABLED", true)
	// No baked-in advertiser id — the value lives in ops config (XHS_ADVERTISER_ID
	// in .env). If the callback is on but this is unset, skip rather than post to
	// the wrong account.
	xhsAdvertiserID = envStr("XHS_ADVERTISER_ID", "")
	xhsAPIBase = envStr("XHS_API_BASE", "https://adapi.xiaohongshu.com")
	if xhsCallbackEnabled && xhsAdvertiserID == "" {
		logger.Default().Error("XHS_CALLBACK_ENABLED=true but XHS_ADVERTISER_ID is empty; callbacks skipped")
	}
}

const xhsCommonPath = "/api/open/common"

var xhsHTTP = &http.Client{Timeout: 8 * time.Second}

// fireXHSCallback reports the eventType conversion for ref to 聚光 exactly once,
// in the background. Safe to call repeatedly; a no-op unless the master switch is
// on, an advertiser id is configured, and ref carries a 聚光 click_id.
func fireXHSCallback(ref, eventType string) {
	if !xhsCallbackEnabled || xhsAdvertiserID == "" {
		return
	}
	go func() {
		won, t, err := ClaimCallback(db.DB, ref, eventType)
		if err != nil {
			logger.Default().Error("install xhs callback claim failed", "ref", ref, "event_type", eventType, "err", err)
			return
		}
		if !won || t.ClickID == "" {
			return // already succeeded, no 聚光 click id, or not claimable
		}
		code, err := reportXHSConversion(t.ClickID, eventType)
		if err != nil {
			logger.Default().Error("install xhs callback failed", "ref", ref, "event_type", eventType, "code", code, "err", err)
		}
		if e := SetCallbackCode(db.DB, ref, eventType, code); e != nil {
			logger.Default().Error("install xhs callback set code failed", "ref", ref, "event_type", eventType, "err", e)
		}
		if code == 0 {
			event("install_callback_xhs", ref, "channel", t.Channel, "event_type", eventType)
		}
	}()
}

// reportXHSConversion POSTs one aurora.leads conversion for clickID with the
// given eventType and returns the platform response code (0 = accepted, >0 =
// platform error) or -2 on a transport/token error.
func reportXHSConversion(clickID, eventType string) (int, error) {
	body := map[string]interface{}{
		"advertiser_id": xhsAdvertiserID,
		"method":        "aurora.leads",
		"event_type":    eventType,
		"conv_time":     time.Now().UnixMilli(),
		"click_id":      clickID,
	}
	if xhsAuthEnabled {
		token, err := getXHSAccessToken()
		if err != nil {
			return -2, fmt.Errorf("get access token: %w", err)
		}
		body["access_token"] = token
	}
	var out xhsResp
	if err := xhsPost(body, &out); err != nil {
		return -2, err
	}
	return out.Code, nil
}

// --- access token cache (7200s ttl, refreshed a minute early) ---

var (
	xhsTokenMu  sync.Mutex
	xhsToken    string
	xhsTokenExp int64 // unix ms
)

func getXHSAccessToken() (string, error) {
	xhsTokenMu.Lock()
	defer xhsTokenMu.Unlock()
	now := time.Now().UnixMilli()
	if xhsToken != "" && now < xhsTokenExp-60_000 {
		return xhsToken, nil
	}
	var out xhsResp
	if err := xhsPost(map[string]interface{}{
		"advertiser_id": xhsAdvertiserID,
		"method":        "oauth.getAccessToken",
	}, &out); err != nil {
		return "", err
	}
	if out.Code != 0 || out.Data.AccessToken == "" {
		return "", fmt.Errorf("getAccessToken code=%d msg=%s", out.Code, out.Msg)
	}
	ttl := out.Data.ExpireIn
	if ttl <= 0 {
		ttl = 7200
	}
	xhsToken = out.Data.AccessToken
	xhsTokenExp = now + ttl*1000
	return xhsToken, nil
}

type xhsResp struct {
	Code    int    `json:"code"`
	Msg     string `json:"msg"`
	Success bool   `json:"success"`
	Data    struct {
		AccessToken string `json:"access_token"`
		ExpireIn    int64  `json:"access_token_expire_in"`
	} `json:"data"`
}

func xhsPost(body map[string]interface{}, out *xhsResp) error {
	buf, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, xhsAPIBase+xhsCommonPath, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := xhsHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusBadRequest {
		return fmt.Errorf("xhs api 400 (unparseable body)")
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}
