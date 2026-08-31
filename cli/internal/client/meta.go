package client

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"cli.eigenflux.ai/internal/config"
)

// Meta holds client environment metadata sent as HTTP headers on every request.
type Meta struct {
	OS         string // e.g. "darwin/arm64"
	TZ         string // e.g. "Asia/Shanghai"
	Lang       string // e.g. "zh-CN"
	Host       string // e.g. "openclaw/0.0.10", "claude-code/0.0.5", "terminal"
	DeviceName string // user-visible computer name, e.g. "Lynn-MacBook-Pro"
	Model      string // e.g. "claude-opus-4-8" (the model the host agent runs as)
	Channel    string // e.g. "feishu", "cli", "telegram"
	ClientID   string // e.g. "a1b2c3d4"
	CLIVersion string // e.g. "0.0.16" — enables backend to know which skills bundle the client carries
}

// SetHeaders writes all non-empty Meta fields to the given http.Header.
func (m Meta) SetHeaders(h http.Header) {
	if m.OS != "" {
		h.Set("X-Client-OS", m.OS)
	}
	if m.TZ != "" {
		h.Set("X-Client-TZ", m.TZ)
	}
	if m.Lang != "" {
		h.Set("X-Client-Lang", m.Lang)
	}
	if m.Host != "" {
		h.Set("X-Client-Host", m.Host)
	}
	if m.DeviceName != "" {
		h.Set("X-Client-Device-Name", m.DeviceName)
	}
	if m.Model != "" {
		h.Set("X-Client-Model", m.Model)
	}
	if m.Channel != "" {
		h.Set("X-Client-Channel", m.Channel)
	}
	if m.ClientID != "" {
		h.Set("X-Client-ID", m.ClientID)
	}
	if m.CLIVersion != "" {
		h.Set("X-Client-CLI-Version", m.CLIVersion)
	}
}

// ResolveMeta collects environment metadata from the current runtime.
func ResolveMeta() Meta {
	return Meta{
		OS:         runtime.GOOS + "/" + runtime.GOARCH,
		TZ:         resolveTimezone(),
		Lang:       resolveLanguage(),
		Host:       resolveRuntimeHost(),
		DeviceName: resolveDeviceName(),
		Model:      os.Getenv("EIGENFLUX_MODEL"),
		Channel:    resolveEnvOrDefault("EIGENFLUX_CHANNEL", "cli"),
		ClientID:   loadOrCreateClientID(),
	}
}

func resolveDeviceName() string {
	name := strings.TrimSpace(os.Getenv("EIGENFLUX_DEVICE_NAME"))
	if name == "" {
		name, _ = os.Hostname()
	}
	name = strings.TrimSuffix(strings.TrimSpace(name), ".local")
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	runes := []rune(strings.TrimSpace(name))
	if len(runes) > 128 {
		runes = runes[:128]
	}
	return string(runes)
}

// resolveRuntimeHost keeps EIGENFLUX_HOST as the explicit override, then uses
// deterministic host process metadata when the host exposes it. Codex exports
// process-scoped markers to every command it starts; WorkBuddy exposes its own
// product metadata. Other products are never guessed: their agent can pass a
// known identity through settings push or set EIGENFLUX_HOST in the tool
// environment.
func resolveRuntimeHost() string {
	if host := strings.TrimSpace(os.Getenv("EIGENFLUX_HOST")); host != "" {
		return host
	}
	if host, ok := workBuddyHomeRuntime(); ok {
		return host
	}
	if codexRuntime() {
		return "codex"
	}
	version, ok := workBuddyRuntime()
	if !ok {
		return "terminal"
	}
	if normalized, ok := normalizeRuntimePart(version); ok {
		return "workbuddy/" + normalized
	}
	return "workbuddy"
}

func workBuddyHomeRuntime() (string, bool) {
	for dir := filepath.Clean(config.HomeDir()); ; dir = filepath.Dir(dir) {
		base := strings.ToLower(filepath.Base(dir))
		if base == ".workbuddy" || base == "workbuddy" {
			if version, ok := normalizeRuntimePart(os.Getenv("WORKBUDDY_APP_VERSION")); ok {
				return "workbuddy/" + version, true
			}
			return "workbuddy", true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
	}
}

func codexRuntime() bool {
	for _, key := range []string{"CODEX_THREAD_ID", "CODEX_SANDBOX", "CODEX_SHELL"} {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			return true
		}
	}
	return strings.EqualFold(strings.TrimSpace(os.Getenv("CODEX_INTERNAL_ORIGINATOR_OVERRIDE")), "Codex Desktop")
}

func workBuddyRuntime() (string, bool) {
	workBuddyDetected := false
	for _, key := range []string{"WORKBUDDY_APP_NAME", "WORKBUDDY_PRODUCT_NAME"} {
		if strings.EqualFold(strings.TrimSpace(os.Getenv(key)), "workbuddy") {
			workBuddyDetected = true
			if version := strings.TrimSpace(os.Getenv("WORKBUDDY_APP_VERSION")); version != "" {
				return version, true
			}
		}
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(os.Getenv("CODEBUDDY_HOST"))), "workbuddy") {
		workBuddyDetected = true
		if version := strings.TrimSpace(os.Getenv("WORKBUDDY_APP_VERSION")); version != "" {
			return version, true
		}
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("CLIENT_INFO_PRODUCT_NAME")), "workbuddy") {
		workBuddyDetected = true
		if version := strings.TrimSpace(os.Getenv("CLIENT_INFO_PRODUCT_VERSION")); version != "" {
			return version, true
		}
	}
	return "", workBuddyDetected
}

func normalizeRuntimePart(value string) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || len(value) > 64 {
		return "", false
	}
	if strings.IndexFunc(value, func(r rune) bool {
		return !(r == '.' || r == '-' || r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z')
	}) >= 0 {
		return "", false
	}
	return value, true
}

func resolveTimezone() string {
	name := time.Now().Location().String()
	if name != "" && name != "Local" {
		return name
	}
	if tz := os.Getenv("TZ"); tz != "" {
		return tz
	}
	return "UTC"
}

func resolveLanguage() string {
	for _, key := range []string{"LANG", "LC_ALL", "LC_MESSAGES"} {
		if v := os.Getenv(key); v != "" {
			// "zh_CN.UTF-8" → "zh-CN", "en_US.UTF-8" → "en-US"
			v = strings.SplitN(v, ".", 2)[0]
			v = strings.ReplaceAll(v, "_", "-")
			if len(v) > 5 {
				v = v[:5]
			}
			return v
		}
	}
	return ""
}

func resolveEnvOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func loadOrCreateClientID() string {
	dir := config.HomeDir()
	path := filepath.Join(dir, "client_id")

	data, err := os.ReadFile(path)
	if err == nil {
		id := strings.TrimSpace(string(data))
		if len(id) >= 8 {
			return id
		}
	}

	// Generate 8-char hex random ID
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	id := hex.EncodeToString(buf)

	os.MkdirAll(dir, 0700)
	os.WriteFile(path, []byte(id+"\n"), 0600)
	return id
}
