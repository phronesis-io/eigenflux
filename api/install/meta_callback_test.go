package install

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"eigenflux_server/pkg/db"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestMetaClickAttribution(t *testing.T) {
	const now = int64(1794480000000)
	const cookie = "fb.1.1794470000000.click_123"
	if got := metaFbc("click_123", cookie, now); got != cookie {
		t.Fatalf("click time changed: %s", got)
	}
	if got := metaFbc("click_123", cookie+".A", now); got != cookie+".A" {
		t.Fatalf("extended click cookie changed: %s", got)
	}
	if got := metaFbc("new-click", cookie, now); got != "fb.1.1794480000000.new-click" {
		t.Fatal(got)
	}
	if got := metaFbc("bad\nclick", cookie, now); got != "" {
		t.Fatal(got)
	}
	if got := normalizeMetaCookie(strings.Repeat("x", 3000)); got != "" {
		t.Fatal("oversized cookie accepted")
	}
	for _, click := range []string{"", strings.Repeat("x", 2049), "bad.click"} {
		if validMetaClick(click) {
			t.Fatal("invalid click accepted")
		}
	}
	if got := deriveChannel("", "google", "", "", "", "", "", "", cookie); got != "facebook" {
		t.Fatal(got)
	}
	if got := deriveChannel("", "google", "", "", "", "", "", "", ""); got != "google" {
		t.Fatal(got)
	}
	for _, source := range []string{"meta", "facebook", "fb", "instagram", "ig"} {
		if normalizeChannel(source) != "facebook" {
			t.Fatal(source)
		}
	}
}

func TestMetaRefConversionLifecycle(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := gdb.DB()
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := gdb.AutoMigrate(&Token{}); err != nil {
		t.Fatal(err)
	}
	oldDB, oldEnabled := db.DB, metaEnabled
	oldURL, oldHTTP, oldID, oldToken, oldVersion, oldTest := metaGraphURL, metaHTTP, metaPixelID, metaAccessToken, metaAPIVersion, metaTestEventCode
	t.Cleanup(func() {
		db.DB, metaEnabled = oldDB, oldEnabled
		metaGraphURL, metaHTTP, metaPixelID, metaAccessToken, metaAPIVersion, metaTestEventCode = oldURL, oldHTTP, oldID, oldToken, oldVersion, oldTest
	})
	events := make(chan map[string]interface{}, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Data []map[string]interface{} `json:"data"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || len(payload.Data) != 1 {
			t.Error("invalid event payload")
			w.WriteHeader(400)
			return
		}
		events <- payload.Data[0]
		_, _ = w.Write([]byte(`{"events_received":1}`))
	}))
	defer srv.Close()
	db.DB, metaEnabled = gdb, true
	metaGraphURL, metaHTTP = srv.URL, srv.Client()
	metaPixelID, metaAccessToken, metaAPIVersion, metaTestEventCode = "123", "local-test", "v25.0", ""
	h := server.New()
	h.POST("/token", mintRef)
	h.POST("/copy", reportCopy)
	h.POST("/report", reportInstall)
	post := func(path, body string) []byte {
		t.Helper()
		resp := ut.PerformRequest(h.Engine, http.MethodPost, path, &ut.Body{Body: strings.NewReader(body), Len: len(body)}, ut.Header{Key: "User-Agent", Value: "landing-browser"}).Result()
		if resp.StatusCode() != 200 {
			t.Fatalf("%s: %d %s", path, resp.StatusCode(), resp.Body())
		}
		return resp.Body()
	}
	var minted struct {
		Data struct {
			Ref string `json:"ref"`
		} `json:"data"`
	}
	if err := json.Unmarshal(post("/token", `{"fbclid":"test-click","meta_fbp":"fb.1.1794470000000.123","utm_source":"google"}`), &minted); err != nil {
		t.Fatal(err)
	}
	ref := minted.Data.Ref
	if !ValidTokenFormat(ref) {
		t.Fatal("no valid ref")
	}
	if claimed, _, err := claimMetaCallback(gdb, ref, metaInstallEvent); err != nil || claimed != nil {
		t.Fatal("install claimed before success", err)
	}
	body := `{"ref":"` + ref + `"}`
	for _, want := range []bool{true, false} {
		var reply struct {
			Data struct {
				FirstCopy bool `json:"first_copy"`
			} `json:"data"`
		}
		if err := json.Unmarshal(post("/copy", body), &reply); err != nil || reply.Data.FirstCopy != want {
			t.Fatalf("first copy = %t, want %t, decode error: %v", reply.Data.FirstCopy, want, err)
		}
	}
	waitForCode := func(column string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			var code int
			if err := gdb.Model(&Token{}).Select(column).Where("token = ?", ref).Scan(&code).Error; err != nil {
				t.Fatal(err)
			}
			if code == 0 {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("callback did not complete", column)
	}
	waitForCode("meta_copy_code")
	post("/report", body)
	post("/report", body)
	if claimed, _, err := claimMetaCallback(gdb, ref, metaInstallEvent); err != nil || claimed != nil {
		t.Fatal("public reports made registration eligible", err)
	}
	if err := gdb.Exec("CREATE TABLE install_meta_registration_refs (agent_id INTEGER PRIMARY KEY, ref TEXT)").Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec("INSERT INTO install_meta_registration_refs (agent_id, ref) VALUES (10, ?)", ref).Error; err != nil {
		t.Fatal(err)
	}
	if confirmed, err := RecordVerifiedMetaRegistration(gdb, 10, time.Now().UnixMilli(), "https://www.eigenflux.ai/dashboard/claim"); err != nil || confirmed != ref {
		t.Fatalf("verified registration ref=%q err=%v", confirmed, err)
	}
	// Recovery sends committed work even if the request never dispatches it.
	retryPendingMetaCallbacks(t.Context(), metaInstallEvent)
	waitForCode("meta_install_code")
	for _, name := range []string{metaCopyEvent, metaInstallEvent} {
		e := <-events
		if e["event_name"] != name {
			t.Fatal(e)
		}
		user := e["user_data"].(map[string]interface{})
		if user["client_user_agent"] != "landing-browser" || !strings.HasSuffix(user["fbc"].(string), ".test-click") {
			t.Fatal(user)
		}
		if claimed, _, err := claimMetaCallback(gdb, ref, name); err != nil || claimed != nil {
			t.Fatal("successful callback reclaimed", err)
		}
	}
	var tok Token
	if err := gdb.First(&tok, "token = ?", ref).Error; err != nil {
		t.Fatal(err)
	}
	if tok.Channel != "facebook" || tok.ReportCount != 2 || tok.Status != StatusInstalled {
		t.Fatal(tok.Channel, tok.ReportCount, tok.Status)
	}
	select {
	case e := <-events:
		t.Fatal("duplicate conversion", e)
	default:
	}
	// An in-flight attempt is protected; a failed expired attempt is reclaimable.
	if err := gdb.Model(&Token{}).Where("token = ?", ref).Updates(map[string]interface{}{"meta_install_code": -2, "meta_install_sent_at": time.Now().UnixMilli()}).Error; err != nil {
		t.Fatal(err)
	}
	if claimed, _, err := claimMetaCallback(gdb, ref, metaInstallEvent); err != nil || claimed != nil {
		t.Fatal("active lease reclaimed", err)
	}
	if err := gdb.Model(&Token{}).Where("token = ?", ref).Update("meta_install_sent_at", time.Now().Add(-2*time.Minute).UnixMilli()).Error; err != nil {
		t.Fatal(err)
	}
	if claimed, _, err := claimMetaCallback(gdb, ref, metaInstallEvent); err != nil || claimed == nil {
		t.Fatal("expired lease did not recover", err)
	}
	if err := gdb.Model(&Token{}).Where("token = ?", ref).Updates(map[string]interface{}{
		"meta_install_sent_at": 0, "meta_registered_at": time.Now().Add(-7 * 24 * time.Hour).UnixMilli(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if claimed, _, err := claimMetaCallback(gdb, ref, metaInstallEvent); err != nil || claimed != nil {
		t.Fatal("stale event remained eligible", err)
	}
}

func TestMetaConversionPayload(t *testing.T) {
	oldURL, oldHTTP, oldID, oldToken, oldVersion, oldTest := metaGraphURL, metaHTTP, metaPixelID, metaAccessToken, metaAPIVersion, metaTestEventCode
	t.Cleanup(func() {
		metaGraphURL, metaHTTP, metaPixelID, metaAccessToken, metaAPIVersion, metaTestEventCode = oldURL, oldHTTP, oldID, oldToken, oldVersion, oldTest
	})
	metaPixelID, metaAccessToken, metaAPIVersion, metaTestEventCode = "123", "test-secret", "v25.0", "TEST123"
	var payload struct {
		Data []struct {
			Name   string            `json:"event_name"`
			Time   int64             `json:"event_time"`
			ID     string            `json:"event_id"`
			URL    string            `json:"event_source_url"`
			Source string            `json:"action_source"`
			User   map[string]string `json:"user_data"`
		} `json:"data"`
		Test string `json:"test_event_code"`
	}
	response := `{"events_received":1}`
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v25.0/123/events" || r.URL.RawQuery != "" {
			t.Errorf("wrong target: %s %s", r.Method, r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Error("missing server authentication")
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(response))
	}))
	defer srv.Close()
	metaGraphURL, metaHTTP = srv.URL, srv.Client()
	tok := &Token{Token: "EF-Abcd1234", MetaFbc: "fb.1.1794470000000.click", MetaFbp: "fb.1.1794470000000.123", ClientIP: "203.0.113.8", MetaUserAgent: "landing-browser", CopiedAt: 1794480000000, MetaRegisteredAt: 1794480010000, MetaRegistrationURL: "https://www.eigenflux.ai/dashboard/claim"}
	for _, name := range []string{metaCopyEvent, metaInstallEvent} {
		code, err := reportMetaConversion(tok, name)
		if err != nil || code != 0 {
			t.Fatalf("code=%d err=%v", code, err)
		}
		e := payload.Data[0]
		stage, at, source := "install", tok.MetaRegisteredAt, tok.MetaRegistrationURL
		if name == metaCopyEvent {
			stage, at, source = "copy", tok.CopiedAt, "https://www.eigenflux.ai/install"
		}
		if e.Name != name || e.ID != "eigenflux:"+stage+":"+tok.Token || e.Time != at/1000 {
			t.Fatalf("wrong event: %+v", e)
		}
		if e.URL != source || e.Source != "website" || payload.Test != "TEST123" {
			t.Fatalf("wrong source: %+v", payload)
		}
		if e.User["fbc"] != tok.MetaFbc || e.User["fbp"] != tok.MetaFbp || e.User["client_user_agent"] != "landing-browser" || e.User["client_ip_address"] != tok.ClientIP {
			t.Fatal(e.User)
		}
	}
	for _, body := range []string{`{}`, `{"events_received":0}`, `{"events_received":1,"error":{"code":190}}`, `invalid`} {
		response = body
		if code, err := reportMetaConversion(tok, metaInstallEvent); code == 0 || err == nil {
			t.Fatalf("accepted invalid response %s", body)
		}
	}
	status, response = http.StatusBadRequest, `{"error":{"message":"test-secret"}}`
	if code, err := reportMetaConversion(tok, metaInstallEvent); code != 400 || err == nil || strings.Contains(err.Error(), "test-secret") {
		t.Fatalf("unsafe error: %d %v", code, err)
	}
	response = `{"error":{"code":2,"error_subcode":99,"is_transient":true,"message":"test-secret"}}`
	if code, err := reportMetaConversion(tok, metaInstallEvent); code != -2 || err == nil || !strings.Contains(err.Error(), "Graph code 2") || strings.Contains(err.Error(), "test-secret") {
		t.Fatalf("transient Graph failure not classified safely: %d %v", code, err)
	}
	if code, err := reportMetaConversion(&Token{}, metaInstallEvent); code == 0 || err == nil {
		t.Fatal("unconfirmed install accepted")
	}
}
