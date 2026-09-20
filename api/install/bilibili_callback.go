package install

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
)

const (
	bilibiliConversionURL    = "https://cm.bilibili.com/conv/api/conversion/ad/cb/v1"
	bilibiliEventFormSubmit  = "FORM_SUBMIT"
	bilibiliEventClueValid   = "CLUE_VALID"
	bilibiliTrackIDMaxLength = 512
)

var (
	bilibiliCallbackEnabled bool
	bilibiliCallbackBase    string
	bilibiliHTTP            = &http.Client{Timeout: 8 * time.Second}
	bilibiliRetryDelays     = []time.Duration{0, 200 * time.Millisecond, time.Second}
)

func initBilibiliConfig() {
	bilibiliCallbackEnabled = envBool("BILIBILI_CALLBACK_ENABLED", true)
	bilibiliCallbackBase = strings.TrimRight(envStr("BILIBILI_CALLBACK_BASE", bilibiliConversionURL), "?")
}

func normalizeBilibiliTrackID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > bilibiliTrackIDMaxLength {
		return ""
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	return value
}

func fireBilibiliCallback(ref, eventType string) {
	if !bilibiliCallbackEnabled {
		return
	}
	go func() {
		won, tok, err := claimBilibiliCallback(db.DB, ref, eventType)
		if err != nil {
			logger.Default().Error("bilibili callback claim failed", "ref", ref, "conv_type", eventType, "err", err)
			return
		}
		if !won || tok.BilibiliTrackID == "" {
			return
		}
		code, err := reportBilibiliConversionWithRetry(tok.BilibiliTrackID, eventType, bilibiliEventTimestamp(tok, eventType), tok.ClientIP)
		if err != nil {
			logger.Default().Error("bilibili callback failed", "ref", ref, "conv_type", eventType, "code", code, "err", err)
		}
		if err := setBilibiliCallbackCode(db.DB, ref, eventType, code); err != nil {
			logger.Default().Error("bilibili callback state update failed", "ref", ref, "conv_type", eventType, "err", err)
		}
		if code == 0 {
			event("install_callback_bilibili", ref, "channel", tok.Channel, "conv_type", eventType)
		}
	}()
}

func reportBilibiliConversionWithRetry(trackID, eventType string, timestamp int64, clientIP string) (int, error) {
	var code int
	var err error
	for attempt, delay := range bilibiliRetryDelays {
		if delay > 0 {
			time.Sleep(delay)
		}
		code, err = reportBilibiliConversion(trackID, eventType, timestamp, clientIP)
		if err == nil || (code != -2 && code < http.StatusInternalServerError) {
			return code, err
		}
		if attempt == len(bilibiliRetryDelays)-1 {
			break
		}
	}
	return code, err
}

func bilibiliEventTimestamp(tok *Token, eventType string) int64 {
	if eventType == bilibiliEventClueValid {
		return tok.ReportedAt
	}
	return tok.CopiedAt
}

func reportBilibiliConversion(trackID, eventType string, timestamp int64, clientIP string) (int, error) {
	q := url.Values{
		"conv_type": {eventType},
		"track_id":  {trackID},
		"conv_time": {strconv.FormatInt(timestamp, 10)},
	}
	if clientIP != "" {
		q.Set("client_ip", clientIP)
	}
	req, err := http.NewRequest(http.MethodPost, bilibiliCallbackBase+"?"+q.Encode(), nil)
	if err != nil {
		return -2, err
	}
	resp, err := bilibiliHTTP.Do(req)
	if err != nil {
		return -2, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return -2, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("bilibili HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	// The endpoint has returned both an empty 2xx body and a JSON result across
	// versions. A present non-zero code is an explicit platform rejection.
	if len(strings.TrimSpace(string(body))) > 0 {
		var result struct {
			Code    *int   `json:"code"`
			Message string `json:"message"`
			Msg     string `json:"msg"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			return -2, fmt.Errorf("invalid bilibili response: %w", err)
		}
		if result.Code == nil {
			return -2, fmt.Errorf("bilibili response missing code")
		}
		if *result.Code != 0 {
			message := result.Message
			if message == "" {
				message = result.Msg
			}
			return *result.Code, fmt.Errorf("bilibili code=%d: %s", *result.Code, message)
		}
	}
	return 0, nil
}
