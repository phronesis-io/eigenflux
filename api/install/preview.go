package install

import (
	"regexp"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
)

// Match crawler identities, not platform names: their in-app browsers are
// real visits too. LarkUrl was observed minting tokens from Feishu previews.
var previewUARe = regexp.MustCompile(`(?i)bot|crawl|spider|preview|slurp|headless|facebookexternalhit|facebookcatalog|embedly|vkshare|larkurl|google-inspectiontool|developers\.google\.com/\+/web/snippet`)

func isPreviewBot(userAgent string) bool {
	if previewUARe.MatchString(userAgent) {
		return true
	}
	// WhatsApp's unfurler identifies itself without a browser UA. Do not
	// exclude browser UAs that merely carry the WhatsApp app marker.
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(userAgent)), "whatsapp/")
}

func isPreviewRequest(c *app.RequestContext) bool {
	if isPreviewBot(string(c.GetHeader("User-Agent"))) {
		return true
	}
	for _, name := range []string{"Purpose", "Sec-Purpose", "X-Purpose", "X-Moz"} {
		value := strings.ToLower(string(c.GetHeader(name)))
		for _, part := range strings.FieldsFunc(value, func(r rune) bool {
			return r == ';' || r == ',' || r == '=' || r == ' ' || r == '\t'
		}) {
			if part == "prefetch" || part == "preview" || part == "prerender" {
				return true
			}
		}
	}
	return false
}
