package consolev2

import (
	"net/http"
	"testing"

	"eigenflux_server/pkg/config"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestConsoleMultipleOrigins(t *testing.T) {
	primary := "https://www.eigenflux.ai"
	secondary := "https://www.eigenflux.net"
	for _, tc := range []struct {
		name, origin, host string
		allowed            bool
	}{
		{"primary", primary, "www.eigenflux.ai", true},
		{"secondary", secondary, "www.eigenflux.net", true},
		{"cross trusted domains", primary, "www.eigenflux.net", false},
		{"reverse cross trusted domains", secondary, "www.eigenflux.ai", false},
		{"unknown same host", "https://evil.test", "evil.test", false},
		{"suffix attack", "https://www.eigenflux.net.evil.test", "www.eigenflux.net.evil.test", false},
		{"insecure", "http://www.eigenflux.net", "www.eigenflux.net", false},
		{"wrong port", "https://www.eigenflux.net:444", "www.eigenflux.net:444", false},
		{"missing", "", "www.eigenflux.net", false},
		{"null", "null", "www.eigenflux.net", false},
		{"path", secondary + "/path", "www.eigenflux.net", false},
		{"query", secondary + "?", "www.eigenflux.net", false},
		{"fragment", secondary + "#", "www.eigenflux.net", false},
		{"credentials", "https://user@www.eigenflux.net", "www.eigenflux.net", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := validConsoleSameOrigin(tc.origin, tc.host, primary, secondary); got != tc.allowed {
				t.Fatalf("REST allowed=%v", got)
			}
			if got := validConsoleWebSocketRequest(tc.origin, tc.host, consoleV2WebSocketProtocol, "", primary, secondary); got != tc.allowed {
				t.Fatalf("WebSocket allowed=%v", got)
			}
		})
	}
	if validConsoleSameOrigin(secondary, "www.eigenflux.net", primary) {
		t.Fatal("secondary trusted without configuration")
	}
	if validConsoleWebSocketRequest(secondary, "www.eigenflux.net", consoleV2WebSocketProtocol, "secret", primary, secondary) {
		t.Fatal("query token accepted")
	}
	if validConsoleWebSocketRequest(secondary, "www.eigenflux.net", "legacy", "", primary, secondary) {
		t.Fatal("wrong protocol accepted")
	}
}

func TestConsoleOriginConfigurationAndLogin(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{ConsoleV2PublicURL: "https://www.eigenflux.ai", ConsoleV2OTPPepper: "test", ConsoleV2AllowedOrigins: []string{"https://www.eigenflux.net"}}
	svc, err := NewService(db, &fixedIDGenerator{}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if svc.publicURL != cfg.ConsoleV2PublicURL || !svc.secureCookie {
		t.Fatal("canonical handoff or cookie policy changed")
	}
	h := server.New(server.WithHostPorts("127.0.0.1:0"))
	svc.Register(h)
	for _, host := range []string{"www.eigenflux.ai", "www.eigenflux.net"} {
		status, payload, _ := performJSON(t, h, http.MethodPost, "https://"+host+"/api/v2/auth/email/challenges", map[string]interface{}{}, ut.Header{Key: "Origin", Value: "https://" + host})
		if status != http.StatusBadRequest || responseErrorCode(t, payload) != "INVALID_REQUEST" {
			t.Fatalf("%s: %d %#v", host, status, payload)
		}
	}
	for _, bad := range []string{"https://*.eigenflux.net", "http://www.eigenflux.net", "https://www.eigenflux.net/path", "https://www.eigenflux.net?x=1", "https://user@www.eigenflux.net", "https://www.eigenflux.net#fragment", "null"} {
		cfg.ConsoleV2AllowedOrigins = []string{bad}
		if _, err := NewService(db, &fixedIDGenerator{}, cfg); err == nil {
			t.Fatalf("invalid configured origin accepted: %s", bad)
		}
	}
}
