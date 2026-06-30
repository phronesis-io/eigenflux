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
		OS:       runtime.GOOS + "/" + runtime.GOARCH,
		TZ:       resolveTimezone(),
		Lang:     resolveLanguage(),
		Host:     resolveEnvOrDefault("EIGENFLUX_HOST", "terminal"),
		Model:    os.Getenv("EIGENFLUX_MODEL"),
		Channel:  resolveEnvOrDefault("EIGENFLUX_CHANNEL", "cli"),
		ClientID: loadOrCreateClientID(),
	}
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
