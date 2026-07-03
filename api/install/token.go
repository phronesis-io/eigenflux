package install

import (
	"crypto/rand"
	"regexp"
	"strings"
)

const (
	base62Charset = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	tokenPrefix   = "EF-"
	randomLen     = 8 // 62^8 ≈ 2.18e14 of space
	// maxByte is the largest multiple of 62 below 256; bytes >= it are rejected
	// to keep base62 output unbiased (no modulo skew toward the first symbols).
	maxByte = 62 * 4 // 248
)

// base62 returns n unbiased base62 characters from crypto/rand. Bytes >= 248
// are rejected and redrawn, so every character is uniformly distributed.
func base62(n int) string {
	out := make([]byte, 0, n)
	buf := make([]byte, n)
	for len(out) < n {
		if _, err := rand.Read(buf); err != nil {
			panic(err) // crypto/rand failure is unrecoverable (matches agti.NewID)
		}
		for _, b := range buf {
			if b >= maxByte {
				continue
			}
			out = append(out, base62Charset[int(b)%62])
			if len(out) == n {
				break
			}
		}
	}
	return string(out)
}

// NewToken returns a fresh attribution token: "EF-" + 8 random base62 chars.
// Channel and UTM data live in the DB row, not the token — the token is an
// opaque key, so it leaks no campaign signal and spends its full length on
// collision resistance.
func NewToken() string {
	return tokenPrefix + base62(randomLen)
}

var tokenRe = regexp.MustCompile(`^EF-[0-9A-Za-z]{8}$`)

// ValidTokenFormat is a cheap guard run before any DB lookup on /report.
func ValidTokenFormat(token string) bool {
	return tokenRe.MatchString(token)
}

// channelMap normalizes a utm_source to a recognizable channel name kept in the
// DB `channel` column for cheap per-channel funnel rollups. This map is the
// single source of truth (server-side); the landing page only sends raw utm.
var channelMap = map[string]string{
	"google":   "google",
	"facebook": "facebook",
	"meta":     "facebook",
	"twitter":  "twitter",
	"x":        "twitter",
	"linkedin": "linkedin",
	"reddit":   "reddit",
	"tiktok":   "tiktok",
	"youtube":  "youtube",
	"discord":  "discord",
	"telegram": "telegram",
	"wechat":   "wechat",
	"weixin":   "wechat",
	"weibo":    "weibo",
	"baidu":    "baidu",
	"organic":  "organic",
	"direct":   "direct",
	"referral": "referral",
	// Xiaohongshu (小红书 / RED) aliases — all normalize to one bucket.
	"xiaohongshu": "xiaohongshu",
	"xhs":         "xiaohongshu",
	"redbook":     "xiaohongshu",
	"rednote":     "xiaohongshu",
	"小红书":         "xiaohongshu",
}

// normalizeChannel maps a raw utm_source to a channel bucket. Unknown non-empty
// sources are lowercased and truncated (kept rather than discarded, so new
// campaigns still group under their own source); empty source becomes "unknown".
func normalizeChannel(utmSource string) string {
	s := strings.ToLower(strings.TrimSpace(utmSource))
	if s == "" {
		return "unknown"
	}
	if c, ok := channelMap[s]; ok {
		return c
	}
	if len(s) > 32 {
		s = s[:32]
	}
	return s
}

// deriveChannel resolves the channel bucket for a mint. An explicit utm_source
// wins; but 聚光 auto-appends only click_id (not utm_source) to the landing URL,
// so when the source is missing/unknown a platform click id is decisive —
// click_id ⇒ xiaohongshu, twclid ⇒ twitter — rather than logging the paid click
// as "unknown".
func deriveChannel(utmSource, clickID, twclid string) string {
	c := normalizeChannel(utmSource)
	if c == "unknown" {
		if clickID != "" {
			return "xiaohongshu"
		}
		if twclid != "" {
			return "twitter"
		}
	}
	return c
}
