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
	oceanengineH5ConversionURL        = "https://analytics.oceanengine.com/api/v2/conversion"
	oceanengineOmnichannelURL         = "https://analytics.oceanengine.com/api/v1/attribution"
	oceanengineEventForm              = "form"
	oceanengineEventCustomerEffective = "customer_effective"
	oceanengineDestinationH5          = "h5"
	oceanengineDestinationOmnichannel = "omnichannel"
	oceanengineClickIDMaxLength       = 2048
)

var (
	oceanengineEnabled              bool
	oceanengineOmnichannelAuthToken string
	oceanengineHTTP                 = &http.Client{Timeout: 8 * time.Second}
)

func initOceanengineConfig() {
	oceanengineEnabled = strings.EqualFold(envStr("OCEANENGINE_CALLBACK_ENABLED", "true"), "true")
	oceanengineOmnichannelAuthToken = strings.TrimSpace(envStr("OCEANENGINE_OMNICHANNEL_AUTH_TOKEN", ""))
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

func fireOceanengineCallbacks(ref, eventType string) {
	if !oceanengineEnabled {
		return
	}
	fireOceanengineCallback(ref, oceanengineDestinationH5, eventType)
	if oceanengineOmnichannelAuthToken != "" {
		fireOceanengineCallback(ref, oceanengineDestinationOmnichannel, eventType)
	}
}

func fireOceanengineCallback(ref, destination, eventType string) {
	go func() {
		won, tok, err := claimOceanengineCallback(db.DB, ref, destination, eventType)
		if err != nil {
			logger.Default().Error("oceanengine callback claim failed", "ref", ref, "destination", destination, "event_type", eventType, "err", err)
			return
		}
		if !won || tok.OceanengineClickID == "" {
			return
		}
		timestamp := oceanengineEventTimestamp(tok, eventType)
		var code int
		if destination == oceanengineDestinationOmnichannel {
			code, err = reportOceanengineOmnichannelConversion(tok.OceanengineClickID, eventType, timestamp, oceanengineOmnichannelAuthToken)
		} else {
			code, err = reportOceanengineH5Conversion(tok.OceanengineClickID, eventType, timestamp)
		}
		if err != nil {
			logger.Default().Error("oceanengine callback failed", "ref", ref, "destination", destination, "event_type", eventType, "code", code, "err", err)
		}
		if err := setOceanengineCallbackCode(db.DB, ref, destination, eventType, code); err != nil {
			logger.Default().Error("oceanengine callback state update failed", "ref", ref, "destination", destination, "event_type", eventType, "err", err)
		}
		if code == 0 {
			event("install_callback_oceanengine", ref, "channel", tok.Channel, "destination", destination, "event_type", eventType)
		}
	}()
}

func oceanengineEventTimestamp(tok *Token, eventType string) int64 {
	if eventType == oceanengineEventCustomerEffective {
		return tok.ReportedAt
	}
	return tok.CopiedAt
}

func reportOceanengineH5Conversion(clickID, eventType string, timestamp int64) (int, error) {
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
	return postOceanengineJSON(oceanengineH5ConversionURL, payload)
}

func reportOceanengineOmnichannelConversion(clickID, eventType string, timestampMS int64, authToken string) (int, error) {
	timestampSeconds := timestampMS / 1000
	payload := struct {
		EventType string `json:"event_type"`
		BizType   int    `json:"biz_type"`
		Context   struct {
			Ad struct {
				ClickID   string `json:"clickid"`
				AuthToken string `json:"auth_token"`
			} `json:"ad"`
		} `json:"context"`
		Properties struct {
			AuthToken string `json:"auth_token"`
			EventTime int64  `json:"event_time"`
			ClickID   string `json:"clickid"`
		} `json:"properties"`
		AttributeLabel string `json:"attribute_label"`
		Source         string `json:"source"`
		Timestamp      int64  `json:"timestamp"`
	}{
		EventType:      eventType,
		BizType:        23,
		AttributeLabel: "convert",
		Source:         "oto",
		Timestamp:      timestampSeconds,
	}
	payload.Context.Ad.ClickID = clickID
	payload.Context.Ad.AuthToken = authToken
	payload.Properties.AuthToken = authToken
	payload.Properties.EventTime = timestampSeconds
	payload.Properties.ClickID = clickID
	return postOceanengineJSON(oceanengineOmnichannelURL, payload)
}

func postOceanengineJSON(url string, payload interface{}) (int, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return -2, err
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
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
