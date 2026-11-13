package install

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
	"gorm.io/gorm"
)

const (
	metaCopyEvent    = "CopyCommand"
	metaInstallEvent = "CompleteRegistration"
)

var (
	metaEnabled                                                     bool
	metaPixelID, metaAccessToken, metaAPIVersion, metaTestEventCode string
	metaHTTP                                                        = &http.Client{Timeout: 10 * time.Second}
	metaGraphURL                                                    = "https://graph.facebook.com"
	metaCookiePattern                                               = regexp.MustCompile(`^fb\.[0-9]+\.[0-9]{13}\.[A-Za-z0-9_-]+$`)
	metaClickPattern                                                = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

func initMetaConfig() {
	metaEnabled = envBool("META_ADS_ENABLED", false)
	metaPixelID = digitsOnly(envStr("META_PIXEL_ID", ""))
	metaAccessToken = envStr("META_CAPI_ACCESS_TOKEN", "")
	metaAPIVersion = strings.Trim(envStr("META_API_VERSION", "v25.0"), "/")
	metaTestEventCode = envStr("META_TEST_EVENT_CODE", "")
	if metaEnabled && (metaPixelID == "" || metaAccessToken == "") {
		logger.Default().Error("Meta callbacks enabled but pixel ID or CAPI token is missing")
	}
}

func normalizeMetaCookie(value string) string {
	if len(value) > 2200 || !metaCookiePattern.MatchString(value) {
		return ""
	}
	return value
}

// Keep the landing browser's click time; never substitute the installing Agent's
// IP, UA or cookies for the visitor who clicked the ad.
func validMetaClick(value string) bool {
	return len(value) <= 2048 && metaClickPattern.MatchString(value)
}

func metaFbc(fbclid, fbc string, now int64) string {
	if !validMetaClick(fbclid) {
		return ""
	}
	fbc = normalizeMetaCookie(fbc)
	if strings.HasSuffix(fbc, "."+fbclid) {
		return fbc
	}
	return fmt.Sprintf("fb.1.%d.%s", now, fbclid)
}

func metaColumns(name string) (code, sent, signal string) {
	switch name {
	case metaCopyEvent:
		return "meta_copy_code", "meta_copy_sent_at", "copied_at"
	case metaInstallEvent:
		return "meta_install_code", "meta_install_sent_at", "meta_provisioned_at"
	default:
		return "", "", ""
	}
}

func claimMetaCallback(gdb *gorm.DB, ref, name string) (*Token, int64, error) {
	code, sent, signal := metaColumns(name)
	if code == "" {
		return nil, 0, fmt.Errorf("unsupported Meta event")
	}
	now := time.Now().UnixMilli()
	eligible := gdb.Model(&Token{}).Where(fmt.Sprintf(
		"token = ? AND channel = 'facebook' AND (meta_fbc <> '' OR meta_fbp <> '') AND %s > 0 AND %s <> 0 AND (%s = 0 OR %s < ?)", signal, code, sent, sent), ref, now-time.Minute.Milliseconds())
	if name == metaInstallEvent {
		eligible = eligible.Where("reported_at > 0")
	} else {
		// A delayed copy retry must still overlap Meta's 48-hour Pixel dedup window.
		eligible = eligible.Where("copied_at > ?", now-(47*time.Hour).Milliseconds())
	}
	result := eligible.Update(sent, now)
	if result.Error != nil {
		return nil, 0, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, 0, nil
	}
	var tok Token
	if err := gdb.First(&tok, "token = ?", ref).Error; err != nil {
		return nil, 0, err
	}
	return &tok, now, nil
}

// SendMetaProvisionConversion retries delivery of a committed provision signal.
func SendMetaProvisionConversion(ref string) {
	if ref != "" {
		fireMetaCallback(ref, metaInstallEvent)
	}
}

func fireMetaCallback(ref, name string) {
	if !metaEnabled || metaPixelID == "" || metaAccessToken == "" {
		return
	}
	go func() {
		tok, lease, err := claimMetaCallback(db.DB, ref, name)
		if err != nil {
			logger.Default().Error("Meta callback claim failed", "ref", ref, "err", err)
			return
		}
		if tok == nil {
			return
		}
		code := -2
		for attempt := 0; attempt < 3; attempt++ {
			if attempt > 0 {
				time.Sleep(time.Duration(attempt*2) * time.Second)
			}
			code, err = reportMetaConversion(tok, name)
			if code == 0 || (code > 0 && code != http.StatusTooManyRequests && code < 500) {
				break
			}
		}
		if err != nil {
			logger.Default().Error("Meta callback failed", "ref", ref, "event_name", name, "code", code, "err", err)
		}
		codeCol, sentCol, _ := metaColumns(name)
		if err := db.DB.Model(&Token{}).Where("token = ? AND "+sentCol+" = ?", ref, lease).Update(codeCol, code).Error; err != nil {
			logger.Default().Error("Meta callback result update failed", "ref", ref, "err", err)
		}
		if code == 0 {
			event("install_callback_meta", ref, "channel", tok.Channel, "event_name", name)
		}
	}()
}

func reportMetaConversion(tok *Token, name string) (int, error) {
	_, _, signal := metaColumns(name)
	if signal == "" {
		return -2, fmt.Errorf("unsupported Meta event")
	}
	at, stage, source := tok.CopiedAt, "copy", "website"
	if name == metaInstallEvent {
		if tok.MetaProvisionedAt <= 0 || tok.ReportedAt <= 0 {
			return -2, fmt.Errorf("Meta install requires provision and report timestamps")
		}
		at, stage, source = max(tok.MetaProvisionedAt, tok.ReportedAt), "install", "other"
	}
	if at <= 0 {
		return -2, fmt.Errorf("Meta event has no confirmed timestamp")
	}
	user := map[string]string{}
	if tok.MetaFbc != "" {
		user["fbc"] = tok.MetaFbc
	}
	if tok.MetaFbp != "" {
		user["fbp"] = tok.MetaFbp
	}
	if tok.ClientIP != "" {
		user["client_ip_address"] = tok.ClientIP
	}
	if tok.MetaUserAgent != "" {
		user["client_user_agent"] = tok.MetaUserAgent
	}
	conversion := map[string]interface{}{
		"event_name": name, "event_time": at / 1000,
		"event_id": "eigenflux:" + stage + ":" + tok.Token, "action_source": source,
		"user_data": user,
	}
	if name == metaCopyEvent {
		conversion["event_source_url"] = "https://www.eigenflux.ai/install"
	}
	payload := map[string]interface{}{"data": []interface{}{conversion}}
	if metaTestEventCode != "" {
		payload["test_event_code"] = metaTestEventCode
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return -2, err
	}
	req, err := http.NewRequest(http.MethodPost, metaGraphURL+"/"+metaAPIVersion+"/"+metaPixelID+"/events", bytes.NewReader(raw))
	if err != nil {
		return -2, err
	}
	// Keep credentials out of request URLs and errors.
	req.Header.Set("Authorization", "Bearer "+metaAccessToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := metaHTTP.Do(req)
	if err != nil {
		return -2, fmt.Errorf("Meta transport failed")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return -2, fmt.Errorf("Meta response read failed")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("Meta HTTP %d", resp.StatusCode)
	}
	var result struct {
		EventsReceived int             `json:"events_received"`
		Error          json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return -2, fmt.Errorf("invalid Meta response")
	}
	if result.EventsReceived != 1 || len(result.Error) > 0 && string(result.Error) != "null" {
		return -2, fmt.Errorf("Meta did not accept the event")
	}
	return 0, nil
}
