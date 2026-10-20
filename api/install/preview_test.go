package install

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
)

func TestPreviewUAClassification(t *testing.T) {
	for _, ua := range []string{
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_6) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4450.0 Safari/537.36 LarkUrl",
		"Mozilla/5.0 (compatible; redditbot/1.0; +http://www.reddit.com/feedback)",
		"Googlebot/2.1", "Google-InspectionTool/1.0", "bingpreview/1.0", "GoogleWebPreview", "Bytespider", "bingbot/2.0",
		"facebookexternalhit/1.1", "Twitterbot/1.0", "Slackbot-LinkExpanding 1.0",
		"TelegramBot (like TwitterBot)", "Discordbot/2.0", "SkypeUriPreview",
		"WhatsApp/2.24.1", "QQUrlPreview/1.0", "WeChatPreview/1.0",
		"Mozilla/5.0 HeadlessChrome/120.0", "Pinterestbot/1.0",
		"Mozilla/5.0 (compatible; Googlebot-News/2.1)",
		"Mozilla/5.0 (compatible; +https://developers.google.com/+/web/snippet)",
	} {
		t.Run(ua, func(t *testing.T) {
			if !isPreviewBot(ua) {
				t.Fatalf("preview UA accepted: %s", ua)
			}
		})
	}
	for _, ua := range []string{
		"", "curl/8.7.1", "eigenflux-cli/1.0", "Python-urllib/3.12",
		"Mozilla/5.0 Chrome/152.0.0.0 Safari/537.36",
		"Mozilla/5.0 (iPhone) Mobile/15E148 MicroMessenger/8.0.60",
		"Mozilla/5.0 (Linux; Android 12) Chrome/114.0 Mobile Safari/537.36 QQ/9.1.0 V1_AND_SQ_9.1.0",
		"Mozilla/5.0 (Linux; Android 12) Mobile Safari/537.36 BiliApp/9140400 mobi_app/android",
		"Mozilla/5.0 (iPhone) Mobile/15E148 Safari/604.1 Xiaohongshu/8.0",
		"Mozilla/5.0 (iPhone) Mobile/15E148 Safari/604.1 YouTube/19.0 Reddit/2026.1",
		"Mozilla/5.0 Chrome/152.0 Safari/537.36 Lark/7.0",
		"Mozilla/5.0 Chrome/152.0 Safari/537.36 WhatsApp/2.24.1",
		"Mozilla/5.0 BiliApp/9150300 metadata/2aWQiOiJYVTdBOTQyN0NBMDA0Mz",
		"Mozilla/5.0 BiliApp/9150300 metadata/xMDg3QjJDODZBOTdBNDYyRUUwRD",
		"Mozilla/5.0 BiliApp/9150300 metadata/bot",
		"Mozilla/5.0 app_preview/1.0 feature=preview",
		"Mozilla/5.0 +https://example.invalid/bot",
	} {
		t.Run(ua, func(t *testing.T) {
			if isPreviewBot(ua) {
				t.Fatalf("real browser or installer excluded: %s", ua)
			}
		})
	}
}

func TestPreviewIdentitySurvivesUATruncation(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	c := app.NewContext(0)
	c.Request.Header.Set("User-Agent", "Mozilla/5.0 "+strings.Repeat("a", 520)+" LarkUrl/1.0")
	logPreview(c, "landing_token", "", "EFI-hhPnlK")
	var event map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &event); err != nil {
		t.Fatal(err)
	}
	if event["crawler_identity"] != "larkurl" || len(event["user_agent"].(string)) != 512 {
		t.Fatalf("missing bounded detection evidence: %#v", event)
	}
}

func TestMintPreviewExitsBeforeAttribution(t *testing.T) {
	for _, header := range []struct{ name, value string }{
		{"User-Agent", "Mozilla/5.0 Chrome/91.0 Safari/537.36 LarkUrl"},
		{"User-Agent", "redditbot/1.0"},
		{"User-Agent", "Bytespider"},
		{"Purpose", "prefetch"}, {"Sec-Purpose", "prefetch;prerender"},
		{"X-Purpose", "preview"}, {"X-Moz", "prefetch"},
	} {
		t.Run(header.name+header.value, func(t *testing.T) {
			c := app.NewContext(0)
			c.Request.Header.Set(header.name, header.value)
			c.Request.SetBodyString(`{"invite_code":"EFI-hhPnlK","utm_source":"youtube"}`)
			// No DB or Redis is configured: reaching attribution would panic.
			mintRef(context.Background(), c)
			if c.Response.StatusCode() != http.StatusForbidden {
				t.Fatalf("status=%d body=%s", c.Response.StatusCode(), c.Response.Body())
			}
			var body struct {
				Code int             `json:"code"`
				Data json.RawMessage `json:"data"`
			}
			if err := json.Unmarshal(c.Response.Body(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Code != 403 || (string(body.Data) != "null" && len(body.Data) != 0) {
				t.Fatalf("unexpected token response: %s", c.Response.Body())
			}
			if string(c.Response.Header.Peek("Cache-Control")) != "no-store" {
				t.Fatal("preview response must not be cached")
			}
		})
	}
}

func TestPreviewFetchDoesNotStampExistingToken(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	h := server.New()
	h.GET("/r/:ref", serveRef)
	resp := ut.PerformRequest(h.Engine, http.MethodGet, "/r/EF-1234abcd", nil,
		ut.Header{Key: "User-Agent", Value: "LarkUrl"}).Result()
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode(), resp.Body())
	}
	var event map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &event); err != nil {
		t.Fatal(err)
	}
	if event["ev"] != "install_preview_blocked" || event["stage"] != "token_instruction" ||
		event["ref"] != "EF-1234abcd" || event["reason"] != "crawler_user_agent" {
		t.Fatalf("unexpected preview event: %#v", event)
	}
}

func TestMintPreviewLogsChannelWithoutCreatingLanding(t *testing.T) {
	for _, tc := range []struct{ body, code string }{
		{`{"invite_code":"EFI-hhPnlK"}`, "EFI-hhPnlK"},
		{`{"invite_code":"not-a-code","referrer":"private-query"}`, ""},
		{`{`, ""},
	} {
		t.Run(tc.body, func(t *testing.T) {
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			c := app.NewContext(0)
			c.Request.Header.Set("Sec-Purpose", "prefetch;prerender")
			c.Request.Header.Set("User-Agent", "Mozilla/5.0 Chrome/152.0")
			c.Request.SetBodyString(tc.body)
			mintRef(context.Background(), c)
			if c.Response.StatusCode() != http.StatusForbidden {
				t.Fatalf("preview reached landing creation: %d", c.Response.StatusCode())
			}
			var event map[string]any
			if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &event); err != nil {
				t.Fatal(err)
			}
			if event["ev"] != "install_preview_blocked" || event["stage"] != "landing_token" ||
				event["invite_code"] != tc.code || event["reason"] != "sec-purpose:prefetch" {
				t.Fatalf("unexpected preview event: %#v", event)
			}
			if bytes.Contains(logs.Bytes(), []byte("private-query")) {
				t.Fatal("request body leaked into preview log")
			}
		})
	}
}

func TestPreviewHeaderTokenBoundaries(t *testing.T) {
	for _, value := range []string{"not-preview", "no-prefetch", "prerendered", "navigate"} {
		c := app.NewContext(0)
		c.Request.Header.Set("Sec-Purpose", value)
		if isPreviewRequest(c) {
			t.Fatalf("non-preview purpose was excluded: %s", value)
		}
	}
}

func TestNormalBrowserReachesMintValidation(t *testing.T) {
	for _, ua := range []string{"Mozilla/5.0 MicroMessenger/8.0", "Mozilla/5.0 QQ/9.0", "Mozilla/5.0 BiliApp/9.0", "Mozilla/5.0 Xiaohongshu/8.0"} {
		c := app.NewContext(0)
		c.Request.Header.Set("User-Agent", ua)
		c.Request.Header.Set("Sec-Purpose", "navigate")
		c.Request.SetBodyString("{")
		mintRef(context.Background(), c)
		if c.Response.StatusCode() != http.StatusBadRequest {
			t.Fatalf("browser blocked: %s status=%d", ua, c.Response.StatusCode())
		}
	}
}
