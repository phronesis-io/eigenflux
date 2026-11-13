package install

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
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
	metaSendSlots                                                   = make(chan struct{}, 32)
	metaCookiePattern                                               = regexp.MustCompile(`^fb\.[0-9]+\.[0-9]{13}\.[A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]+)?$`)
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
	parts := strings.SplitN(fbc, ".", 5)
	if len(parts) >= 4 && parts[3] == fbclid {
		return fbc
	}
	return fmt.Sprintf("fb.1.%d.%s", now, fbclid)
}

func metaColumns(name string) (code, sent, signal string) {
	switch name {
	case metaCopyEvent:
		return "meta_copy_code", "meta_copy_sent_at", "copied_at"
	case metaInstallEvent:
		return "meta_install_code", "meta_install_sent_at", "meta_registered_at"
	default:
		return "", "", ""
	}
}

func pendingMetaCallbacks(gdb *gorm.DB, name string, now time.Time) *gorm.DB {
	code, sent, signal := metaColumns(name)
	// Copy can overlap a Pixel event. Leave room for transport retries inside
	// Meta's 48-hour deduplication and seven-day event acceptance limits.
	maxAge := 6 * 24 * time.Hour
	if name == metaCopyEvent {
		maxAge = 47 * time.Hour
	}
	return gdb.Model(&Token{}).Where(fmt.Sprintf(
		"channel = 'facebook' AND (meta_fbc <> '' OR meta_fbp <> '') AND %s > 0 AND %s > ? AND %s <> 0 AND (%s = 0 OR %s < ?)"+
			" AND (%s < 400 OR %s >= 500 OR %s = 429 OR %s < ?)",
		signal, signal, code, sent, sent, code, code, code, sent),
		now.Add(-maxAge).UnixMilli(), now.Add(-time.Minute).UnixMilli(), now.Add(-15*time.Minute).UnixMilli())
}

func claimMetaCallback(gdb *gorm.DB, ref, name string) (*Token, int64, error) {
	_, sent, _ := metaColumns(name)
	if sent == "" {
		return nil, 0, fmt.Errorf("unsupported Meta event")
	}
	now := time.Now()
	result := pendingMetaCallbacks(gdb, name, now).Where("token = ?", ref).Update(sent, now.UnixMilli())
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
	return &tok, now.UnixMilli(), nil
}

// RecordVerifiedMetaRegistration records the first email-verified registration
// for a ref bound during signed provision. Call inside the email transaction.
func RecordVerifiedMetaRegistration(tx *gorm.DB, agentID, verifiedAt int64, sourceURL string) (string, error) {
	var ref string
	err := tx.Raw(`UPDATE install_tokens
		SET meta_registered_at = ?, meta_registration_url = ?
		WHERE token = (SELECT ref FROM install_meta_registration_refs WHERE agent_id = ?)
		  AND meta_registered_at = 0
		RETURNING token`, verifiedAt, sourceURL, agentID).Scan(&ref).Error
	return ref, err
}

// SendVerifiedMetaRegistration schedules delivery after verification commits.
func SendVerifiedMetaRegistration(ref string) {
	if ref != "" {
		fireMetaCallback(ref, metaInstallEvent)
	}
}

func metaDeliveryEnabled() bool {
	return metaEnabled && metaPixelID != "" && metaAccessToken != ""
}

// recoverMetaCallbacks re-enqueues durable signals after a restart or a failed
// send. The per-event claim also fences sends from other replicas and requests.
func recoverMetaCallbacks(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for ctx.Err() == nil {
		for _, name := range []string{metaCopyEvent, metaInstallEvent} {
			retryPendingMetaCallbacks(ctx, name)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func retryPendingMetaCallbacks(ctx context.Context, name string) {
	_, sent, signal := metaColumns(name)
	var refs []string
	err := pendingMetaCallbacks(db.DB.WithContext(ctx), name, time.Now()).
		Order(sent+" ASC, "+signal+" ASC").Limit(50).Pluck("token", &refs).Error
	if err != nil {
		logger.Default().Error("Meta callback recovery scan failed", "event_name", name, "err", err)
		return
	}
	for _, ref := range refs {
		fireMetaCallback(ref, name)
	}
}

func fireMetaCallback(ref, name string) {
	if !metaDeliveryEnabled() {
		return
	}
	select {
	case metaSendSlots <- struct{}{}:
	default:
		// The periodic scan can pick up work when capacity frees up.
		return
	}
	go func() {
		defer func() { <-metaSendSlots }()
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
	at, stage, sourceURL := tok.MetaRegisteredAt, "install", tok.MetaRegistrationURL
	if name == metaCopyEvent {
		at, stage, sourceURL = tok.CopiedAt, "copy", "https://www.eigenflux.ai/install"
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
	if net.ParseIP(tok.ClientIP) != nil {
		user["client_ip_address"] = tok.ClientIP
	}
	if tok.MetaUserAgent != "" {
		user["client_user_agent"] = tok.MetaUserAgent
	}
	payload := map[string]interface{}{"data": []interface{}{map[string]interface{}{
		"event_name": name, "event_time": at / 1000,
		"event_id":      "eigenflux:" + stage + ":" + tok.Token,
		"action_source": "website", "event_source_url": sourceURL,
		"user_data": user,
	}}}
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
	var result struct {
		EventsReceived int `json:"events_received"`
		Error          *struct {
			Code        int  `json:"code"`
			Subcode     int  `json:"error_subcode"`
			IsTransient bool `json:"is_transient"`
		} `json:"error"`
	}
	decodeErr := json.Unmarshal(body, &result)
	if result.Error != nil {
		code := resp.StatusCode
		if result.Error.IsTransient || code < 400 {
			code = -2
		}
		return code, fmt.Errorf("Meta HTTP %d, Graph code %d, subcode %d, transient %t",
			resp.StatusCode, result.Error.Code, result.Error.Subcode, result.Error.IsTransient)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("Meta HTTP %d", resp.StatusCode)
	}
	if decodeErr != nil {
		return -2, fmt.Errorf("invalid Meta response")
	}
	if result.EventsReceived != 1 {
		return -2, fmt.Errorf("Meta did not accept the event")
	}
	return 0, nil
}
