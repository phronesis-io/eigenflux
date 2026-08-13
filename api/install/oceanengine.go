package install

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
)

const (
	oceanengineConversionURL    = "https://analytics.oceanengine.com/api/v2/conversion"
	oceanengineEventActive      = "active"
	oceanengineEventRegister    = "active_register"
	oceanengineClickIDMaxLength = 2048
)

var (
	oceanengineEnabled bool
	oceanengineHTTP    = &http.Client{Timeout: 8 * time.Second}
)

func initOceanengineConfig() {
	oceanengineEnabled = strings.EqualFold(envStr("OCEANENGINE_CALLBACK_ENABLED", "true"), "true")
}

func normalizeOceanengineClickID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > oceanengineClickIDMaxLength {
		return ""
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	return value
}

func fireOceanengineCallback(ref, eventType string) {
	if !oceanengineEnabled {
		return
	}
	go func() {
		won, tok, err := claimOceanengineCallback(db.DB, ref, eventType)
		if err != nil {
			logger.Default().Error("oceanengine callback claim failed", "ref", ref, "event_type", eventType, "err", err)
			return
		}
		if !won || tok.OceanengineClickID == "" {
			return
		}
		timestamp := oceanengineEventTimestamp(tok, eventType)
		code, err := reportOceanengineConversion(tok.OceanengineClickID, eventType, timestamp)
		if err != nil {
			logger.Default().Error("oceanengine callback failed", "ref", ref, "event_type", eventType, "code", code, "err", err)
		}
		if err := setOceanengineCallbackCode(db.DB, ref, eventType, code); err != nil {
			logger.Default().Error("oceanengine callback state update failed", "ref", ref, "event_type", eventType, "err", err)
		}
		if code == 0 {
			event("install_callback_oceanengine", ref, "channel", tok.Channel, "event_type", eventType)
		}
	}()
}

func oceanengineEventTimestamp(tok *Token, eventType string) int64 {
	if eventType == oceanengineEventRegister {
		return tok.ReportedAt
	}
	return tok.CopiedAt
}

func reportOceanengineConversion(clickID, eventType string, timestamp int64) (int, error) {
	payload := struct {
		EventType string `json:"event_type"`
		Context   struct {
			Ad struct {
				Callback string `json:"callback"`
			} `json:"ad"`
		} `json:"context"`
		Timestamp int64 `json:"timestamp"`
	}{EventType: eventType, Timestamp: timestamp}
	payload.Context.Ad.Callback = clickID
	body, err := json.Marshal(payload)
	if err != nil {
		return -2, err
	}
	req, err := http.NewRequest(http.MethodPost, oceanengineConversionURL, bytes.NewReader(body))
	if err != nil {
		return -2, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := oceanengineHTTP.Do(req)
	if err != nil {
		return -2, err
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return -2, err
	}
	var result struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return -2, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("oceanengine HTTP %d: %s", resp.StatusCode, result.Message)
	}
	if result.Code != 0 {
		return result.Code, fmt.Errorf("oceanengine code=%d: %s", result.Code, result.Message)
	}
	return 0, nil
}
