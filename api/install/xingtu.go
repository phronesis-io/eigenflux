package install

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
)

const (
	xingtuCallbackURL = "https://ad.oceanengine.com/track/activate/"
	// Xingtu clickid is opaque. Keep a generous but bounded size and reject
	// control characters before persisting it or placing it in the callback URL.
	xingtuClickIDMaxLength = 2048
)

func normalizeXingtuClickID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > xingtuClickIDMaxLength {
		return ""
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	return value
}

func fireXingtuCallback(ref, eventType string) {
	go func() {
		won, tok, err := claimXingtuCallback(db.DB, ref, eventType)
		if err != nil {
			logger.Default().Error("xingtu callback claim failed", "ref", ref, "event_type", eventType, "err", err)
			return
		}
		if !won || tok.XingtuClickID == "" {
			return
		}
		code, err := reportXingtuConversion(tok.XingtuClickID, eventType)
		if err != nil {
			logger.Default().Error("xingtu callback failed", "ref", ref, "event_type", eventType, "code", code, "err", err)
		}
		if err := setXingtuCallbackCode(db.DB, ref, eventType, code); err != nil {
			logger.Default().Error("xingtu callback state update failed", "ref", ref, "event_type", eventType, "err", err)
		}
		if code == 0 {
			event("install_callback_xingtu", ref, "channel", tok.Channel, "event_type", eventType)
		}
	}()
}

func reportXingtuConversion(clickID, eventType string) (int, error) {
	q := url.Values{"callback": {clickID}, "event_type": {eventType}}
	req, err := http.NewRequest(http.MethodGet, xingtuCallbackURL+"?"+q.Encode(), nil)
	if err != nil {
		return -2, err
	}
	resp, err := xingtuHTTP.Do(req)
	if err != nil {
		return -2, err
	}
	defer resp.Body.Close()
	var body struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return -2, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("xingtu HTTP %d: %s", resp.StatusCode, body.Msg)
	}
	if body.Code != 0 {
		return body.Code, fmt.Errorf("xingtu code=%d: %s", body.Code, body.Msg)
	}
	return 0, nil
}

var xingtuHTTP = &http.Client{Timeout: 8 * time.Second}
